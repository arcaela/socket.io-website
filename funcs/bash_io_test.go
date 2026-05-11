package funcs

import (
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// =============================================================================
// bash_input — spawning detached jobs and routing output to a log file.
// These replace the old TestBash_Background* set we dropped during the
// foreground/background split. Same coverage, new API.
// =============================================================================

func TestBashInput_ReturnsImmediately(t *testing.T) {
	t.Setenv("MINI_JOBS_DIR", t.TempDir())
	r := BuiltIn()

	start := time.Now()
	out, err := r.Call(context.Background(), "bash_input", map[string]any{
		"command":     "sleep 5",
		"description": "would block if foreground",
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("bash_input should return immediately, took %v", elapsed)
	}
	in := out.(BashInputResult)
	if in.JobID == "" || in.LogFile == "" || in.PID == 0 {
		t.Fatalf("missing fields: %+v", in)
	}
	// Cleanup the long sleep.
	_ = syscall.Kill(-in.PID, syscall.SIGKILL)
}

func TestBashInput_WritesHeaderAndExitMarker(t *testing.T) {
	t.Setenv("MINI_JOBS_DIR", t.TempDir())
	r := BuiltIn()
	out, err := r.Call(context.Background(), "bash_input", map[string]any{
		"command":     `echo a; echo b`,
		"description": "two lines",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(BashInputResult)
	waitFor(t, res.LogFile, "exit_code=0", 2*time.Second)
	body, _ := os.ReadFile(res.LogFile)
	for _, want := range []string{
		"[mini] job=",
		"description=\"two lines\"",
		"a\nb",
		"exited at",
		"exit_code=0",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("log missing %q. body=%q", want, string(body))
		}
	}
}

func TestBashInput_SurvivesContextCancel(t *testing.T) {
	t.Setenv("MINI_JOBS_DIR", t.TempDir())
	r := BuiltIn()
	ctx, cancel := context.WithCancel(context.Background())
	out, err := r.Call(ctx, "bash_input", map[string]any{
		"command":     `sleep 0.3; echo survived`,
		"description": "prove detachment",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(BashInputResult)
	cancel() // immediately

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(res.LogFile)
		if strings.Contains(string(data), "survived") {
			return
		}
		time.Sleep(60 * time.Millisecond)
	}
	body, _ := os.ReadFile(res.LogFile)
	t.Fatalf("background process did not survive cancel; log=%q", string(body))
}

func TestBashInput_StderrMergedAndExitCodePropagated(t *testing.T) {
	t.Setenv("MINI_JOBS_DIR", t.TempDir())
	r := BuiltIn()
	out, err := r.Call(context.Background(), "bash_input", map[string]any{
		"command":     `echo OUT; echo ERR >&2; exit 3`,
		"description": "mix",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(BashInputResult)
	waitFor(t, res.LogFile, "exit_code=3", 2*time.Second)
	body, _ := os.ReadFile(res.LogFile)
	for _, want := range []string{"OUT", "ERR", "exit_code=3"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("log missing %q", want)
		}
	}
}

func TestBashInput_RequiresDescription(t *testing.T) {
	t.Setenv("MINI_JOBS_DIR", t.TempDir())
	r := BuiltIn()
	_, err := r.Call(context.Background(), "bash_input", map[string]any{
		"command": "true",
	})
	if err == nil {
		t.Fatal("description must be required")
	}
}

func TestBashInput_TrackedInRegistry(t *testing.T) {
	t.Setenv("MINI_JOBS_DIR", t.TempDir())
	r := BuiltIn()

	out, err := r.Call(context.Background(), "bash_input", map[string]any{
		"command": "sleep 10", "description": "tracked",
	})
	if err != nil {
		t.Fatal(err)
	}
	in := out.(BashInputResult)
	t.Cleanup(func() { _ = syscall.Kill(-in.PID, syscall.SIGKILL) })

	if _, ok := jobRegistry.lookup(in.JobID); !ok {
		t.Fatalf("job %q not in registry", in.JobID)
	}
}

// =============================================================================
// bash_output — read live or final state of a background job.
// =============================================================================

func TestBashOutput_RunningJobShowsLiveContent(t *testing.T) {
	t.Setenv("MINI_JOBS_DIR", t.TempDir())
	r := BuiltIn()
	out, err := r.Call(context.Background(), "bash_input", map[string]any{
		"command":     `for i in 1 2 3 4 5; do echo step-$i; sleep 0.15; done`,
		"description": "ticker",
	})
	if err != nil {
		t.Fatal(err)
	}
	in := out.(BashInputResult)
	t.Cleanup(func() { _ = syscall.Kill(-in.PID, syscall.SIGKILL) })

	time.Sleep(250 * time.Millisecond)
	out, err = r.Call(context.Background(), "bash_output", map[string]any{"job_id": in.JobID})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(BashOutputResult)
	if !res.Running {
		t.Errorf("job should still be running at this point")
	}
	if !strings.Contains(res.Content, "step-1") {
		t.Errorf("live read should include step-1: %q", res.Content)
	}
	if res.ExitCode != nil {
		t.Errorf("exit_code must be nil while running, got %v", *res.ExitCode)
	}
}

func TestBashOutput_FinishedJobReportsExitCode(t *testing.T) {
	t.Setenv("MINI_JOBS_DIR", t.TempDir())
	r := BuiltIn()
	out, err := r.Call(context.Background(), "bash_input", map[string]any{
		"command":     `echo done; exit 7`,
		"description": "ends with 7",
	})
	if err != nil {
		t.Fatal(err)
	}
	in := out.(BashInputResult)

	waitFor(t, in.LogFile, "exit_code=7", 2*time.Second)

	out, err = r.Call(context.Background(), "bash_output", map[string]any{"job_id": in.JobID})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(BashOutputResult)
	if res.Running {
		t.Error("job should not be reported running after exit marker")
	}
	if res.ExitCode == nil || *res.ExitCode != 7 {
		t.Fatalf("exit code wrong: %+v", res.ExitCode)
	}
}

func TestBashOutput_TailLines(t *testing.T) {
	t.Setenv("MINI_JOBS_DIR", t.TempDir())
	r := BuiltIn()
	out, err := r.Call(context.Background(), "bash_input", map[string]any{
		"command":     `for i in 1 2 3 4 5; do echo line$i; done`,
		"description": "five",
	})
	if err != nil {
		t.Fatal(err)
	}
	in := out.(BashInputResult)
	waitFor(t, in.LogFile, "exit_code=0", 2*time.Second)

	// tail must include line5 + exit marker; the log ends with
	// "line5\n\n[mini] exited..." so we need at least 3 lines.
	out, err = r.Call(context.Background(), "bash_output", map[string]any{
		"job_id":     in.JobID,
		"tail_lines": 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	content := out.(BashOutputResult).Content
	if strings.Contains(content, "line1") {
		t.Errorf("tail=3 should not include line1: %q", content)
	}
	if !strings.Contains(content, "line5") {
		t.Errorf("tail=3 should include line5: %q", content)
	}
}

func TestBashOutput_UnknownJob(t *testing.T) {
	r := BuiltIn()
	_, err := r.Call(context.Background(), "bash_output", map[string]any{"job_id": "nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown job_id") {
		t.Fatalf("expected unknown-job error, got %v", err)
	}
}

// waitFor polls a file for a substring with a deadline.
func waitFor(t *testing.T, path, needle string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), needle) {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("waiting for %q in %s timed out; current content: %q", needle, path, string(data))
}
