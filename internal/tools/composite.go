package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Composites never call other tools directly — they go through Caller. The
// registry is the single source of truth, which is what lets us audit /
// instrument / swap any base tool without touching composites.

// =============================================================================
// ReadTool — composite: read a line range of a file with line numbers.
// Built on top of `bash` (uses cat / awk under the hood).
//
// Why composite + bash instead of os.ReadFile? The agent already has bash
// available; routing reads through it gives a single audit point and lets us
// reuse bash's timeout / error reporting. Performance is fine for any file
// the agent would reasonably read in a turn.
//
// Output is line-numbered so the agent always knows the absolute position it
// is looking at — important when followed by a `write` call that needs an
// anchor.
// =============================================================================

type ReadTool struct{}

func (ReadTool) Name() string         { return "read" }
func (ReadTool) Kind() Kind           { return KindComposite }
func (ReadTool) DependsOn() []string  { return []string{"bash"} }
func (ReadTool) Description() string {
	return "Read a range of lines from a file. Returns the content with each line prefixed by its 1-indexed line number, " +
		"so the agent has explicit positional context. Backed by `bash` (cat/awk). " +
		"If `start` and `end` are omitted, reads the first 2000 lines."
}

func (ReadTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file.",
			},
			"start": map[string]any{
				"type":        "integer",
				"description": "First line to return, 1-indexed (default 1).",
			},
			"end": map[string]any{
				"type":        "integer",
				"description": "Last line to return, 1-indexed inclusive. 0 or omitted means \"start + max_lines - 1\" (capped to EOF).",
			},
			"max_lines": map[string]any{
				"type":        "integer",
				"description": "Cap on the number of lines returned when `end` is not specified (default 2000).",
			},
			"raw": map[string]any{
				"type": "boolean",
				"description": "If true, return content WITHOUT line-number prefixes. Use only when you need byte-exact content (e.g. to feed back into `write`'s old_block). Agents should normally leave this false to keep positional context.",
			},
		},
		"required": []string{"path"},
	}
}

type ReadResult struct {
	Path       string `json:"path"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	TotalLines int    `json:"total_lines_in_file"`
	LineCount  int    `json:"line_count"`
	Truncated  bool   `json:"truncated,omitempty"`
	Content    string `json:"content"`
}

func (ReadTool) Execute(ctx context.Context, args map[string]any, c Caller) (any, error) {
	path, err := argRequiredString(args, "path")
	if err != nil {
		return nil, err
	}
	start, err := argInt(args, "start", 1)
	if err != nil {
		return nil, err
	}
	if start < 1 {
		return nil, fmt.Errorf(`arg "start" must be >= 1`)
	}
	end, err := argInt(args, "end", 0)
	if err != nil {
		return nil, err
	}
	maxLines, err := argInt(args, "max_lines", 2000)
	if err != nil {
		return nil, err
	}
	if maxLines <= 0 {
		maxLines = 2000
	}
	raw, err := argBool(args, "raw", false)
	if err != nil {
		return nil, err
	}

	// One bash invocation: awk emits lines in the requested range (numbered or
	// not depending on `raw`) and writes the file's total line count to stderr
	// so we can detect truncation.
	endExpr := "0"
	if end > 0 {
		endExpr = strconv.Itoa(end)
	}
	printf := `printf "%6d→%s\n", NR, $0`
	if raw {
		printf = `print $0`
	}
	awkScript := fmt.Sprintf(`
NR>=%d && (%s==0 || NR<=%s) && (count<%d) { %s; count++ }
END { printf "TOTAL=%%d", NR > "/dev/stderr" }
`, start, endExpr, endExpr, maxLines, printf)

	cmd := fmt.Sprintf(`awk %s %s`, shellQuote(awkScript), shellQuote(path))

	out, err := c.Call(ctx, "bash", map[string]any{
		"command":     cmd,
		"description": fmt.Sprintf("Read lines %d–%s of %s", start, endStr(end), path),
	})
	if err != nil {
		return nil, err
	}
	br, ok := out.(BashResult)
	if !ok {
		return nil, fmt.Errorf("bash returned unexpected type %T", out)
	}
	if br.ExitCode != 0 && br.Stdout == "" {
		return nil, fmt.Errorf("read failed: %s", strings.TrimSpace(br.Stderr))
	}

	total := parseTotalFromStderr(br.Stderr)
	content := strings.TrimRight(br.Stdout, "\n")
	lineCount := 0
	if content != "" {
		lineCount = strings.Count(content, "\n") + 1
	}

	effectiveEnd := end
	if effectiveEnd <= 0 {
		effectiveEnd = start + lineCount - 1
	}

	res := ReadResult{
		Path:       path,
		Start:      start,
		End:        effectiveEnd,
		TotalLines: total,
		LineCount:  lineCount,
		Content:    content,
	}
	// Truncation is set when we capped at max_lines and there were more lines
	// available beyond what we read.
	if end <= 0 && lineCount == maxLines && total > start+lineCount-1 {
		res.Truncated = true
	}
	return res, nil
}

func endStr(end int) string {
	if end <= 0 {
		return "EOF"
	}
	return strconv.Itoa(end)
}

func parseTotalFromStderr(stderr string) int {
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "TOTAL=") {
			n, err := strconv.Atoi(strings.TrimPrefix(line, "TOTAL="))
			if err == nil {
				return n
			}
		}
	}
	return 0
}

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

// =============================================================================
// TaskTool — composite: spawn a sub-agent (this same `mini` binary, in
// one-shot chat mode) to handle a delegated prompt.
//
// Foreground: blocks while the sub-agent runs to completion; the parent
// receives the sub-agent's full transcript (model text + tool execution
// markers) in the bash result's stdout.
//
// Background: spawns the sub-agent detached, returns immediately with a
// log file path. The parent agent can read the file later (with `read`) to
// inspect or wait for completion via the exit marker.
//
// Why this is useful: lets an agent fan out long-running investigations
// (search a large repo, run a test suite, etc.) without blocking its own
// loop, and combine results later.
// =============================================================================

type TaskTool struct{}

func (TaskTool) Name() string         { return "task" }
func (TaskTool) Kind() Kind           { return KindComposite }
func (TaskTool) DependsOn() []string  { return []string{"bash"} }
func (TaskTool) Description() string {
	return "Spawn a sub-agent (a fresh one-shot `mini chat`) with the given prompt. " +
		"In foreground mode, blocks until the sub-agent finishes and returns its full transcript. " +
		"In background mode, returns immediately with a `log_file` you can read later to follow the sub-agent's progress."
}

func (TaskTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "The prompt to send to the sub-agent. It will run in a fresh conversation (no shared history with the caller).",
			},
			"background": map[string]any{
				"type":        "boolean",
				"description": "If true, the sub-agent runs detached and the call returns a `log_file` immediately. Default false (block and return transcript).",
			},
		},
		"required": []string{"prompt"},
	}
}

func (TaskTool) Execute(ctx context.Context, args map[string]any, c Caller) (any, error) {
	prompt, err := argRequiredString(args, "prompt")
	if err != nil {
		return nil, err
	}
	background, err := argBool(args, "background", false)
	if err != nil {
		return nil, err
	}

	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "mini" // fall back to PATH
	}

	command := fmt.Sprintf("%s chat %s", shellQuote(exe), shellQuote(prompt))
	desc := fmt.Sprintf("Sub-agent task: %s", truncateForDesc(prompt, 100))

	bashArgs := map[string]any{
		"command":     command,
		"description": desc,
		"background":  background,
	}
	// Sub-agents can run for many minutes (multiple model turns + their own
	// tool calls). Foreground tasks must NOT inherit the bash 120s default.
	// Background ignores timeout entirely so we don't set it there (would
	// trip the mutual-exclusion check).
	if !background {
		bashArgs["timeout_seconds"] = 0
	}
	return c.Call(ctx, "bash", bashArgs)
}

// =============================================================================
// MemoryTool — third-level composite: persistent, agent-managed memory.
//
// Storage: ~/.mini/memory/<name>.md (one file per memory entry).
// At every `mini` startup, the contents of every file under that directory
// are concatenated unmodified and appended to the system prompt — for both
// the main agent and any sub-agents it spawns via `task`. This is how the
// agent retains information across conversations.
//
// This tool is "third level" because it builds on TWO other tools:
//   - read  (composite, raw mode) to fetch the current contents
//   - write (base) to atomically replace the file with new content
//
// Modes:
//   set    (default) — overwrite the file completely with `content`
//   append          — read current, append `content` after a blank line, write back
// =============================================================================

type MemoryTool struct{}

func (MemoryTool) Name() string         { return "memory" }
func (MemoryTool) Kind() Kind           { return KindComposite }
func (MemoryTool) DependsOn() []string  { return []string{"read", "write"} }
func (MemoryTool) Description() string {
	return "Save persistent information to the agent's long-term memory. " +
		"Stored under ~/.mini/memory/<name>.md and auto-loaded into the system prompt of every future `mini` session (including sub-agents). " +
		"Use this for stable facts about the user, project conventions, or anything you want to remember across conversations."
}

func (MemoryTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Identifier for this memory entry (alphanumeric, dash, underscore only). E.g. \"user_preferences\", \"project_conventions\".",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The text to remember. Will be loaded into future system prompts unmodified, so write it as natural-language facts.",
			},
			"mode": map[string]any{
				"type":        "string",
				"description": "`set` (default) overwrites the entire entry. `append` adds to the existing entry separated by a blank line.",
				"enum":        []string{"set", "append"},
			},
		},
		"required": []string{"name", "content"},
	}
}

type MemoryResult struct {
	Path        string `json:"path"`
	Mode        string `json:"mode"`
	BytesBefore int    `json:"bytes_before"`
	BytesAfter  int    `json:"bytes_after"`
	Created     bool   `json:"created,omitempty"`
}

var memoryNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func (MemoryTool) Execute(ctx context.Context, args map[string]any, c Caller) (any, error) {
	name, err := argRequiredString(args, "name")
	if err != nil {
		return nil, err
	}
	if !memoryNameRE.MatchString(name) {
		return nil, fmt.Errorf(
			`memory name %q is invalid: use alphanumeric, dash, underscore only (max 64 chars, must start with letter or digit)`,
			name)
	}
	content, err := argRequiredString(args, "content")
	if err != nil {
		return nil, err
	}
	mode, err := argString(args, "mode", "set")
	if err != nil {
		return nil, err
	}
	if mode != "set" && mode != "append" {
		return nil, fmt.Errorf(`mode must be "set" or "append" (got %q)`, mode)
	}

	if err := os.MkdirAll(memoryDirPath(), 0o700); err != nil {
		return nil, fmt.Errorf("create memory dir: %w", err)
	}
	path := filepath.Join(memoryDirPath(), name+".md")

	// Step 1: read current contents (raw, no line numbers) if the file exists.
	existing := ""
	created := true
	if _, err := os.Stat(path); err == nil {
		out, err := c.Call(ctx, "read", map[string]any{
			"path":      path,
			"raw":       true,
			"max_lines": 1_000_000, // memory files are tiny; cap is just a safety
		})
		if err != nil {
			return nil, fmt.Errorf("read existing memory: %w", err)
		}
		rr, ok := out.(ReadResult)
		if !ok {
			return nil, fmt.Errorf("read returned unexpected type %T", out)
		}
		existing = rr.Content
		created = false
	}

	// Step 2: compose the new content based on mode.
	var newContent string
	switch mode {
	case "set":
		newContent = content
	case "append":
		if existing == "" {
			newContent = content
		} else {
			newContent = strings.TrimRight(existing, "\n") + "\n\n" + content
		}
	}

	// Step 3: hand off to the write tool. When the file already existed we
	// pass its full content as old_block (anchor). When it's new we pass an
	// empty old_block, which write interprets as "create file".
	writeArgs := map[string]any{
		"path":      path,
		"old_block": existing,
		"new_block": newContent,
	}
	out, err := c.Call(ctx, "write", writeArgs)
	if err != nil {
		return nil, err
	}
	wr, ok := out.(WriteResult)
	if !ok {
		return nil, fmt.Errorf("write returned unexpected type %T", out)
	}

	return MemoryResult{
		Path:        wr.Path,
		Mode:        mode,
		BytesBefore: wr.BytesBefore,
		BytesAfter:  wr.BytesAfter,
		Created:     created,
	}, nil
}

// memoryDirPath returns ~/.mini/memory by default. Override with MINI_MEMORY_DIR
// (mainly for tests).
func memoryDirPath() string {
	if v := os.Getenv("MINI_MEMORY_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "mini-memory")
	}
	return filepath.Join(home, ".mini", "memory")
}

// LoadMemoryContent walks the memory directory, reads every *.md file in
// alphabetical order, and returns the concatenation of their contents. Used
// by the CLI at startup to seed the agent's system prompt. Files are joined
// with a single blank line between them; nothing else is added (no headers,
// no per-file labels, just the raw text).
func LoadMemoryContent() (string, error) {
	dir := memoryDirPath()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	names := []string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var b strings.Builder
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return "", err
		}
		text := strings.TrimRight(string(data), "\n")
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(text)
	}
	return b.String(), nil
}

// =============================================================================
// helpers (private to composite.go)
// =============================================================================

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func splitNonEmpty(s, sep string) []string {
	out := []string{}
	for _, line := range strings.Split(s, sep) {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func truncateForDesc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
