package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// Config loading
// =============================================================================

func TestLoadConfig_Missing(t *testing.T) {
	t.Setenv("MINI_MCP_CONFIG", filepath.Join(t.TempDir(), "does-not-exist.json"))
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Servers) != 0 {
		t.Fatalf("missing file should give empty servers, got %d", len(c.Servers))
	}
}

func TestLoadConfig_Parses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	body := `{"servers":{"fs":{"command":"echo","args":["x"],"env":{"K":"V"}}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINI_MCP_CONFIG", path)
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	srv, ok := c.Servers["fs"]
	if !ok {
		t.Fatalf("server 'fs' missing: %+v", c)
	}
	if srv.Command != "echo" || len(srv.Args) != 1 || srv.Args[0] != "x" {
		t.Errorf("bad server config: %+v", srv)
	}
	if srv.Env["K"] != "V" {
		t.Errorf("env not parsed: %+v", srv.Env)
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	_ = os.WriteFile(path, []byte("not json"), 0o600)
	t.Setenv("MINI_MCP_CONFIG", path)
	_, err := LoadConfig()
	if err == nil {
		t.Fatal("expected parse error")
	}
}

// =============================================================================
// Live JSON-RPC over a real subprocess: we use a Python one-liner as a
// stub MCP server. Skipped if python3 isn't available.
// =============================================================================

const stubServerPy = `
import sys, json
for line in sys.stdin:
    try:
        msg = json.loads(line)
    except Exception:
        continue
    # Notifications have no id.
    if msg.get("id") is None:
        continue
    method = msg.get("method")
    rid = msg["id"]
    if method == "initialize":
        out = {"jsonrpc":"2.0","id":rid,"result":{
            "protocolVersion":"2024-11-05",
            "serverInfo":{"name":"stub","version":"0"},
            "capabilities":{"tools":{}}}}
    elif method == "tools/list":
        out = {"jsonrpc":"2.0","id":rid,"result":{"tools":[
            {"name":"echo","description":"return what we sent",
             "inputSchema":{"type":"object","properties":{"msg":{"type":"string"}}}}]}}
    elif method == "tools/call":
        msg_arg = msg.get("params",{}).get("arguments",{}).get("msg","")
        out = {"jsonrpc":"2.0","id":rid,"result":{
            "content":[{"type":"text","text":"echo: " + msg_arg}],
            "isError": False}}
    else:
        out = {"jsonrpc":"2.0","id":rid,"error":{"code":-32601,"message":"unknown method " + str(method)}}
    sys.stdout.write(json.dumps(out) + "\n")
    sys.stdout.flush()
`

func pythonAvailable(t *testing.T) string {
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	t.Skip("python not available")
	return ""
}

func TestClient_InitializeAndListTools(t *testing.T) {
	py := pythonAvailable(t)
	c, err := Start(context.Background(), "stub", ServerConfig{
		Command: py,
		Args:    []string{"-u", "-c", stubServerPy},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
}

func TestClient_CallTool(t *testing.T) {
	py := pythonAvailable(t)
	c, err := Start(context.Background(), "stub", ServerConfig{
		Command: py,
		Args:    []string{"-u", "-c", stubServerPy},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	res, err := c.CallTool(context.Background(), "echo", map[string]any{"msg": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("bad content: %+v", res.Content)
	}
	if res.Content[0].Text != "echo: hello" {
		t.Errorf("body roundtrip wrong: %q", res.Content[0].Text)
	}
}

func TestClient_UnknownMethod(t *testing.T) {
	py := pythonAvailable(t)
	c, err := Start(context.Background(), "stub", ServerConfig{
		Command: py,
		Args:    []string{"-u", "-c", stubServerPy},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.call(context.Background(), "bogus", nil, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "unknown method") {
		t.Fatalf("expected unknown-method error, got %v", err)
	}
}

// JSON roundtrip sanity (no subprocess needed).
func TestRPC_Marshalling(t *testing.T) {
	req := rpcRequest{JSONRPC: "2.0", ID: 7, Method: "tools/call"}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var back rpcRequest
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Method != "tools/call" || back.ID != 7 {
		t.Fatalf("roundtrip broken: %+v", back)
	}
}
