package funcs

import (
	"context"
	"fmt"
	"syscall"
	"time"
)

// =============================================================================
// bash_kill — base function: terminate a background job started by bash_input.
//
// Looks up the job by id, sends SIGTERM to the entire process group, waits
// a short grace period, then SIGKILLs anything still alive. Matches the
// strategy ShutdownBackgroundJobs uses at exit, but scoped to one job.
// =============================================================================

type bashKillTool struct{}

func init() { Register(bashKillTool{}) }

func (bashKillTool) Name() string        { return "bash_kill" }
func (bashKillTool) Kind() Kind          { return KindBase }
func (bashKillTool) DependsOn() []string { return nil }
func (bashKillTool) Description() string {
	return "Kill a background job previously started by `bash_input`. Pass the `job_id` " +
		"returned by bash_input. Sends SIGTERM, waits up to 2 seconds, then SIGKILL if needed."
}

func (bashKillTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"job_id": map[string]any{
				"type":        "string",
				"description": "The id returned by `bash_input` when the job was started.",
			},
			"grace_seconds": map[string]any{
				"type":        "integer",
				"description": "Seconds to wait between SIGTERM and SIGKILL (default 2, max 30).",
			},
		},
		"required": []string{"job_id"},
	}
}

type BashKillResult struct {
	JobID      string `json:"job_id"`
	PID        int    `json:"pid"`
	Killed     bool   `json:"killed"`             // true if we actually had to act
	WasRunning bool   `json:"was_running"`        // true if the process was still alive when we called
	Forced     bool   `json:"forced,omitempty"`   // SIGKILL had to be sent
}

// RiskOf — killing a background job is High: it might lose work, but it
// isn't catastrophic (the user explicitly started the job).
func (bashKillTool) RiskOf(_ map[string]any) Risk { return RiskHigh }

func (bashKillTool) Execute(_ context.Context, args map[string]any, _ Caller) (any, error) {
	jobID, err := argRequiredString(args, "job_id")
	if err != nil {
		return nil, err
	}
	grace, err := argInt(args, "grace_seconds", 2)
	if err != nil {
		return nil, err
	}
	if grace <= 0 {
		grace = 2
	}
	if grace > 30 {
		grace = 30
	}

	info, ok := jobRegistry.lookup(jobID)
	if !ok {
		return nil, fmt.Errorf("unknown job_id %q (was it started in this process?)", jobID)
	}

	res := BashKillResult{
		JobID:      jobID,
		PID:        info.pid,
		WasRunning: isPidAlive(info.pid),
	}
	if !res.WasRunning {
		return res, nil
	}

	// SIGTERM the process group (negative PID).
	_ = syscall.Kill(-info.pid, syscall.SIGTERM)
	res.Killed = true

	deadline := time.Now().Add(time.Duration(grace) * time.Second)
	for time.Now().Before(deadline) {
		if !isPidAlive(info.pid) {
			return res, nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Still alive after grace: force-kill the whole group.
	_ = syscall.Kill(-info.pid, syscall.SIGKILL)
	res.Forced = true
	return res, nil
}
