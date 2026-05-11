package funcs

import (
	"context"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBashKill_TerminatesRunningJob(t *testing.T) {
	t.Setenv("MINI_JOBS_DIR", t.TempDir())
	r := BuiltIn()

	out, err := r.Call(context.Background(), "bash_input", map[string]any{
		"command":     "sleep 30",
		"description": "to be killed",
	})
	if err != nil {
		t.Fatal(err)
	}
	in := out.(BashInputResult)
	if !isPidAlive(in.PID) {
		t.Fatalf("expected job to be running")
	}

	out, err = r.Call(context.Background(), "bash_kill", map[string]any{
		"job_id": in.JobID,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(BashKillResult)
	if !res.WasRunning {
		t.Fatalf("expected WasRunning=true, got %+v", res)
	}
	if !res.Killed {
		t.Fatalf("expected Killed=true, got %+v", res)
	}

	// PID should be gone now.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !isPidAlive(in.PID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("PID %d still alive after bash_kill", in.PID)
}

func TestBashKill_AlreadyExited(t *testing.T) {
	t.Setenv("MINI_JOBS_DIR", t.TempDir())
	r := BuiltIn()
	out, err := r.Call(context.Background(), "bash_input", map[string]any{
		"command":     "true",
		"description": "instant",
	})
	if err != nil {
		t.Fatal(err)
	}
	in := out.(BashInputResult)
	// Wait for natural exit.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !isPidAlive(in.PID) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	out, err = r.Call(context.Background(), "bash_kill", map[string]any{"job_id": in.JobID})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(BashKillResult)
	if res.WasRunning || res.Killed {
		t.Fatalf("expected no-op on already-exited job, got %+v", res)
	}
}

func TestBashKill_UnknownJob(t *testing.T) {
	r := BuiltIn()
	_, err := r.Call(context.Background(), "bash_kill", map[string]any{"job_id": "does-not-exist"})
	if err == nil || !strings.Contains(err.Error(), "unknown job_id") {
		t.Fatalf("expected unknown-job error, got %v", err)
	}
}

func TestListJobs_IncludesActiveAndExited(t *testing.T) {
	t.Setenv("MINI_JOBS_DIR", t.TempDir())
	jobRegistry.mu.Lock()
	jobRegistry.m = map[string]jobInfo{} // reset for isolation
	jobRegistry.mu.Unlock()

	r := BuiltIn()

	// Start two jobs.
	a, _ := r.Call(context.Background(), "bash_input", map[string]any{"command": "sleep 30", "description": "a"})
	b, _ := r.Call(context.Background(), "bash_input", map[string]any{"command": "true", "description": "b"})
	t.Cleanup(func() {
		_ = syscall.Kill(-a.(BashInputResult).PID, syscall.SIGKILL)
		_ = syscall.Kill(-b.(BashInputResult).PID, syscall.SIGKILL)
	})

	// Wait for b to exit, then snapshot.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !isPidAlive(b.(BashInputResult).PID) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	jobs := ListJobs()
	if len(jobs) < 2 {
		t.Fatalf("expected at least 2 jobs, got %d", len(jobs))
	}
	var sawRunning, sawExited bool
	for _, j := range jobs {
		if j.Running {
			sawRunning = true
		} else {
			sawExited = true
		}
	}
	if !sawRunning || !sawExited {
		t.Fatalf("expected at least one running and one exited; got %+v", jobs)
	}
}
