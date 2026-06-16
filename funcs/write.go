package funcs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// =============================================================================
// write — base function: anchor-based file edit.
//
// The file at `path` must contain `old_block` EXACTLY ONCE; that block is
// replaced with `new_block`. Empty `old_block` + nonexistent file = create.
// Empty `old_block` + existing file = refuse (would silently overwrite).
// =============================================================================

type writeTool struct{}

func init() { Register(writeTool{}) }

func (writeTool) Name() string        { return "write" }
func (writeTool) Kind() Kind          { return KindBase }
func (writeTool) DependsOn() []string { return nil }
func (writeTool) Description() string {
	return "Edit a file by replacing one EXACT block of text. Args: path, old_block, new_block. " +
		"Fails if old_block is missing or appears more than once — read the file first when that happens. " +
		"To create a new file: pass empty old_block and the full content as new_block. " +
		"Pass dry_run=true to get a diff preview of the change without touching the file."
}

func (writeTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Filesystem path of the file to edit (or create).",
			},
			"old_block": map[string]any{
				"type": "string",
				"description": "Exact existing text to replace, including indentation and surrounding context as needed " +
					"to be unique in the file. Pass empty string to create a brand-new file.",
			},
			"new_block": map[string]any{
				"type":        "string",
				"description": "Text that will replace old_block. Pass empty string to delete the matched block.",
			},
			"dry_run": map[string]any{
				"type":        "boolean",
				"description": "If true, validate the edit and return a `diff` preview WITHOUT modifying the file. Default false.",
			},
		},
		"required": []string{"path", "old_block", "new_block"},
	}
}

type WriteResult struct {
	Path        string `json:"path"`
	BytesBefore int    `json:"bytes_before"`
	BytesAfter  int    `json:"bytes_after"`
	Created     bool   `json:"created,omitempty"`
	DryRun      bool   `json:"dry_run,omitempty"`
	Diff        string `json:"diff,omitempty"`
}

// RiskOf is High for any real edit; we don't try to second-guess "small vs
// large" since whitespace can be deceptive. Creating a new file is also High
// since the destination may be a sensitive path. A dry_run preview mutates
// nothing, so it is Low and never prompts for approval.
func (writeTool) RiskOf(args map[string]any) Risk {
	if dry, _ := args["dry_run"].(bool); dry {
		return RiskLow
	}
	return RiskHigh
}

func (writeTool) Execute(_ context.Context, args map[string]any, _ Caller) (any, error) {
	path, err := argRequiredString(args, "path")
	if err != nil {
		return nil, err
	}
	if _, ok := args["old_block"]; !ok {
		return nil, fmt.Errorf(`arg "old_block" is required (empty string to create a new file)`)
	}
	if _, ok := args["new_block"]; !ok {
		return nil, fmt.Errorf(`arg "new_block" is required`)
	}
	oldBlock, _ := args["old_block"].(string)
	newBlock, _ := args["new_block"].(string)
	dryRun, err := argBool(args, "dry_run", false)
	if err != nil {
		return nil, err
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	existing, readErr := os.ReadFile(abs)

	// Create path
	if oldBlock == "" {
		if readErr == nil {
			return nil, fmt.Errorf(
				"refusing to overwrite existing file %s with empty old_block; "+
					"read it and provide a real old_block, or delete it explicitly first",
				abs)
		}
		if !os.IsNotExist(readErr) {
			return nil, readErr
		}
		if dryRun {
			return WriteResult{
				Path: abs, BytesAfter: len(newBlock), Created: true,
				DryRun: true, Diff: blockDiff(abs, "", newBlock, 1, true),
			}, nil
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(abs, []byte(newBlock), 0o644); err != nil {
			return nil, err
		}
		return WriteResult{Path: abs, BytesAfter: len(newBlock), Created: true}, nil
	}

	// Edit path
	if readErr != nil {
		return nil, fmt.Errorf("read %s: %w", abs, readErr)
	}
	original := string(existing)
	switch strings.Count(original, oldBlock) {
	case 0:
		return nil, fmt.Errorf(
			"old_block not found in %s — read the file (use `read`) and call write again with a real anchor",
			abs)
	case 1:
	default:
		return nil, fmt.Errorf(
			"old_block matches %d times in %s — anchor is ambiguous, widen with surrounding context",
			strings.Count(original, oldBlock), abs)
	}
	updated := strings.Replace(original, oldBlock, newBlock, 1)
	if dryRun {
		startLine := strings.Count(original[:strings.Index(original, oldBlock)], "\n") + 1
		return WriteResult{
			Path: abs, BytesBefore: len(existing), BytesAfter: len(updated),
			DryRun: true, Diff: blockDiff(abs, oldBlock, newBlock, startLine, false),
		}, nil
	}
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return nil, err
	}
	return WriteResult{Path: abs, BytesBefore: len(existing), BytesAfter: len(updated)}, nil
}

// blockDiff renders a minimal unified-diff-style preview of a single-block
// replacement. write only ever changes one contiguous block, so showing that
// block's removed (-) and added (+) lines — anchored at its 1-indexed start
// line — is exact and needs no LCS pass or external dependency.
func blockDiff(path, oldBlock, newBlock string, startLine int, created bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", path, path)
	if created {
		b.WriteString("@@ new file @@\n")
	} else {
		fmt.Fprintf(&b, "@@ line %d @@\n", startLine)
	}
	for _, ln := range diffLines(oldBlock) {
		b.WriteString("-" + ln + "\n")
	}
	for _, ln := range diffLines(newBlock) {
		b.WriteString("+" + ln + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// diffLines splits a block into display lines, dropping a single trailing
// newline so "a\n" renders as one line rather than one line plus an empty one.
func diffLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// strBuilderWriter is used by bashTool to capture child stdout/stderr.
type strBuilderWriter struct{ buf []byte }

func (s *strBuilderWriter) Write(p []byte) (int, error) { s.buf = append(s.buf, p...); return len(p), nil }
func (s *strBuilderWriter) String() string              { return string(s.buf) }
