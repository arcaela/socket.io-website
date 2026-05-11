// Package mcp speaks JSON-RPC 2.0 to MCP servers over stdio (the most
// common MCP transport). It discovers each server's tools at startup and
// exposes them as funcs.Tool instances so the agent can call them with the
// same machinery as our built-in tools.
//
// Why no third-party SDK: MCP is JSON-RPC 2.0 over stdio. About ~250 LOC
// gets us the subset we need (initialize, tools/list, tools/call, basic
// error mapping). Adding an SDK dependency for this is not worth the
// supply-chain surface.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arcaela/mini-cli/config"
	"github.com/arcaela/mini-cli/funcs"
)

// =============================================================================
// Configuration (~/.mini/mcp.json)
// =============================================================================
//
// {
//   "servers": {
//     "filesystem": {
//       "command": "npx",
//       "args": ["@modelcontextprotocol/server-filesystem", "/home/user"],
//       "env":  {"FOO": "bar"}
//     },
//     "github": {
//       "command": "npx",
//       "args": ["@modelcontextprotocol/server-github"],
//       "env":  {"GITHUB_TOKEN": "ghp_..."}
//     }
//   }
// }

type ServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type Config struct {
	Servers map[string]ServerConfig `json:"servers"`
}

// ConfigPath returns ~/.mini/mcp.json (overridable via MINI_MCP_CONFIG).
func ConfigPath() string {
	if v := os.Getenv("MINI_MCP_CONFIG"); v != "" {
		return v
	}
	return filepath.Join(config.ConfigDir(), "mcp.json")
}

// LoadConfig reads ~/.mini/mcp.json. Returns nil if it does not exist
// (zero servers — equivalent to MCP being off).
func LoadConfig() (*Config, error) {
	path := ConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{Servers: map[string]ServerConfig{}}, nil
		}
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Servers == nil {
		cfg.Servers = map[string]ServerConfig{}
	}
	return &cfg, nil
}

// =============================================================================
// JSON-RPC 2.0 wire types
// =============================================================================

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// =============================================================================
// MCP types we care about
// =============================================================================

type initializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	ServerInfo      map[string]any `json:"serverInfo"`
	Capabilities    map[string]any `json:"capabilities"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type listToolsResult struct {
	Tools []Tool `json:"tools"`
}

type CallToolResult struct {
	Content []ContentPart  `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type ContentPart struct {
	Type string `json:"type"`           // "text", "image", "resource"
	Text string `json:"text,omitempty"` // populated for type=text
}

// =============================================================================
// Client: one process per MCP server.
// =============================================================================

type Client struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	nextID  int64

	mu      sync.Mutex
	pending map[int]chan rpcResponse
	closed  bool
}

// Start launches the MCP server subprocess and performs the JSON-RPC
// handshake (`initialize` + `initialized` notification).
func Start(ctx context.Context, name string, srv ServerConfig) (*Client, error) {
	if srv.Command == "" {
		return nil, fmt.Errorf("server %q: command is empty", name)
	}
	cmd := exec.Command(srv.Command, srv.Args...)
	cmd.Env = os.Environ()
	for k, v := range srv.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Pipe server stderr through ours so misconfigurations are visible.
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q (%s): %w", name, srv.Command, err)
	}

	c := &Client{
		name:    name,
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
		pending: map[int]chan rpcResponse{},
	}
	go c.readerLoop()

	// 1. Send `initialize` and wait for the result.
	initParams, _ := json.Marshal(map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]any{"name": "mini-cli", "version": "0.1.0"},
		"capabilities":    map[string]any{},
	})
	if _, err := c.call(ctx, "initialize", initParams, 10*time.Second); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize %q: %w", name, err)
	}

	// 2. Send the `initialized` notification (no response expected).
	if err := c.notify("notifications/initialized", nil); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialized notify %q: %w", name, err)
	}
	return c, nil
}

// ListTools queries the server's tools/list. Cached by caller if needed.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	raw, err := c.call(ctx, "tools/list", nil, 10*time.Second)
	if err != nil {
		return nil, err
	}
	var lr listToolsResult
	if err := json.Unmarshal(raw, &lr); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	return lr.Tools, nil
}

// CallTool invokes `tools/call` with the given name + arguments.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (CallToolResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	params, _ := json.Marshal(map[string]any{
		"name":      name,
		"arguments": args,
	})
	raw, err := c.call(ctx, "tools/call", params, 5*time.Minute)
	if err != nil {
		return CallToolResult{}, err
	}
	var res CallToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return CallToolResult{}, fmt.Errorf("decode tools/call: %w", err)
	}
	return res, nil
}

// Close terminates the subprocess. Idempotent.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
	return nil
}

// Name returns the configured server name (used as the prefix for tool names).
func (c *Client) Name() string { return c.name }

// =============================================================================
// Wire mechanics — line-delimited JSON over stdio.
// =============================================================================

func (c *Client) call(ctx context.Context, method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	id := int(atomic.AddInt64(&c.nextID, 1))
	ch := make(chan rpcResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	data, _ := json.Marshal(req)
	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		return nil, err
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-deadline.C:
		return nil, fmt.Errorf("mcp %s: %s timed out after %v", c.name, method, timeout)
	}
}

func (c *Client) notify(method string, params json.RawMessage) error {
	n := rpcNotification{JSONRPC: "2.0", Method: method, Params: params}
	data, _ := json.Marshal(n)
	_, err := c.stdin.Write(append(data, '\n'))
	return err
}

func (c *Client) readerLoop() {
	for {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			c.mu.Lock()
			for _, ch := range c.pending {
				close(ch)
			}
			c.pending = map[int]chan rpcResponse{}
			c.mu.Unlock()
			return
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue // could be a server notification; ignore for v1
		}
		if resp.ID == 0 {
			continue // notification without id; we don't act on these yet
		}
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		c.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
}

// =============================================================================
// Adapter: wrap an MCP tool as a funcs.Tool so the agent can call it.
// =============================================================================

// remoteTool implements funcs.Tool by forwarding Execute to the underlying
// MCP client. Tool names are prefixed with `<server>__` to avoid collisions
// between servers and with built-in tools.
type remoteTool struct {
	client      *Client
	remoteName  string // name as the server knows it
	exposedName string // prefixed name as the agent sees it
	description string
	schema      map[string]any
}

func (t *remoteTool) Name() string             { return t.exposedName }
func (t *remoteTool) Description() string      { return t.description }
func (t *remoteTool) Kind() funcs.Kind         { return funcs.KindBase }
func (t *remoteTool) DependsOn() []string      { return nil }
func (t *remoteTool) Schema() map[string]any   { return t.schema }
// RiskOf — without server-side hints we treat MCP tools as High by default:
// they touch external systems the user may care about (filesystem, GitHub).
func (t *remoteTool) RiskOf(_ map[string]any) funcs.Risk { return funcs.RiskHigh }

func (t *remoteTool) Execute(ctx context.Context, args map[string]any, _ funcs.Caller) (any, error) {
	res, err := t.client.CallTool(ctx, t.remoteName, args)
	if err != nil {
		return nil, err
	}
	// Flatten text content into a single string for the agent. The richer
	// resource/image variants are surfaced under `content[]` for callers
	// that want structured access.
	var text string
	for _, c := range res.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	return map[string]any{
		"text":     text,
		"content":  res.Content,
		"is_error": res.IsError,
	}, nil
}

// =============================================================================
// RegisterAll launches every configured MCP server and registers their
// tools into `funcs.BuiltIn()`. Failures of individual servers are logged
// and do not block startup — the agent runs with whichever ones worked.
//
// Returns the live clients so the caller can Close() them at shutdown.
// =============================================================================

func RegisterAll(ctx context.Context) ([]*Client, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	if len(cfg.Servers) == 0 {
		return nil, nil
	}

	clients := make([]*Client, 0, len(cfg.Servers))
	for name, srv := range cfg.Servers {
		c, err := Start(ctx, name, srv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[mcp] server %q failed to start: %v\n", name, err)
			continue
		}
		tools, err := c.ListTools(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[mcp] server %q tools/list failed: %v\n", name, err)
			_ = c.Close()
			continue
		}
		registered := 0
		for _, t := range tools {
			rt := &remoteTool{
				client:      c,
				remoteName:  t.Name,
				exposedName: name + "__" + t.Name,
				description: fmt.Sprintf("[mcp/%s] %s", name, t.Description),
				schema:      t.InputSchema,
			}
			if err := funcs.BuiltIn().Register(rt); err != nil {
				fmt.Fprintf(os.Stderr, "[mcp] register %s: %v\n", rt.exposedName, err)
				continue
			}
			registered++
		}
		if os.Getenv("MINI_QUIET") != "1" {
			fmt.Fprintf(os.Stderr, "[mcp] %s: %d tool(s) registered\n", name, registered)
		}
		clients = append(clients, c)
	}
	return clients, nil
}
