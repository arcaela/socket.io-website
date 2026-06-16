package funcs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// =============================================================================
// bash_input — base function: spawn a command in the background.
//
// Returns immediately with a `job_id` the agent can use later (via the
// `bash_output` tool) to inspect output, check exit status, or kill the job.
// Output is written to a per-job log file under ~/.mini/jobs/.
//
// Why a separate tool from `bash`: foreground exec and detached lifecycle
// have different semantics (no timeout, async exit, log file). Splitting
// keeps each tool's schema and result shape narrow.
// =============================================================================

type bashInputTool struct{}

func init() { Register(bashInputTool{}) }

func (bashInputTool) Name() string        { return "bash_input" }
func (bashInputTool) Kind() Kind          { return KindBase }
func (bashInputTool) DependsOn() []string { return nil }
func (bashInputTool) Description() string {
	return "Start a shell command detached from the agent. Returns a `job_id` immediately; " +
		"the command keeps running. Use `bash_output` later (with the same `job_id`) to read " +
		"its output or check whether it finished. Use this for servers, watchers, build pipelines."
}

func (bashInputTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "Shell command to spawn (interpreted by /bin/bash -c).",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "One-line summary so the user can audit what was started.",
			},
		},
		"required": []string{"command", "description"},
	}
}

type BashInputResult struct {
	Description string `json:"description,omitempty"`
	JobID       string `json:"job_id"`
	LogFile     string `json:"log_file"`
	PID         int    `json:"pid"`
	StartedAt   string `json:"started_at"`
}

// RiskOf treats every background spawn as at least High, even for ostensibly
// benign commands — backgrounding means the user loses immediate visibility,
// which raises the impact of any misjudgement.
func (bashInputTool) RiskOf(args map[string]any) Risk {
	cmd, _ := args["command"].(string)
	if r := classifyBashCommand(cmd); r > RiskHigh {
		return r
	}
	return RiskHigh
}

func (bashInputTool) Execute(_ context.Context, args map[string]any, _ Caller) (any, error) {
	command, err := argRequiredString(args, "command")
	if err != nil {
		return nil, err
	}
	description, err := argRequiredString(args, "description")
	if err != nil {
		return nil, err
	}
	return spawnBackground(command, description)
}

// spawnBackground writes a header + the command's stdout/stderr + a trailing
// exit marker to a fresh log file in the per-job directory. The wrapped bash
// writes its OWN exit marker so the file is reliable even if our parent
// process dies between Start and Wait.
func spawnBackground(command, description string) (BashInputResult, error) {
	jobsDir := jobsDirPath()
	if err := os.MkdirAll(jobsDir, 0o700); err != nil {
		return BashInputResult{}, fmt.Errorf("create jobs dir: %w", err)
	}
	jobID := newJobID()
	logFile := filepath.Join(jobsDir, jobID+".log")

	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return BashInputResult{}, fmt.Errorf("open log file: %w", err)
	}

	header := fmt.Sprintf("[mini] job=%s started=%s description=%q cmd=%q\n",
		jobID, time.Now().UTC().Format(time.RFC3339), description, command)
	_, _ = f.WriteString(header)

	wrapped := fmt.Sprintf(
		`(%s); __rc=$?; printf '\n[mini] exited at %%s exit_code=%%d\n' "$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)" "$__rc"`,
		command,
	)
	cmd := exec.Command("/bin/bash", "-c", wrapped)
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		_ = f.Close()
		_ = os.Remove(logFile)
		return BashInputResult{}, fmt.Errorf("start command: %w", err)
	}
	pid := cmd.Process.Pid
	bgTracker.track(pid)
	jobRegistry.store(jobID, logFile, pid)

	go func() {
		_ = cmd.Wait()
		bgTracker.untrack(pid)
		_ = f.Close()
	}()

	return BashInputResult{
		Description: description,
		JobID:       jobID,
		LogFile:     logFile,
		PID:         cmd.Process.Pid,
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// ---- shared helpers for background lifecycle ----

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

// jobRegistry maps job_id → {log file, pid} so `bash_output` can look up a
// job by id alone (instead of forcing the agent to remember its log path).
type jobInfo struct {
	logFile string
	pid     int
}

var jobRegistry = &jobMap{m: map[string]jobInfo{}}

type jobMap struct {
	mu sync.RWMutex
	m  map[string]jobInfo
}

func (j *jobMap) store(id, logFile string, pid int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.m[id] = jobInfo{logFile: logFile, pid: pid}
}

func (j *jobMap) lookup(id string) (jobInfo, bool) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	info, ok := j.m[id]
	return info, ok
}

// JobSummary is what the REPL's /jobs command sees. Exported because
// inspectors (cli, future tools) live in a different package.
type JobSummary struct {
	JobID   string
	LogFile string
	PID     int
	Running bool
}

// ListJobs returns a snapshot of every background job this process has
// started, including ones that already finished (so /jobs can show their
// log path even after exit).
func ListJobs() []JobSummary {
	jobRegistry.mu.RLock()
	out := make([]JobSummary, 0, len(jobRegistry.m))
	for id, info := range jobRegistry.m {
		out = append(out, JobSummary{
			JobID:   id,
			LogFile: info.logFile,
			PID:     info.pid,
			Running: isPidAlive(info.pid),
		})
	}
	jobRegistry.mu.RUnlock()
	return out
}

