package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// =============================================================================
// BashTool — base function: run a command in a non-interactive bash subshell.
// Schema is intentionally tiny: command, description, background. Anything
// else (cwd, env vars, redirection) the model can express in the command
// itself with `cd /path && ...` etc.
// =============================================================================

type BashTool struct{}

func (BashTool) Name() string         { return "bash" }
func (BashTool) Kind() Kind           { return KindBase }
func (BashTool) DependsOn() []string  { return nil }
func (BashTool) Description() string {
	return "Execute a shell command in a `bash -c` subshell. " +
		"Foreground: blocks until completion (60s timeout) and returns stdout/stderr/exit_code. " +
		"Background: spawns detached, returns immediately with `log_file` path; the agent can read that file later to see live output."
}

func (BashTool) Schema() map[string]any {
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
				"description": "Foreground timeout in seconds. Default 120. " +
					"Mutually exclusive with `background` — sending both is an error. " +
					"0 means no timeout (reserved for internal callers; agents should leave it unset or use a positive value).",
			},
			"background": map[string]any{
				"type": "boolean",
				"description": "If true, spawn detached: stdout+stderr go to a log file and the call returns " +
					"immediately with `log_file`, `job_id`, `pid`. Use for servers, watchers, or long-running tasks. " +
					"Default false. Cannot be combined with `timeout_seconds`.",
			},
		},
		"required": []string{"command", "description"},
	}
}

// BashResult is unified across foreground and background modes. Foreground
// fills Stdout/Stderr/ExitCode/TimedOut; background fills JobID/LogFile/PID
// and the command keeps running.
type BashResult struct {
	Description string `json:"description,omitempty"`

	// Foreground.
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out,omitempty"`

	// Background.
	Background bool   `json:"background,omitempty"`
	JobID      string `json:"job_id,omitempty"`
	LogFile    string `json:"log_file,omitempty"`
	PID        int    `json:"pid,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
}

const bashDefaultTimeoutSeconds = 120

func (BashTool) Execute(ctx context.Context, args map[string]any, _ Caller) (any, error) {
	command, err := argRequiredString(args, "command")
	if err != nil {
		return nil, err
	}
	description, err := argRequiredString(args, "description")
	if err != nil {
		return nil, err
	}
	background, err := argBool(args, "background", false)
	if err != nil {
		return nil, err
	}

	// Detect whether the caller explicitly set timeout_seconds — different
	// from "left it unset". Used both for default selection and for the
	// mutual-exclusion check below.
	_, timeoutWasSet := args["timeout_seconds"]
	timeout, err := argInt(args, "timeout_seconds", bashDefaultTimeoutSeconds)
	if err != nil {
		return nil, err
	}

	if background && timeoutWasSet {
		return nil, fmt.Errorf(
			"cannot combine `background` and `timeout_seconds`: background processes do not honor a foreground timeout. " +
				"Pick one")
	}
	if background {
		return runBashBackground(command, description)
	}

	// Foreground. timeout=0 is a sentinel reserved for internal callers
	// (notably Task) and means "no time cap".
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

// =============================================================================
// runBashBackground spawns a detached bash subprocess. The wrapped command
// emits its own exit marker into the log file so the marker is reliable even
// if our parent process exits first (one-shot CLI mode).
// =============================================================================

func runBashBackground(command, description string) (any, error) {
	jobsDir := jobsDirPath()
	if err := os.MkdirAll(jobsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create jobs dir: %w", err)
	}
	jobID := newJobID()
	logFile := filepath.Join(jobsDir, jobID+".log")

	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	header := fmt.Sprintf("[mini-cli] job=%s started=%s description=%q cmd=%q\n",
		jobID, time.Now().UTC().Format(time.RFC3339), description, command)
	_, _ = f.WriteString(header)

	wrapped := fmt.Sprintf(
		`(%s); __rc=$?; printf '\n[mini-cli] exited at %%s exit_code=%%d\n' "$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)" "$__rc"`,
		command,
	)

	cmd := exec.Command("/bin/bash", "-c", wrapped)
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		_ = f.Close()
		_ = os.Remove(logFile)
		return nil, fmt.Errorf("start command: %w", err)
	}
	pid := cmd.Process.Pid
	bgTracker.track(pid)

	go func() {
		_ = cmd.Wait()
		bgTracker.untrack(pid)
		_ = f.Close()
	}()

	return BashResult{
		Description: description,
		Background:  true,
		JobID:       jobID,
		LogFile:     logFile,
		PID:         cmd.Process.Pid,
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func jobsDirPath() string {
	if v := os.Getenv("MINI_JOBS_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "mini-jobs")
	}
	return filepath.Join(home, ".mini", "jobs")
}

func newJobID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}

// =============================================================================
// WriteTool — base function: anchor-based file edit.
//
// Args: path, old_block, new_block.
//
// Semantics:
//   - The file at `path` must contain `old_block` EXACTLY ONCE. The tool
//     replaces that occurrence with `new_block` and writes the file back.
//   - If `old_block` is not found, returns an error asking the agent to read
//     the file first (so it can see the actual contents and pick a real
//     anchor).
//   - If `old_block` is found multiple times, returns an error: the anchor
//     is ambiguous and the agent must pick a wider unique block.
//   - Special case: if `old_block` is empty AND the file does not exist, the
//     file is created with `new_block`. This is the explicit way to create
//     new files.
//   - If `old_block` is empty and the file DOES exist, that's an error
//     (would silently overwrite).
//
// Why this design instead of a plain "write whole file": agents are
// notoriously bad at preserving the rest of a file when asked to make a
// targeted edit. Anchor-based edits force them to demonstrate they have read
// the file recently enough to know its current state.
// =============================================================================

type WriteTool struct{}

func (WriteTool) Name() string         { return "write" }
func (WriteTool) Kind() Kind           { return KindBase }
func (WriteTool) DependsOn() []string  { return nil }
func (WriteTool) Description() string {
	return "Edit a file by replacing an exact block of text. Provide `path`, `old_block` (current content to replace), and `new_block` (replacement). " +
		"Fails if `old_block` is missing or appears more than once — read the file first when that happens. " +
		"To create a new file: pass empty `old_block` and the full content as `new_block`."
}

func (WriteTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Filesystem path of the file to edit (or create).",
			},
			"old_block": map[string]any{
				"type": "string",
				"description": "The exact existing text to replace, including indentation and surrounding context as needed " +
					"to be unique in the file. Pass empty string to create a brand-new file.",
			},
			"new_block": map[string]any{
				"type":        "string",
				"description": "The new text that will replace `old_block`. Pass empty string to delete the matched block.",
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

func (WriteTool) Execute(_ context.Context, args map[string]any, _ Caller) (any, error) {
	path, err := argRequiredString(args, "path")
	if err != nil {
		return nil, err
	}
	// old_block / new_block can be empty strings; we still require the keys.
	if _, ok := args["old_block"]; !ok {
		return nil, fmt.Errorf(`arg "old_block" is required (use empty string to create a new file)`)
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

	// Create path: empty old_block + nonexistent file.
	if oldBlock == "" {
		if readErr == nil {
			return nil, fmt.Errorf(
				"refusing to overwrite existing file %s with empty old_block; "+
					"read the file and provide a real old_block, or delete it explicitly first",
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
		return WriteResult{
			Path:        abs,
			BytesBefore: 0,
			BytesAfter:  len(newBlock),
			Created:     true,
		}, nil
	}

	// Edit path: file must exist.
	if readErr != nil {
		return nil, fmt.Errorf("read %s: %w", abs, readErr)
	}

	original := string(existing)
	occurrences := strings.Count(original, oldBlock)
	switch occurrences {
	case 0:
		return nil, fmt.Errorf(
			"old_block not found in %s — the file's current content does not contain that text. "+
				"Read the file first (use the `read` tool) to see its actual content, then call `write` again with a real anchor",
			abs)
	case 1:
		// good
	default:
		return nil, fmt.Errorf(
			"old_block matches %d times in %s — anchor is ambiguous. "+
				"Expand old_block with surrounding context until it is unique",
			occurrences, abs)
	}

	updated := strings.Replace(original, oldBlock, newBlock, 1)
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return nil, err
	}
	return WriteResult{
		Path:        abs,
		BytesBefore: len(existing),
		BytesAfter:  len(updated),
	}, nil
}

// =============================================================================
// strBuilderWriter — internal io.Writer used by BashTool foreground mode.
// =============================================================================

type strBuilderWriter struct {
	buf []byte
}

func (s *strBuilderWriter) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	return len(p), nil
}
func (s *strBuilderWriter) String() string { return string(s.buf) }
