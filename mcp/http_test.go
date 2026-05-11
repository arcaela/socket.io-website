package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// stubHTTPServer returns an httptest server that mirrors a tiny MCP server
// over HTTP. The `recorder` counts how many requests came in (useful to
// confirm initialize + initialized + tools/list paths).
func stubHTTPServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var msg map[string]any
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Errorf("bad JSON: %v", err)
		}

		// Auth header was forwarded?
		if want := "Bearer hush"; r.Header.Get("Authorization") != want && r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Authorization: %q", r.Header.Get("Authorization"))
		}

		method, _ := msg["method"].(string)
		idRaw, hasID := msg["id"]

		// Notifications (no id) — accept and return 202.
		if !hasID {
			w.WriteHeader(202)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "initialize":
			respond(w, idRaw, map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]any{"name": "stub", "version": "0"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			})
		case "tools/list":
			respond(w, idRaw, map[string]any{
				"tools": []map[string]any{
					{"name": "echo", "description": "echoes back",
						"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
							"msg": map[string]any{"type": "string"}}}},
				},
			})
		case "tools/call":
			args, _ := msg["params"].(map[string]any)
			inner, _ := args["arguments"].(map[string]any)
			text := "echo: "
			if v, ok := inner["msg"].(string); ok {
				text += v
			}
			respond(w, idRaw, map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
				"isError": false,
			})
		default:
			respondError(w, idRaw, -32601, "unknown method "+method)
		}
	}))
	return srv, &calls
}

func respond(w http.ResponseWriter, id any, result map[string]any) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func respondError(w http.ResponseWriter, id any, code int, msg string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	})
}

// =============================================================================
// HTTP transport tests
// =============================================================================

func TestHTTPClient_InitializeAndListTools(t *testing.T) {
	srv, calls := stubHTTPServer(t)
	defer srv.Close()

	c, err := Start(context.Background(), "stub", ServerConfig{
		URL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if c.transport != "http" {
		t.Fatalf("expected http transport, got %q", c.transport)
	}

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	// initialize + initialized(notify) + tools/list = 3 requests.
	if got := calls.Load(); got < 3 {
		t.Errorf("expected ≥3 HTTP calls, got %d", got)
	}
}

func TestHTTPClient_CallTool(t *testing.T) {
	srv, _ := stubHTTPServer(t)
	defer srv.Close()

	c, err := Start(context.Background(), "stub", ServerConfig{
		URL: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	res, err := c.CallTool(context.Background(), "echo", map[string]any{"msg": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "echo: hi" {
		t.Errorf("bad result: %+v", res)
	}
}

func TestHTTPClient_ForwardsHeaders(t *testing.T) {
	gotAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || r.Method == "" {
		}
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var msg map[string]any
		_ = json.Unmarshal(body, &msg)
		if _, hasID := msg["id"]; !hasID {
			w.WriteHeader(202)
			return
		}
		respond(w, msg["id"], map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
		})
	}))
	defer srv.Close()

	c, err := Start(context.Background(), "stub", ServerConfig{
		URL:     srv.URL,
		Headers: map[string]string{"Authorization": "Bearer hush"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if gotAuth != "Bearer hush" {
		t.Fatalf("Authorization header not forwarded; got %q", gotAuth)
	}
}

func TestHTTPClient_4xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	_, err := Start(context.Background(), "stub", ServerConfig{URL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 surfaced, got %v", err)
	}
}

func TestHTTPClient_JSONRPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var msg map[string]any
		_ = json.Unmarshal(body, &msg)
		if _, hasID := msg["id"]; !hasID {
			w.WriteHeader(202)
			return
		}
		method := msg["method"].(string)
		if method == "initialize" {
			respond(w, msg["id"], map[string]any{"protocolVersion": "x"})
			return
		}
		respondError(w, msg["id"], -32603, "boom")
	}))
	defer srv.Close()

	c, err := Start(context.Background(), "stub", ServerConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.ListTools(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected JSON-RPC error surfaced, got %v", err)
	}
}

func TestStart_RequiresCommandOrURL(t *testing.T) {
	_, err := Start(context.Background(), "x", ServerConfig{})
	if err == nil || !strings.Contains(err.Error(), "command") {
		t.Fatalf("expected error mentioning command/url, got %v", err)
	}
}
