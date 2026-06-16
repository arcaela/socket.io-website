package funcs

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// =============================================================================
// GlobTool — composite: find files/dirs by glob pattern.
// Translates glob → `find` invocation under the bash tool.
// =============================================================================

type GlobTool struct{}

func (GlobTool) Name() string        { return "glob" }
func (GlobTool) Kind() Kind          { return KindComposite }
func (GlobTool) DependsOn() []string { return []string{"bash"} }
func (GlobTool) Description() string {
	return "Find files or directories matching a glob pattern. Supports recursive `**`. " +
		"Returns matched paths, sorted. Backed by `find` via the bash tool."
}

func (GlobTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type": "string",
				"description": "Glob pattern. Examples: `*.go`, `src/**/*.ts`, `internal/*/test*.go`. " +
					"`**` matches any number of intermediate directory levels.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Base directory to search from. Default `.`",
			},
			"type": map[string]any{
				"type":        "string",
				"description": "Filter by entry type: `f` (files, default), `d` (directories), `a` (any).",
				"enum":        []string{"f", "d", "a"},
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "Cap on results returned (default 1000).",
			},
			"include_hidden": map[string]any{
				"type":        "boolean",
				"description": "If true, include hidden entries (names starting with `.`). Default false.",
			},
		},
		"required": []string{"pattern"},
	}
}

type GlobResult struct {
	Pattern   string   `json:"pattern"`
	BasePath  string   `json:"base_path"`
	Paths     []string `json:"paths"`
	Total     int      `json:"total"`
	Truncated bool     `json:"truncated,omitempty"`
}

func (GlobTool) Execute(ctx context.Context, args map[string]any, c Caller) (any, error) {
	pattern, err := argRequiredString(args, "pattern")
	if err != nil {
		return nil, err
	}
	basePath, err := argString(args, "path", ".")
	if err != nil {
		return nil, err
	}
	fileType, err := argString(args, "type", "f")
	if err != nil {
		return nil, err
	}
	if fileType != "f" && fileType != "d" && fileType != "a" {
		return nil, fmt.Errorf(`arg "type" must be one of: f, d, a (got %q)`, fileType)
	}
	maxResults, err := argInt(args, "max_results", 1000)
	if err != nil {
		return nil, err
	}
	if maxResults <= 0 {
		maxResults = 1000
	}
	includeHidden, err := argBool(args, "include_hidden", false)
	if err != nil {
		return nil, err
	}

	cmd := buildFindCommand(pattern, basePath, fileType, maxResults+1, includeHidden)

	out, err := c.Call(ctx, "bash", map[string]any{
		"command":     cmd,
		"description": fmt.Sprintf("Glob %q under %s", pattern, basePath),
	})
	if err != nil {
		return nil, err
	}
	br, ok := out.(BashResult)
	if !ok {
		return nil, fmt.Errorf("bash returned unexpected type %T", out)
	}

	paths := splitNonEmpty(br.Stdout, "\n")
	sort.Strings(paths)

	res := GlobResult{
		Pattern:  pattern,
		BasePath: basePath,
		Total:    len(paths),
	}
	if len(paths) > maxResults {
		res.Paths = paths[:maxResults]
		res.Truncated = true
		res.Total = maxResults
	} else {
		res.Paths = paths
	}
	return res, nil
}

func buildFindCommand(pattern, basePath, fileType string, hardCap int, includeHidden bool) string {
	prefix, name, recursive := splitGlob(pattern)
	searchDir := basePath
	if prefix != "" {
		searchDir = filepath.Join(basePath, prefix)
	}
	if searchDir == "" {
		searchDir = "."
	}

	var b strings.Builder
	b.WriteString("find ")
	b.WriteString(shellQuote(searchDir))
	b.WriteString(" -mindepth 1")
	if !recursive {
		b.WriteString(" -maxdepth 1")
	}
	if fileType != "a" {
		b.WriteString(" -type ")
		b.WriteString(fileType)
	}
	if name != "" {
		b.WriteString(" -name ")
		b.WriteString(shellQuote(name))
	}
	if !includeHidden {
		b.WriteString(" -not -path '*/.*'")
	}
	b.WriteString(fmt.Sprintf(" 2>/dev/null | head -n %d", hardCap))
	return b.String()
}

func splitGlob(pattern string) (prefix, name string, recursive bool) {
	if i := strings.Index(pattern, "**"); i >= 0 {
		recursive = true
		prefix = strings.TrimSuffix(pattern[:i], "/")
		after := strings.TrimPrefix(pattern[i+2:], "/")
		if j := strings.LastIndex(after, "/"); j >= 0 {
			name = after[j+1:]
		} else {
			name = after
		}
		return
	}
	if j := strings.LastIndex(pattern, "/"); j >= 0 {
		prefix = pattern[:j]
		name = pattern[j+1:]
	} else {
		name = pattern
	}
	return
}


func init() { Register(GlobTool{}) }
