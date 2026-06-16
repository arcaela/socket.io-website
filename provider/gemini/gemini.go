// Package gemini implements provider.Provider on top of cloudcode-pa
// (Gemini Code Assist for individuals).
package gemini

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/arcaela/mini-cli/provider"
)

type Provider struct {
	client    *Client
	projectID string
	tierID    string
	email     string
}

// New constructs the Gemini provider, refreshing tokens and onboarding the
// user if needed. Call this once at startup.
func New(ctx context.Context) (*Provider, error) {
	// Move legacy ~/.mini/creds.json (pre-refactor) into the provider-
	// namespaced location. No-op when already migrated.
	_ = MigrateLegacyState()

	token, creds, err := EnsureAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	client := newClient(token)
	projectID, tierID, err := client.EnsureOnboarded(ctx)
	if err != nil {
		return nil, fmt.Errorf("onboard: %w", err)
	}
	email := creds.Email
	if email == "" {
		if ui, err := client.UserInfo(ctx); err == nil {
			email = ui.Email
		}
	}
	return &Provider{
		client:    client,
		projectID: projectID,
		tierID:    tierID,
		email:     email,
	}, nil
}

func (p *Provider) Name() string { return "gemini" }

func (p *Provider) Account(ctx context.Context) (*provider.AccountInfo, error) {
	return &provider.AccountInfo{
		Provider: "gemini",
		Email:    p.email,
		Tier:     p.tierID,
		Project:  p.projectID,
	}, nil
}

// Generate streams events for one model turn. Channel is closed at end.
func (p *Provider) Generate(ctx context.Context, req provider.GenerateRequest) (<-chan provider.Event, error) {
	contents, systemInstruction, err := messagesToContents(req.Messages)
	if err != nil {
		return nil, err
	}
	tools := toolDeclsToGemini(req.Tools)

	out := make(chan provider.Event, 32)
	go func() {
		defer close(out)
		emit := func(e provider.Event) {
			select {
			case out <- e:
			case <-ctx.Done():
			}
		}

		final, err := p.client.StreamGenerateContent(
			ctx,
			req.Model,
			p.projectID,
			randomID(),
			contents,
			systemInstruction,
			tools,
			func(part Part) {
				switch {
				case part.Thought && part.Text != "":
					emit(provider.Event{Kind: provider.EventThoughtDelta, Text: part.Text})
				case part.Text != "":
					emit(provider.Event{Kind: provider.EventTextDelta, Text: part.Text})
				case part.FunctionCall != nil:
					tc := geminiFunctionCallToToolCall(part.FunctionCall)
					emit(provider.Event{Kind: provider.EventToolCallRequest, ToolCall: tc})
				}
			},
		)
		if err != nil {
			emit(provider.Event{Kind: provider.EventError, Err: err})
			return
		}
		var usage *provider.Usage
		if final != nil && final.Response != nil && final.Response.UsageMetadata != nil {
			u := final.Response.UsageMetadata
			usage = &provider.Usage{
				PromptTokens:  u.PromptTokenCount,
				OutputTokens:  u.CandidatesTokenCount,
				ThoughtTokens: u.ThoughtsTokenCount,
				TotalTokens:   u.TotalTokenCount,
			}
		}
		emit(provider.Event{Kind: provider.EventTurnDone, Usage: usage})
	}()
	return out, nil
}

// ----- Translation: provider.Message ↔ Content -----

// messagesToContents splits provider Messages into Gemini's two-channel
// representation: regular conversation `contents` and a separate
// `systemInstruction`. Multiple system messages are concatenated. Gemini
// treats systemInstruction as out-of-band priming and does NOT bill it
// against the conversation history (so it's free to re-send each turn).
func messagesToContents(msgs []provider.Message) ([]Content, *Content, error) {
	var sysBuf strings.Builder
	out := make([]Content, 0, len(msgs))

	for _, m := range msgs {
		if m.Role == provider.RoleSystem {
			if sysBuf.Len() > 0 {
				sysBuf.WriteString("\n\n")
			}
			sysBuf.WriteString(m.Text)
			continue
		}
		c := Content{}
		switch m.Role {
		case provider.RoleUser:
			c.Role = "user"
		case provider.RoleAssistant:
			c.Role = "model"
		case provider.RoleTool:
			c.Role = "user"
		default:
			c.Role = "user"
		}
		switch {
		case m.ToolCall != nil:
			c.Role = "model"
			c.Parts = []Part{{
				FunctionCall: map[string]any{
					"name": m.ToolCall.Name,
					"args": m.ToolCall.Args,
				},
			}}
		case m.ToolResult != nil:
			c.Role = "user"
			respPayload := map[string]any{}
			if m.ToolResult.Error != "" {
				respPayload["error"] = m.ToolResult.Error
			} else {
				respPayload["result"] = m.ToolResult.Result
			}
			// The functionResponse plus any images the tool emitted, all in one
			// user Content so Gemini sees the result and the image together.
			parts := []Part{{
				FunctionResponse: map[string]any{
					"name":     m.ToolResult.Name,
					"response": respPayload,
				},
			}}
			for _, img := range m.ToolResult.Images {
				parts = append(parts, Part{InlineData: &InlineData{
					MimeType: img.MimeType,
					Data:     base64.StdEncoding.EncodeToString(img.Data),
				}})
			}
			c.Parts = parts
		default:
			c.Parts = []Part{{Text: m.Text}}
		}
		out = append(out, c)
	}

	var systemInstruction *Content
	if sysBuf.Len() > 0 {
		systemInstruction = &Content{
			// Gemini's systemInstruction Content typically uses role "user".
			Role:  "user",
			Parts: []Part{{Text: sysBuf.String()}},
		}
	}
	return out, systemInstruction, nil
}

func geminiFunctionCallToToolCall(fc map[string]any) *provider.ToolCall {
	tc := &provider.ToolCall{
		ID:   randomID(),
		Args: map[string]any{},
	}
	if name, ok := fc["name"].(string); ok {
		tc.Name = name
	}
	if args, ok := fc["args"].(map[string]any); ok {
		tc.Args = args
	}
	return tc
}

func toolDeclsToGemini(decls []provider.ToolDecl) []map[string]any {
	out := make([]map[string]any, 0, len(decls))
	for _, d := range decls {
		out = append(out, map[string]any{
			"name":        d.Name,
			"description": d.Description,
			"parameters":  d.Schema,
		})
	}
	return out
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
