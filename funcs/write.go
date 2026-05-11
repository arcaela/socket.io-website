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
		"To create a new file: pass empty old_block and the full content as new_block."
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
		},
		"required": []string{"path", "old_block", "new_block"},
	}
}

type WriteResult struct {
	Path        string `json:"path"`
	BytesBefore int    `json:"bytes_before"`
	BytesAfter  int    `json:"bytes_after"`
	Created     bool   `json:"created,omitempty"`
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
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return nil, err
	}
	return WriteResult{Path: abs, BytesBefore: len(existing), BytesAfter: len(updated)}, nil
}

// strBuilderWriter is used by bashTool to capture child stdout/stderr.
type strBuilderWriter struct{ buf []byte }

func (s *strBuilderWriter) Write(p []byte) (int, error) { s.buf = append(s.buf, p...); return len(p), nil }
func (s *strBuilderWriter) String() string              { return string(s.buf) }
