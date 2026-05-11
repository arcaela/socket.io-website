package funcs

import (
	"context"
	"fmt"
)

// =============================================================================
// web_search — base function (stub).
//
// Placeholder. A real implementation will query a search provider (DuckDuckGo,
// SearXNG, Brave Search API, etc.) and return ranked snippets. For now it
// always returns an explicit "not implemented" error so the model knows the
// tool exists in the schema but isn't usable yet.
// =============================================================================

type webSearchTool struct{}

func init() { Register(webSearchTool{}) }

func (webSearchTool) Name() string        { return "web_search" }
func (webSearchTool) Kind() Kind          { return KindBase }
func (webSearchTool) DependsOn() []string { return nil }
func (webSearchTool) Description() string {
	return "Search the web (NOT YET IMPLEMENTED — returns an error if called). " +
		"Future: ranked text snippets matching the query."
}

func (webSearchTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query.",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "Maximum number of results (default 10).",
			},
		},
		"required": []string{"query"},
	}
}

func (webSearchTool) Execute(_ context.Context, _ map[string]any, _ Caller) (any, error) {
	return nil, fmt.Errorf("web_search: not implemented yet")
}
