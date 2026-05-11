package funcs

import (
	"context"
	"fmt"
)

// =============================================================================
// web_fetch — base function (stub).
//
// Placeholder. A real implementation will GET the URL with a sensible
// timeout, follow redirects, and return the rendered text (HTML stripped or
// markdown-converted). For now it returns an explicit error.
// =============================================================================

type webFetchTool struct{}

func init() { Register(webFetchTool{}) }

func (webFetchTool) Name() string        { return "web_fetch" }
func (webFetchTool) Kind() Kind          { return KindBase }
func (webFetchTool) DependsOn() []string { return nil }
func (webFetchTool) Description() string {
	return "Fetch a URL and return its text content (NOT YET IMPLEMENTED — returns an error if called)."
}

func (webFetchTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "Absolute URL to GET.",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Optional wall-clock cap (default 30).",
			},
		},
		"required": []string{"url"},
	}
}

func (webFetchTool) Execute(_ context.Context, _ map[string]any, _ Caller) (any, error) {
	return nil, fmt.Errorf("web_fetch: not implemented yet")
}
