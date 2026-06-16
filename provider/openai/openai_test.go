package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arcaela/mini-cli/provider"
)

func TestGenerateImage_DecodesBase64(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/images/generations") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		b64 := base64.StdEncoding.EncodeToString([]byte("IMGBYTES"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"b64_json": b64}},
		})
	}))
	defer srv.Close()

	p := &Provider{APIKey: "k", BaseURL: srv.URL, HTTP: srv.Client()}
	imgs, err := p.GenerateImage(context.Background(), "a cat", provider.ImageGenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 || imgs[0].MimeType != "image/png" || string(imgs[0].Data) != "IMGBYTES" {
		t.Fatalf("bad image: %+v", imgs)
	}
}

func TestGenerateImage_SurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"bad prompt"}`))
	}))
	defer srv.Close()

	p := &Provider{APIKey: "k", BaseURL: srv.URL, HTTP: srv.Client()}
	if _, err := p.GenerateImage(context.Background(), "x", provider.ImageGenOptions{}); err == nil {
		t.Fatal("expected API error to surface")
	}
}

// =============================================================================
// Message translation
// =============================================================================

func TestMessagesToChat_BasicRoles(t *testing.T) {
	in := []provider.Message{
		{Role: provider.RoleSystem, Text: "be brief"},
		{Role: provider.RoleUser, Text: "hi"},
		{Role: provider.RoleAssistant, Text: "hello"},
	}
	out := messagesToChat(in)
	if len(out) != 3 {
		t.Fatalf("want 3 msgs, got %d", len(out))
	}
	want := []struct{ role, content string }{
		{"system", "be brief"},
		{"user", "hi"},
		{"assistant", "hello"},
	}
	for i, w := range want {
		if out[i].Role != w.role || out[i].Content != w.content {
			t.Errorf("msg[%d] = (%q, %q); want (%q, %q)", i, out[i].Role, out[i].Content, w.role, w.content)
		}
	}
}

func TestMessagesToChat_ToolCall(t *testing.T) {
	out := messagesToChat([]provider.Message{
		{Role: provider.RoleAssistant, ToolCall: &provider.ToolCall{
			ID:   "call_1",
			Name: "bash",
			Args: map[string]any{"command": "ls"},
		}},
	})
	if len(out) != 1 || out[0].Role != "assistant" {
		t.Fatalf("expected single assistant msg, got %+v", out)
	}
	if len(out[0].ToolCalls) != 1 {
		t.Fatalf("expected one tool_call, got %d", len(out[0].ToolCalls))
	}
	tc := out[0].ToolCalls[0]
	if tc.ID != "call_1" || tc.Type != "function" || tc.Function.Name != "bash" {
		t.Errorf("bad tool call: %+v", tc)
	}
	if !strings.Contains(tc.Function.Arguments, `"command":"ls"`) {
		t.Errorf("args not serialised: %s", tc.Function.Arguments)
	}
}

func TestMessagesToChat_ToolResult(t *testing.T) {
	out := messagesToChat([]provider.Message{
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{
			CallID: "call_1",
			Name:   "bash",
			Result: map[string]any{"stdout": "hi"},
		}},
	})
	if len(out) != 1 || out[0].Role != "tool" {
		t.Fatalf("expected tool role, got %+v", out)
	}
	if out[0].ToolCallID != "call_1" {
		t.Errorf("missing tool_call_id: %+v", out[0])
	}
	if c, _ := out[0].Content.(string); !strings.Contains(c, `"stdout":"hi"`) {
		t.Errorf("result body not serialised: %v", out[0].Content)
	}
}

func TestMessagesToChat_ToolResultError(t *testing.T) {
	out := messagesToChat([]provider.Message{
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{Name: "bash", Error: "perm"}},
	})
	if c, _ := out[0].Content.(string); !strings.HasPrefix(c, "error: ") {
		t.Fatalf("error not surfaced: %v", out[0].Content)
	}
}

func TestMessagesToChat_ToolResultWithImageAddsUserTurn(t *testing.T) {
	out := messagesToChat([]provider.Message{
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{
			CallID: "call_1", Name: "read", Result: map[string]any{"kind": "image"},
			Images: []provider.Image{{MimeType: "image/png", Data: []byte{1, 2, 3}}},
		}},
	})
	if len(out) != 2 {
		t.Fatalf("expected tool message + user image message, got %d", len(out))
	}
	if out[1].Role != "user" {
		t.Fatalf("image must ride in a user turn, got %q", out[1].Role)
	}
	parts, ok := out[1].Content.([]map[string]any)
	if !ok || len(parts) != 1 || parts[0]["type"] != "image_url" {
		t.Fatalf("bad image parts: %#v", out[1].Content)
	}
	url, _ := parts[0]["image_url"].(map[string]any)
	if url["url"] != "data:image/png;base64,AQID" {
		t.Fatalf("bad data url: %#v", url)
	}
}

func TestToolsToChat(t *testing.T) {
	decls := []provider.ToolDecl{
		{Name: "bash", Description: "run shell", Schema: map[string]any{"type": "object"}},
	}
	out := toolsToChat(decls)
	if len(out) != 1 || out[0].Type != "function" || out[0].Function.Name != "bash" {
		t.Fatalf("bad tools array: %+v", out)
	}
}

// =============================================================================
// Streaming end-to-end against httptest
// =============================================================================

// sseStream writes OpenAI-style chunks. Each entry produces one
// `data: <json>\n\n` line; the test helper appends the `[DONE]` sentinel.
func sseStream(t *testing.T, w http.ResponseWriter, lines []string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	for _, l := range lines {
		fmt.Fprintf(w, "data: %s\n\n", l)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestGenerate_TextStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("bad auth header: %q", r.Header.Get("Authorization"))
		}
		var body chatRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Stream {
			t.Errorf("expected stream=true")
		}
		sseStream(t, w, []string{
			`{"choices":[{"delta":{"content":"Hello "}}]}`,
			`{"choices":[{"delta":{"content":"world"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`,
		})
	}))
	defer srv.Close()

	p := &Provider{APIKey: "test-key", BaseURL: srv.URL, HTTP: srv.Client()}
	events, err := p.Generate(context.Background(), provider.GenerateRequest{
		Model:    "gpt-5",
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
	if text.String() != "Hello world" {
		t.Errorf("text reassembly: %q", text.String())
	}
	if usage == nil || usage.TotalTokens != 15 {
		t.Errorf("usage not threaded: %+v", usage)
	}
}

func TestGenerate_ToolCallStream(t *testing.T) {
	// OpenAI streams tool_call arguments byte by byte; the provider must
	// buffer them by `index` and emit a single complete EventToolCallRequest.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseStream(t, w, []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","function":{"name":"bash","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		})
	}))
	defer srv.Close()

	p := &Provider{APIKey: "k", BaseURL: srv.URL, HTTP: srv.Client()}
	events, err := p.Generate(context.Background(), provider.GenerateRequest{Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}

	var calls []provider.ToolCall
	for ev := range events {
		if ev.Kind == provider.EventToolCallRequest {
			calls = append(calls, *ev.ToolCall)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "bash" || calls[0].ID != "call_abc" {
		t.Errorf("bad call: %+v", calls[0])
	}
	if calls[0].Args["command"] != "ls" {
		t.Errorf("args not reassembled: %+v", calls[0].Args)
	}
}

func TestGenerate_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()

	p := &Provider{APIKey: "k", BaseURL: srv.URL, HTTP: srv.Client()}
	_, err := p.Generate(context.Background(), provider.GenerateRequest{Model: "x"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 surfaced, got %v", err)
	}
}

// =============================================================================
// Factory / registration
// =============================================================================

func TestNew_RequiresAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	if _, err := New(context.Background()); err == nil {
		t.Fatal("expected error when OPENAI_API_KEY missing")
	}
}

func TestNew_UsesOverrideBaseURL(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", "https://example.test/v1/")
	p, err := New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.BaseURL != "https://example.test/v1" { // trailing / trimmed
		t.Fatalf("base url: %q", p.BaseURL)
	}
}

func TestAccount_MasksKey(t *testing.T) {
	p := &Provider{APIKey: "sk-supersecret-1234"}
	a, _ := p.Account(context.Background())
	if !strings.HasSuffix(a.Email, "1234") {
		t.Fatalf("masked key not surfaced: %+v", a)
	}
	if strings.Contains(a.Email, "supersecret") {
		t.Fatalf("full key leaked: %+v", a)
	}
}

func TestFactoryRegistered(t *testing.T) {
	f, ok := provider.GetFactory("openai")
	if !ok {
		t.Fatal("openai provider not registered")
	}
	if f.DefaultModel != DefaultModel {
		t.Errorf("default model: %q", f.DefaultModel)
	}
	if _, ok := f.Commands["whoami"]; !ok {
		t.Error("expected 'whoami' subcommand")
	}
}
