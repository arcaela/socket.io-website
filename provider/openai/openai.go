// Package openai implements provider.Provider against OpenAI's
// chat-completions API (and Anthropic/Groq/Together compatibles that speak
// the same wire format via a different base URL).
//
// Auth is plain API key: set OPENAI_API_KEY in the environment. No OAuth
// flow, no per-account onboarding — register the provider via init() and
// the CLI picks it up like any other.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/arcaela/mini-cli/provider"
)

const (
	DefaultModel    = "gpt-5"
	defaultBaseURL  = "https://api.openai.com/v1"
	chatPath        = "/chat/completions"
	defaultTimeoutS = 0 // streaming: no global timeout
)

func init() {
	provider.Register("openai", provider.Factory{
		New:          func(ctx context.Context) (provider.Provider, error) { return New(ctx) },
		DefaultModel: DefaultModel,
		Commands: map[string]provider.Command{
			"whoami": {
				Description: "Show which API key is in use (last 4 chars only) and base URL.",
				Run:         runWhoami,
			},
		},
	})
}

// Provider implements provider.Provider for OpenAI-compatible endpoints.
type Provider struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

// New reads OPENAI_API_KEY (required) and optional OPENAI_BASE_URL.
func New(_ context.Context) (*Provider, error) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is not set (export it or use the gemini provider)")
	}
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = defaultBaseURL
	}
	return &Provider{
		APIKey:  key,
		BaseURL: strings.TrimRight(base, "/"),
		HTTP:    &http.Client{Timeout: 0},
	}, nil
}

func (p *Provider) Name() string { return "openai" }

func (p *Provider) Account(_ context.Context) (*provider.AccountInfo, error) {
	masked := "(unset)"
	if n := len(p.APIKey); n >= 4 {
		masked = "…" + p.APIKey[n-4:]
	}
	return &provider.AccountInfo{
		Provider: "openai",
		Email:    masked,    // we have no email, surface the key tail instead
		Tier:     "api-key", // API-key tiering is invisible to clients
		Extra: map[string]any{
			"base_url": p.BaseURL,
		},
	}, nil
}

// =============================================================================
// Wire types (subset of OpenAI's chat.completions schema)
// =============================================================================

type chatMessage struct {
	Role       string         `json:"role"`              // system | user | assistant | tool
	Content    any            `json:"content,omitempty"` // string, or []part for multimodal user turns
	Name       string         `json:"name,omitempty"`    // for role=tool: the tool name
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // always "function" today
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded
}

type chatRequest struct {
	Model    string         `json:"model"`
	Messages []chatMessage  `json:"messages"`
	Tools    []chatToolDecl `json:"tools,omitempty"`
	Stream   bool           `json:"stream"`
}

type chatToolDecl struct {
	Type     string           `json:"type"` // "function"
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// Streaming delta shape.
type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int          `json:"index"`
				ID       string       `json:"id"`
				Function chatFunction `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// =============================================================================
// Generate (streaming)
// =============================================================================

func (p *Provider) Generate(ctx context.Context, req provider.GenerateRequest) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 32)

	body := chatRequest{
		Model:    req.Model,
		Messages: messagesToChat(req.Messages),
		Tools:    toolsToChat(req.Tools),
		Stream:   true,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.BaseURL+chatPath, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	res, err := p.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, fmt.Errorf("openai %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}

	go func() {
		defer close(out)
		defer res.Body.Close()

		// Buffer tool calls across chunks: OpenAI streams arguments byte by
		// byte, indexed by `tool_calls[i].index`.
		partialCalls := map[int]*chatToolCall{}

		scanner := bufio.NewScanner(res.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

		var usage *provider.Usage

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if payload == "[DONE]" {
				break
			}
			var chunk chatStreamChunk
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				continue // skip malformed chunks
			}

			for _, ch := range chunk.Choices {
				if ch.Delta.Content != "" {
					out <- provider.Event{Kind: provider.EventTextDelta, Text: ch.Delta.Content}
				}
				for _, tc := range ch.Delta.ToolCalls {
					p, ok := partialCalls[tc.Index]
					if !ok {
						p = &chatToolCall{ID: tc.ID, Type: "function"}
						partialCalls[tc.Index] = p
					}
					if tc.Function.Name != "" {
						p.Function.Name = tc.Function.Name
					}
					if tc.Function.Arguments != "" {
						p.Function.Arguments += tc.Function.Arguments
					}
					if tc.ID != "" && p.ID == "" {
						p.ID = tc.ID
					}
				}
				if ch.FinishReason == "tool_calls" {
					// Emit the complete tool calls once arguments are assembled.
					for _, pc := range partialCalls {
						out <- provider.Event{
							Kind:     provider.EventToolCallRequest,
							ToolCall: chatCallToToolCall(pc),
						}
					}
					partialCalls = map[int]*chatToolCall{}
				}
			}
			if chunk.Usage != nil {
				usage = &provider.Usage{
					PromptTokens: chunk.Usage.PromptTokens,
					OutputTokens: chunk.Usage.CompletionTokens,
					TotalTokens:  chunk.Usage.TotalTokens,
				}
			}
		}

		// Flush any tool calls that didn't get an explicit finish_reason
		// (some compatible endpoints skip it).
		for _, pc := range partialCalls {
			if pc.Function.Name != "" {
				out <- provider.Event{
					Kind:     provider.EventToolCallRequest,
					ToolCall: chatCallToToolCall(pc),
				}
			}
		}

		out <- provider.Event{Kind: provider.EventTurnDone, Usage: usage}
	}()

	return out, nil
}

// =============================================================================
// Image generation (provider.ImageGenerator)
// =============================================================================

const imagesPath = "/images/generations"

// GenerateImage calls OpenAI's images API and returns the raw PNG bytes. The
// model is gpt-image-1 by default (override with OPENAI_IMAGE_MODEL); dall-e-3
// is also supported. Both are returned as base64 and decoded here.
func (p *Provider) GenerateImage(ctx context.Context, prompt string, opts provider.ImageGenOptions) ([]provider.Image, error) {
	model := os.Getenv("OPENAI_IMAGE_MODEL")
	if model == "" {
		model = "gpt-image-1"
	}
	size := opts.Size
	if size == "" {
		size = "1024x1024"
	}
	n := opts.Count
	if n <= 0 {
		n = 1
	}
	body := map[string]any{"model": model, "prompt": prompt, "size": size, "n": n}
	// gpt-image-1 always returns base64 and rejects response_format; dall-e
	// needs it set explicitly to get base64 instead of a URL.
	if strings.HasPrefix(model, "dall-e") {
		body["response_format"] = "b64_json"
	}
	payload, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", p.BaseURL+imagesPath, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	res, err := p.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("openai images %d: %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	var decoded struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decode images response: %w", err)
	}
	images := make([]provider.Image, 0, len(decoded.Data))
	for _, d := range decoded.Data {
		if d.B64JSON == "" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(d.B64JSON)
		if err != nil {
			return nil, fmt.Errorf("decode image data: %w", err)
		}
		images = append(images, provider.Image{MimeType: "image/png", Data: b})
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("openai images: response contained no image data")
	}
	return images, nil
}

// =============================================================================
// Translation: provider.Message ↔ OpenAI chat messages
// =============================================================================

func messagesToChat(msgs []provider.Message) []chatMessage {
	out := make([]chatMessage, 0, len(msgs))
	for _, m := range msgs {
		switch {
		case m.ToolCall != nil:
			args, _ := json.Marshal(m.ToolCall.Args)
			out = append(out, chatMessage{
				Role: "assistant",
				ToolCalls: []chatToolCall{{
					ID:       m.ToolCall.ID,
					Type:     "function",
					Function: chatFunction{Name: m.ToolCall.Name, Arguments: string(args)},
				}},
			})
		case m.ToolResult != nil:
			var content string
			if m.ToolResult.Error != "" {
				content = "error: " + m.ToolResult.Error
			} else {
				b, _ := json.Marshal(m.ToolResult.Result)
				content = string(b)
			}
			out = append(out, chatMessage{
				Role:       "tool",
				Name:       m.ToolResult.Name,
				ToolCallID: m.ToolResult.CallID,
				Content:    content,
			})
			// OpenAI tool messages are text-only, so any images ride in a
			// follow-up user turn as image_url data parts.
			if parts := imagePartsToChat(m.ToolResult.Images); parts != nil {
				out = append(out, chatMessage{Role: "user", Content: parts})
			}
		default:
			role := string(m.Role)
			switch role {
			case "assistant", "system", "user", "tool":
				// keep
			default:
				role = "user"
			}
			out = append(out, chatMessage{Role: role, Content: m.Text})
		}
	}
	return out
}

// imagePartsToChat renders provider images as OpenAI image_url content parts
// (base64 data URLs). Returns nil when there are no images.
func imagePartsToChat(images []provider.Image) []map[string]any {
	if len(images) == 0 {
		return nil
	}
	parts := make([]map[string]any, 0, len(images))
	for _, img := range images {
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": img.DataURL()},
		})
	}
	return parts
}

func toolsToChat(tools []provider.ToolDecl) []chatToolDecl {
	if len(tools) == 0 {
		return nil
	}
	out := make([]chatToolDecl, 0, len(tools))
	for _, t := range tools {
		out = append(out, chatToolDecl{
			Type: "function",
			Function: chatToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Schema,
			},
		})
	}
	return out
}

func chatCallToToolCall(p *chatToolCall) *provider.ToolCall {
	args := map[string]any{}
	if p.Function.Arguments != "" {
		_ = json.Unmarshal([]byte(p.Function.Arguments), &args)
	}
	id := p.ID
	if id == "" {
		id = fmt.Sprintf("call-%d", time.Now().UnixNano())
	}
	return &provider.ToolCall{
		ID:   id,
		Name: p.Function.Name,
		Args: args,
	}
}

// =============================================================================
// Subcommand: `mini provider openai whoami`
// =============================================================================

func runWhoami(ctx context.Context, _ []string) error {
	p, err := New(ctx)
	if err != nil {
		return err
	}
	acc, _ := p.Account(ctx)
	b, _ := json.MarshalIndent(acc, "", "  ")
	fmt.Println(string(b))
	return nil
}
