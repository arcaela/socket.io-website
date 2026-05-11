package funcs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

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

func init() { Register(MemoryTool{}) }
