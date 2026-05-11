package tools

import (
	"sync"
	"syscall"
	"time"
)

// Background-process lifecycle.
//
// Every process started via BashTool's background mode is registered here.
// On clean shutdown of the parent `mini` process — whether one-shot or REPL —
// main.go calls ShutdownBackgroundJobs, which signals each tracked process
// group to terminate, waits a grace period, and then SIGKILLs survivors.
//
// Why a global: the tracker has to be reachable both from the tool that
// spawns processes (deep inside a Caller chain) and from the main package
// without threading state through every layer. The agent + tool API stay
// LLM-agnostic; this is purely an internal cleanup concern.

var bgTracker = &processTracker{}

type processTracker struct {
	mu   sync.Mutex
	pids []int
}

// Track registers a background process by its PID. The PID must be the
// process group leader (i.e. started with Setpgid=true) so that signals
// sent to -PID reach the whole subtree.
func (t *processTracker) track(pid int) {
	t.mu.Lock()
	t.pids = append(t.pids, pid)
	t.mu.Unlock()
}

// Untrack removes a PID from the tracker (called by the reaper goroutine
// when the process exits naturally).
func (t *processTracker) untrack(pid int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, p := range t.pids {
		if p == pid {
			t.pids = append(t.pids[:i], t.pids[i+1:]...)
			return
		}
	}
}

// snapshot returns a copy of currently tracked PIDs.
func (t *processTracker) snapshot() []int {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]int, len(t.pids))
	copy(out, t.pids)
	return out
}

// resetForTesting clears the tracker. Tests only.
func (t *processTracker) resetForTesting() {
	t.mu.Lock()
	t.pids = nil
	t.mu.Unlock()
}

// ShutdownBackgroundJobs terminates every tracked background process.
//
//   1. Sends SIGTERM to each process group (kill -TERM -PGID).
//   2. Polls up to `grace` for processes to exit voluntarily.
//   3. SIGKILLs anything still alive.
//
// Safe to call multiple times. Returns the number of processes that were
// alive when called (useful for diagnostics).
func ShutdownBackgroundJobs(grace time.Duration) int {
	pids := bgTracker.snapshot()
	if len(pids) == 0 {
		return 0
	}

	// SIGTERM the whole process group of each tracked PID.
	for _, pid := range pids {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	}

	// Wait for voluntary exit, up to `grace`.
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		alive := false
		for _, pid := range pids {
			if syscall.Kill(pid, 0) == nil {
				alive = true
				break
			}
		}
		if !alive {
			return len(pids)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Force-kill survivors.
	for _, pid := range pids {
		if syscall.Kill(pid, 0) == nil {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}
	return len(pids)
}

// TrackedJobCount is exposed mainly for diagnostics / tests.
func TrackedJobCount() int {
	return len(bgTracker.snapshot())
}
