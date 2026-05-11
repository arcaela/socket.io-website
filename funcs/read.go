package funcs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

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


func init() { Register(ReadTool{}) }
