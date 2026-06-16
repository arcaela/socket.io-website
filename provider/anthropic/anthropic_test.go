package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arcaela/mini-cli/provider"
)

// =============================================================================
// Message translation
// =============================================================================

func TestMessagesToAnthropic_SystemAndUserAssistant(t *testing.T) {
	sys, msgs := messagesToAnthropic([]provider.Message{
		{Role: provider.RoleSystem, Text: "be brief"},
		{Role: provider.RoleSystem, Text: "extra rule"},
		{Role: provider.RoleUser, Text: "hi"},
		{Role: provider.RoleAssistant, Text: "hello"},
	})
	if sys != "be brief\n\nextra rule" {
		t.Fatalf("system concat wrong: %q", sys)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 msgs (system extracted), got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content[0].Text != "hi" {
		t.Errorf("user msg: %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content[0].Text != "hello" {
		t.Errorf("assistant msg: %+v", msgs[1])
	}
}

func TestMessagesToAnthropic_ToolUseBlock(t *testing.T) {
	_, msgs := messagesToAnthropic([]provider.Message{
		{Role: provider.RoleAssistant, ToolCall: &provider.ToolCall{
			ID:   "use_1",
			Name: "bash",
			Args: map[string]any{"command": "ls"},
		}},
	})
	if len(msgs) != 1 || msgs[0].Role != "assistant" {
		t.Fatalf("expected assistant msg, got %+v", msgs)
	}
	if len(msgs[0].Content) != 1 || msgs[0].Content[0].Type != "tool_use" {
		t.Fatalf("expected tool_use block: %+v", msgs[0].Content)
	}
	if msgs[0].Content[0].ID != "use_1" || msgs[0].Content[0].Name != "bash" {
		t.Errorf("bad tool_use: %+v", msgs[0].Content[0])
	}
	if msgs[0].Content[0].Input["command"] != "ls" {
		t.Errorf("input not threaded: %+v", msgs[0].Content[0].Input)
	}
}

func TestMessagesToAnthropic_ToolResultGoesInUserTurn(t *testing.T) {
	_, msgs := messagesToAnthropic([]provider.Message{
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{
			CallID: "use_1", Name: "bash", Result: map[string]any{"stdout": "hi"},
		}},
	})
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("tool_result must live in a user turn, got %+v", msgs)
	}
	if msgs[0].Content[0].Type != "tool_result" || msgs[0].Content[0].ToolUseID != "use_1" {
		t.Errorf("bad tool_result block: %+v", msgs[0].Content[0])
	}
}

func TestMessagesToAnthropic_ToolResultError(t *testing.T) {
	_, msgs := messagesToAnthropic([]provider.Message{
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{Name: "bash", Error: "perm"}},
	})
	b := msgs[0].Content[0]
	if !b.IsError {
		t.Error("is_error should be true on error result")
	}
	text, _ := b.Content.(string)
	if !strings.HasPrefix(text, "error: ") {
		t.Errorf("body wrong: %q", b.Content)
	}
}

func TestMessagesToAnthropic_ToolResultWithImage(t *testing.T) {
	_, msgs := messagesToAnthropic([]provider.Message{
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{
			CallID: "use_1", Name: "read", Result: map[string]any{"kind": "image"},
			Images: []provider.Image{{MimeType: "image/png", Data: []byte{1, 2, 3}}},
		}},
	})
	block := msgs[0].Content[0]
	arr, ok := block.Content.([]map[string]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("expected [text, image] content array, got %#v", block.Content)
	}
	if arr[0]["type"] != "text" || arr[1]["type"] != "image" {
		t.Fatalf("bad content blocks: %#v", arr)
	}
	src, _ := arr[1]["source"].(map[string]any)
	if src["media_type"] != "image/png" || src["data"] != "AQID" {
		t.Fatalf("bad image source: %#v", src)
	}
}

func TestToolsToAnthropic(t *testing.T) {
	out := toolsToAnthropic([]provider.ToolDecl{
		{Name: "bash", Description: "run shell", Schema: map[string]any{"type": "object"}},
	})
	if len(out) != 1 || out[0].Name != "bash" || out[0].InputSchema["type"] != "object" {
		t.Fatalf("bad tool decl: %+v", out)
	}
}

// =============================================================================
// Streaming end-to-end
// =============================================================================

func sse(t *testing.T, w http.ResponseWriter, frames []string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	for _, frame := range frames {
		fmt.Fprintf(w, "data: %s\n\n", frame)
	}
}

func TestGenerate_TextStreamAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "k" {
			t.Errorf("missing api-key header: %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != apiVersion {
			t.Errorf("missing anthropic-version: %q", r.Header.Get("anthropic-version"))
		}
		var body apiRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Stream {
			t.Errorf("stream=true expected")
		}
		sse(t, w, []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":42,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hola "}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"mundo"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		})
	}))
	defer srv.Close()

	p := &Provider{APIKey: "k", BaseURL: srv.URL, HTTP: srv.Client()}
	events, err := p.Generate(context.Background(), provider.GenerateRequest{
		Model:    "claude-x",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var usage *provider.Usage
	for ev := range events {
		switch ev.Kind {
		case provider.EventTextDelta:
			text.WriteString(ev.Text)
		case provider.EventTurnDone:
			usage = ev.Usage
		case provider.EventError:
			t.Fatal(ev.Err)
		}
	}
	if text.String() != "Hola mundo" {
		t.Errorf("text reassembly: %q", text.String())
	}
	if usage == nil || usage.PromptTokens != 42 || usage.OutputTokens != 5 || usage.TotalTokens != 47 {
		t.Errorf("usage wrong: %+v", usage)
	}
}

func TestGenerate_ToolUseStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(t, w, []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"use_1","name":"bash"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_stop"}`,
		})
	}))
	defer srv.Close()

	p := &Provider{APIKey: "k", BaseURL: srv.URL, HTTP: srv.Client()}
	events, _ := p.Generate(context.Background(), provider.GenerateRequest{Model: "x"})
	var calls []provider.ToolCall
	for ev := range events {
		if ev.Kind == provider.EventToolCallRequest {
			calls = append(calls, *ev.ToolCall)
		}
	}
	if len(calls) != 1 || calls[0].Name != "bash" || calls[0].ID != "use_1" {
		t.Fatalf("bad calls: %+v", calls)
	}
	if calls[0].Args["command"] != "ls" {
		t.Errorf("args not reassembled: %+v", calls[0].Args)
	}
}

func TestGenerate_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()

	p := &Provider{APIKey: "k", BaseURL: srv.URL, HTTP: srv.Client()}
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Model: "x"})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 surfaced, got %v", err)
	}
}

// =============================================================================
// Factory + registration
// =============================================================================

func TestNew_RequiresAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := New(context.Background()); err == nil {
		t.Fatal("expected missing-key error")
	}
}

func TestAccount_MasksKey(t *testing.T) {
	p := &Provider{APIKey: "sk-ant-supersecret-9999"}
	a, _ := p.Account(context.Background())
	if !strings.HasSuffix(a.Email, "9999") || strings.Contains(a.Email, "supersecret") {
		t.Fatalf("masked key leak: %+v", a)
	}
}

func TestFactoryRegistered(t *testing.T) {
	f, ok := provider.GetFactory("anthropic")
	if !ok {
		t.Fatal("anthropic provider not registered")
	}
	if f.DefaultModel != DefaultModel {
		t.Errorf("default model: %q", f.DefaultModel)
	}
	if _, ok := f.Commands["whoami"]; !ok {
		t.Error("expected whoami subcommand")
	}
}
