package funcs

import (
	"context"
	"fmt"
	"os"
)

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
		exe = "mini"
	}
	command := fmt.Sprintf("%s chat %s", shellQuote(exe), shellQuote(prompt))
	desc := fmt.Sprintf("Sub-agent task: %s", truncateForDesc(prompt, 100))

	if background {
		// Background sub-agent: delegate to bash_input, which spawns a
		// detached process and returns a job_id the caller can poll later
		// with bash_output.
		return c.Call(ctx, "bash_input", map[string]any{
			"command":     command,
			"description": desc,
		})
	}
	// Foreground sub-agent: bash with timeout_seconds=0 so multi-turn
	// agents are not capped by the 120s default.
	return c.Call(ctx, "bash", map[string]any{
		"command":         command,
		"description":     desc,
		"timeout_seconds": 0,
	})
}


func init() { Register(TaskTool{}) }
