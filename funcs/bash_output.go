package funcs

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// =============================================================================
// bash_output — base function: read a background job's output.
//
// Pairs with `bash_input`. Looks up the job by id, reads its log file,
// and reports whether the process is still running (and its exit code if
// it has finished).
// =============================================================================

type bashOutputTool struct{}

func init() { Register(bashOutputTool{}) }

func (bashOutputTool) Name() string        { return "bash_output" }
func (bashOutputTool) Kind() Kind          { return KindBase }
func (bashOutputTool) DependsOn() []string { return nil }
func (bashOutputTool) Description() string {
	return "Read the output of a background job started by `bash_input`. Pass the `job_id` " +
		"returned earlier. Reports the log content (optionally just the last N lines), whether " +
		"the process is still running, and the exit code once it has finished."
}

func (bashOutputTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"job_id": map[string]any{
				"type":        "string",
				"description": "The id returned by `bash_input` when the job was started.",
			},
			"tail_lines": map[string]any{
				"type":        "integer",
				"description": "If > 0, return only the last N lines of the log (default 0 = full log).",
			},
		},
		"required": []string{"job_id"},
	}
}

type BashOutputResult struct {
	JobID    string `json:"job_id"`
	LogFile  string `json:"log_file"`
	PID      int    `json:"pid"`
	Running  bool   `json:"running"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Content  string `json:"content"`
}

var exitMarkerRE = regexp.MustCompile(`\[mini\] exited at \S+ exit_code=(-?\d+)`)

func (bashOutputTool) Execute(_ context.Context, args map[string]any, _ Caller) (any, error) {
	jobID, err := argRequiredString(args, "job_id")
	if err != nil {
		return nil, err
	}
	tail, err := argInt(args, "tail_lines", 0)
	if err != nil {
		return nil, err
	}

	info, ok := jobRegistry.lookup(jobID)
	if !ok {
		return nil, fmt.Errorf("unknown job_id %q (was it started in this process?)", jobID)
	}
	data, err := os.ReadFile(info.logFile)
	if err != nil {
		return nil, fmt.Errorf("read log: %w", err)
	}
	content := string(data)

	if tail > 0 {
		lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
		if len(lines) > tail {
			lines = lines[len(lines)-tail:]
		}
		content = strings.Join(lines, "\n")
	}

	res := BashOutputResult{
		JobID:   jobID,
		LogFile: info.logFile,
		PID:     info.pid,
		Content: content,
		Running: isPidAlive(info.pid),
	}
	if m := exitMarkerRE.FindStringSubmatch(string(data)); len(m) == 2 {
		if code, err := strconv.Atoi(m[1]); err == nil {
			res.Running = false
			res.ExitCode = &code
		}
	}
	return res, nil
}

func isPidAlive(pid int) bool {
	// kill -0 checks for existence without sending a signal.
	return syscall.Kill(pid, 0) == nil
}
