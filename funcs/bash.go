package funcs

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// =============================================================================
// bash — base function: foreground shell exec.
//
// Background mode lives in a SEPARATE tool (bash_input) so the schema the
// model sees stays simple and the foreground/background lifecycles never get
// muddled in a single call.
// =============================================================================

type bashTool struct{}

func init() { Register(bashTool{}) }

func (bashTool) Name() string        { return "bash" }
func (bashTool) Kind() Kind          { return KindBase }
func (bashTool) DependsOn() []string { return nil }
func (bashTool) Description() string {
	return "Execute a shell command in a `bash -c` subshell (foreground only). " +
		"Blocks until completion or timeout. Returns stdout, stderr and exit_code. " +
		"For long-running commands (servers, watchers), use `bash_input` instead."
}

func (bashTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "Shell command to execute (interpreted by /bin/bash -c).",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "One-line summary of what this command does. Required so the user can audit the action.",
			},
			"timeout_seconds": map[string]any{
				"type": "integer",
				"description": "Max wall time in seconds (default 120). " +
					"0 means no timeout — reserved for internal callers like `task`; agents should leave unset or use a positive value.",
			},
		},
		"required": []string{"command", "description"},
	}
}

type BashResult struct {
	Description string `json:"description,omitempty"`
	Stdout      string `json:"stdout,omitempty"`
	Stderr      string `json:"stderr,omitempty"`
	ExitCode    int    `json:"exit_code"`
	TimedOut    bool   `json:"timed_out,omitempty"`
}

const bashDefaultTimeoutSeconds = 120

func (bashTool) Execute(ctx context.Context, args map[string]any, _ Caller) (any, error) {
	command, err := argRequiredString(args, "command")
	if err != nil {
		return nil, err
	}
	description, err := argRequiredString(args, "description")
	if err != nil {
		return nil, err
	}
	timeout, err := argInt(args, "timeout_seconds", bashDefaultTimeoutSeconds)
	if err != nil {
		return nil, err
	}

	// timeout=0 sentinel = no time cap (used by `task` for sub-agents).
	var ctx2 context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx2, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	} else {
		ctx2, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	cmd := exec.CommandContext(ctx2, "/bin/bash", "-c", command)
	var stdoutBuf, stderrBuf strBuilderWriter
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()
	res := BashResult{
		Description: description,
		Stdout:      stdoutBuf.String(),
		Stderr:      stderrBuf.String(),
	}
	if runErr != nil {
		switch {
		case timeout > 0 && ctx2.Err() == context.DeadlineExceeded:
			res.TimedOut = true
			res.ExitCode = -1
		case ctx.Err() == context.Canceled:
			return nil, ctx.Err()
		default:
			var ee *exec.ExitError
			if errors.As(runErr, &ee) {
				res.ExitCode = ee.ExitCode()
			} else {
				return nil, fmt.Errorf("bash run: %w", runErr)
			}
		}
	}
	return res, nil
}
