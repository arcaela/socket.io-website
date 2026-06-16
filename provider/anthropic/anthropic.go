// Package anthropic implements provider.Provider against Anthropic's
// /v1/messages API with streaming. Auth is API key (ANTHROPIC_API_KEY),
// no OAuth.
//
// Anthropic differs from OpenAI in two structural ways the agent has to
// see consistently:
//   - System prompt is a TOP-LEVEL request field, not a message.
//   - Tool use is interleaved within message `content` blocks rather than
//     a separate `tool_calls` field on the assistant message.
//
// We hide both differences inside this package; from outside, it is "yet
// another provider.Provider".
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/arcaela/mini-cli/provider"
)

const (
	DefaultModel   = "claude-sonnet-4-5"
	defaultBaseURL = "https://api.anthropic.com"
	messagesPath   = "/v1/messages"
	apiVersion     = "2023-06-01"
)

func init() {
	provider.Register("anthropic", provider.Factory{
		New:          func(ctx context.Context) (provider.Provider, error) { return New(ctx) },
		DefaultModel: DefaultModel,
		Commands: map[string]provider.Command{
			"whoami": {
				Description: "Show the API key tail in use and the base URL.",
				Run:         runWhoami,
			},
		},
	})
}

type Provider struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

func New(_ context.Context) (*Provider, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is not set")
	}
	base := os.Getenv("ANTHROPIC_BASE_URL")
	if base == "" {
		base = defaultBaseURL
	}
	return &Provider{
		APIKey:  key,
		BaseURL: strings.TrimRight(base, "/"),
		HTTP:    &http.Client{Timeout: 0},
	}, nil
}

func (p *Provider) Name() string { return "anthropic" }

func (p *Provider) Account(_ context.Context) (*provider.AccountInfo, error) {
	masked := "(unset)"
	if n := len(p.APIKey); n >= 4 {
		masked = "…" + p.APIKey[n-4:]
	}
	return &provider.AccountInfo{
		Provider: "anthropic",
		Email:    masked,
		Tier:     "api-key",
		Extra:    map[string]any{"base_url": p.BaseURL},
	}, nil
}

// =============================================================================
// Wire types (subset of Anthropic Messages API)
// =============================================================================

type apiMessage struct {
	Role    string         `json:"role"`    // user | assistant
	Content []contentBlock `json:"content"` // text / tool_use / tool_result
}

type contentBlock struct {
	Type string `json:"type"` // "text" | "tool_use" | "tool_result"

	// type=text
	Text string `json:"text,omitempty"`

	// type=tool_use
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`

	// type=tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	// `content` of a tool_result is a string for plain results, or an array of
	// blocks ([{text},{image}]) when the tool attached images for the model.
	Content any `json:"content,omitempty"`
}

type apiToolDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type apiRequest struct {
	Model     string         `json:"model"`
	System    string         `json:"system,omitempty"`
	Messages  []apiMessage   `json:"messages"`
	Tools     []apiToolDecl  `json:"tools,omitempty"`
	MaxTokens int            `json:"max_tokens"`
	Stream    bool           `json:"stream"`
}

// Streaming events Anthropic emits. We only care about a small subset.
type streamEvent struct {
	Type         string                 `json:"type"`
	Index        int                    `json:"index"`
	Delta        streamDelta            `json:"delta"`
	ContentBlock streamContentBlockStart `json:"content_block"`
	Usage        *streamUsage           `json:"usage"`
	Message      *struct {
		Usage *streamUsage `json:"usage"`
	} `json:"message"`
}

type streamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
}

type streamContentBlockStart struct {
	Type  string         `json:"type"`
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

type streamUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// =============================================================================
// Generate
// =============================================================================

const defaultMaxTokens = 4096

func (p *Provider) Generate(ctx context.Context, req provider.GenerateRequest) (<-chan provider.Event, error) {
	system, messages := messagesToAnthropic(req.Messages)
	body := apiRequest{
		Model:     req.Model,
		System:    system,
		Messages:  messages,
		Tools:     toolsToAnthropic(req.Tools),
		MaxTokens: defaultMaxTokens,
		Stream:    true,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.BaseURL+messagesPath, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.APIKey)
	httpReq.Header.Set("anthropic-version", apiVersion)
	httpReq.Header.Set("Accept", "text/event-stream")

	res, err := p.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		defer res.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, fmt.Errorf("anthropic %d: %s", res.StatusCode, strings.TrimSpace(string(b)))
	}

	out := make(chan provider.Event, 32)
	go func() {
		defer close(out)
		defer res.Body.Close()

		// We buffer partial tool_use blocks until content_block_stop fires.
		type partial struct {
			id   string
			name string
			json strings.Builder
		}
		partials := map[int]*partial{}
		var usage *provider.Usage

		scanner := bufio.NewScanner(res.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			var ev streamEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}

			switch ev.Type {
			case "message_start":
				if ev.Message != nil && ev.Message.Usage != nil {
					usage = &provider.Usage{
						PromptTokens: ev.Message.Usage.InputTokens,
						OutputTokens: ev.Message.Usage.OutputTokens,
						TotalTokens:  ev.Message.Usage.InputTokens + ev.Message.Usage.OutputTokens,
					}
				}
			case "content_block_start":
				if ev.ContentBlock.Type == "tool_use" {
					partials[ev.Index] = &partial{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
				}
			case "content_block_delta":
				switch ev.Delta.Type {
				case "text_delta":
					if ev.Delta.Text != "" {
						out <- provider.Event{Kind: provider.EventTextDelta, Text: ev.Delta.Text}
					}
				case "input_json_delta":
					if p, ok := partials[ev.Index]; ok {
						p.json.WriteString(ev.Delta.PartialJSON)
					}
				}
			case "content_block_stop":
				if p, ok := partials[ev.Index]; ok && p.name != "" {
					args := map[string]any{}
					if s := p.json.String(); s != "" {
						_ = json.Unmarshal([]byte(s), &args)
					}
					out <- provider.Event{
						Kind: provider.EventToolCallRequest,
						ToolCall: &provider.ToolCall{
							ID:   p.id,
							Name: p.name,
							Args: args,
						},
					}
					delete(partials, ev.Index)
				}
			case "message_delta":
				if ev.Usage != nil {
					if usage == nil {
						usage = &provider.Usage{}
					}
					// message_delta carries the cumulative output_tokens.
					usage.OutputTokens = ev.Usage.OutputTokens
					usage.TotalTokens = usage.PromptTokens + ev.Usage.OutputTokens
				}
			case "message_stop":
				// end-of-stream; loop will exit naturally.
			case "error":
				out <- provider.Event{Kind: provider.EventError, Err: fmt.Errorf("anthropic stream error")}
				return
			}
		}

		out <- provider.Event{Kind: provider.EventTurnDone, Usage: usage}
	}()
	return out, nil
}

// =============================================================================
// Translation
// =============================================================================

// messagesToAnthropic returns (system, messages). Anthropic puts the system
// prompt out of band; everything else maps to user/assistant content blocks.
func messagesToAnthropic(msgs []provider.Message) (string, []apiMessage) {
	var sys strings.Builder
	var out []apiMessage

	for _, m := range msgs {
		if m.Role == provider.RoleSystem {
			if sys.Len() > 0 {
				sys.WriteString("\n\n")
			}
			sys.WriteString(m.Text)
			continue
		}

		switch {
		case m.ToolCall != nil:
			out = append(out, apiMessage{
				Role: "assistant",
				Content: []contentBlock{{
					Type:  "tool_use",
					ID:    m.ToolCall.ID,
					Name:  m.ToolCall.Name,
					Input: m.ToolCall.Args,
				}},
			})
		case m.ToolResult != nil:
			text := ""
			if m.ToolResult.Error != "" {
				text = "error: " + m.ToolResult.Error
			} else {
				b, _ := json.Marshal(m.ToolResult.Result)
				text = string(b)
			}
			// Anthropic embeds images directly in the tool_result content array.
			var blockContent any = text
			if len(m.ToolResult.Images) > 0 {
				arr := []map[string]any{{"type": "text", "text": text}}
				for _, img := range m.ToolResult.Images {
					arr = append(arr, map[string]any{
						"type": "image",
						"source": map[string]any{
							"type":       "base64",
							"media_type": img.MimeType,
							"data":       img.Base64(),
						},
					})
				}
				blockContent = arr
			}
			out = append(out, apiMessage{
				Role: "user", // tool_result blocks live inside user turns
				Content: []contentBlock{{
					Type:      "tool_result",
					ToolUseID: m.ToolResult.CallID,
					IsError:   m.ToolResult.Error != "",
					Content:   blockContent,
				}},
			})
		default:
			role := string(m.Role)
			if role != "user" && role != "assistant" {
				role = "user"
			}
			out = append(out, apiMessage{
				Role:    role,
				Content: []contentBlock{{Type: "text", Text: m.Text}},
			})
		}
	}
	return sys.String(), out
}

func toolsToAnthropic(tools []provider.ToolDecl) []apiToolDecl {
	if len(tools) == 0 {
		return nil
	}
	out := make([]apiToolDecl, 0, len(tools))
	for _, t := range tools {
		out = append(out, apiToolDecl{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Schema,
		})
	}
	return out
}

// =============================================================================
// Subcommand
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
