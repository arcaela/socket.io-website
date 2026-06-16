package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arcaela/mini-cli/funcs"
	"github.com/arcaela/mini-cli/provider"
)

// stubCompactor returns a canned summary, lets tests assert on Compact calls.
type stubCompactor struct {
	summary string
	err     error
	calls   int
	seen    []provider.Message
}

func (s *stubCompactor) Compact(_ context.Context, history []provider.Message) (string, error) {
	s.calls++
	s.seen = append([]provider.Message{}, history...)
	return s.summary, s.err
}

// Auto-compaction (maybeCompact) resets the whole conversation to a single
// summary, and ONLY in interactive mode.

func TestMaybeCompact_NoOpWhenNotInteractive(t *testing.T) {
	sc := &stubCompactor{summary: "should not be called"}
	a := &Agent{Compactor: sc, ContextWindow: 1000, Interactive: false}
	history := []provider.Message{
		{Role: provider.RoleUser, Text: "a"},
		{Role: provider.RoleAssistant, Text: "b"},
	}
	out := a.maybeCompact(context.Background(), history, &provider.Usage{PromptTokens: 999}, &recordingSink2{})
	if sc.calls != 0 || len(out) != 2 {
		t.Fatal("one-shot (non-interactive) runs must never auto-compact")
	}
}

func TestMaybeCompact_BelowTriggerIsNoOp(t *testing.T) {
	sc := &stubCompactor{summary: "should not be called"}
	a := &Agent{Compactor: sc, CompactThreshold: 1000, Interactive: true}
	history := []provider.Message{
		{Role: provider.RoleUser, Text: "a"},
		{Role: provider.RoleAssistant, Text: "b"},
	}
	out := a.maybeCompact(context.Background(), history, &provider.Usage{PromptTokens: 500}, &recordingSink2{})
	if sc.calls != 0 || len(out) != 2 {
		t.Fatalf("below trigger should be a no-op, got %d calls / len %d", sc.calls, len(out))
	}
}

func TestMaybeCompact_DisabledWhenNoTrigger(t *testing.T) {
	sc := &stubCompactor{summary: "x"}
	a := &Agent{Compactor: sc, Interactive: true} // window=0 and threshold=0 → never triggers
	history := []provider.Message{{Role: provider.RoleUser, Text: "a"}, {Role: provider.RoleAssistant, Text: "b"}}
	out := a.maybeCompact(context.Background(), history, &provider.Usage{PromptTokens: 99999}, &recordingSink2{})
	if sc.calls != 0 || len(out) != 2 {
		t.Fatal("no context window and no threshold should disable compaction")
	}
}

func TestMaybeCompact_ResetsToSummary(t *testing.T) {
	sc := &stubCompactor{summary: "User likes Go and tea."}
	// 90% of window=1000 is 900; prompt 950 → triggers.
	a := &Agent{Compactor: sc, ContextWindow: 1000, Interactive: true}
	history := []provider.Message{
		{Role: provider.RoleUser, Text: "msg1"},
		{Role: provider.RoleAssistant, Text: "ans1"},
		{Role: provider.RoleUser, Text: "msg2"},
		{Role: provider.RoleAssistant, Text: "ans2"},
	}
	out := a.maybeCompact(context.Background(), history, &provider.Usage{PromptTokens: 950}, &recordingSink2{})

	if sc.calls != 1 || len(sc.seen) != 4 {
		t.Fatalf("expected the whole history summarised once, got %d calls / %d seen", sc.calls, len(sc.seen))
	}
	if len(out) != 1 || out[0].Role != provider.RoleSystem {
		t.Fatalf("expected a single system summary, got %+v", out)
	}
	if !strings.Contains(out[0].Text, "User likes Go and tea") {
		t.Errorf("summary text missing: %+v", out[0])
	}
}

func TestMaybeCompact_CompactorErrorReturnsOriginal(t *testing.T) {
	sc := &stubCompactor{err: errors.New("boom")}
	a := &Agent{Compactor: sc, ContextWindow: 1000, Interactive: true}
	history := []provider.Message{
		{Role: provider.RoleUser, Text: "a"},
		{Role: provider.RoleAssistant, Text: "b"},
	}
	out := a.maybeCompact(context.Background(), history, &provider.Usage{PromptTokens: 999}, &recordingSink2{})
	if len(out) != len(history) {
		t.Fatalf("on error, full history must be preserved")
	}
}

func TestMaybeCompact_EmptySummaryReturnsOriginal(t *testing.T) {
	sc := &stubCompactor{summary: ""}
	a := &Agent{Compactor: sc, ContextWindow: 1000, Interactive: true}
	history := []provider.Message{
		{Role: provider.RoleUser, Text: "x"},
		{Role: provider.RoleAssistant, Text: "y"},
	}
	out := a.maybeCompact(context.Background(), history, &provider.Usage{PromptTokens: 9999}, &recordingSink2{})
	if len(out) != 2 {
		t.Fatalf("empty summary should bail and keep history, got len %d", len(out))
	}
}

// keepTailAfterSummary (the manual /compact path) summarises the old prefix and
// keeps a verbatim tail, snapping the split to a clean boundary so a
// tool_result is never orphaned from its tool_call.
func TestKeepTailAfterSummary_SnapsBoundaryToAvoidOrphan(t *testing.T) {
	sc := &stubCompactor{summary: "summary"}
	history := []provider.Message{
		{Role: provider.RoleUser, Text: "first"},
		{Role: provider.RoleAssistant, Text: "answer one"},
		{Role: provider.RoleUser, Text: "second"},
		{Role: provider.RoleAssistant, ToolCall: &provider.ToolCall{ID: "1", Name: "bash"}},
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "1", Name: "bash", Result: "ok"}},
		{Role: provider.RoleAssistant, Text: "done"},
	}
	out, err := keepTailAfterSummary(context.Background(), sc, history, 2)
	if err != nil {
		t.Fatal(err)
	}
	// keep=2 would split at index 4 (the tool_result); snap back to "second"
	// (index 2): 2 summarised, 4 kept.
	if len(sc.seen) != 2 || len(out) != 5 || out[0].Role != provider.RoleSystem {
		t.Fatalf("unexpected split: %d seen, out len %d", len(sc.seen), len(out))
	}
	for i, m := range out {
		if m.ToolResult != nil && (i == 0 || out[i-1].ToolCall == nil) {
			t.Fatalf("orphaned tool_result at index %d", i)
		}
	}
}

func TestContextWindowFor(t *testing.T) {
	cases := map[string]int{
		"gemini-2.5-flash":  1_000_000,
		"claude-sonnet-4-5": 200_000,
		"gpt-5":             400_000,
		"something-unknown": 128_000,
	}
	for model, want := range cases {
		if got := ContextWindowFor(model); got != want {
			t.Errorf("ContextWindowFor(%q) = %d, want %d", model, got, want)
		}
	}
}

func TestSafeCompactBoundary_NoCleanBoundaryReturnsZero(t *testing.T) {
	history := []provider.Message{
		{Role: provider.RoleUser, Text: "go"},
		{Role: provider.RoleAssistant, ToolCall: &provider.ToolCall{ID: "1", Name: "bash"}},
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "1", Name: "bash"}},
	}
	// keep=1 starts at the tool_result; snapping back passes the tool_call and
	// lands on the user turn (index 0) → returns 0, signalling "skip".
	if got := safeCompactBoundary(history, 1); got != 0 {
		t.Fatalf("expected boundary 0 (skip), got %d", got)
	}
}

// =============================================================================
// ProviderCompactor uses the provider end-to-end. Stub the provider so we can
// assert on what gets sent and on the assembled summary.
// =============================================================================

func TestProviderCompactor_RendersTranscriptAndJoinsTextDeltas(t *testing.T) {
	prov := &scriptedProvider{
		turns: [][]provider.Event{
			{
				{Kind: provider.EventTextDelta, Text: "First half. "},
				{Kind: provider.EventTextDelta, Text: "Second half."},
				{Kind: provider.EventTurnDone},
			},
		},
	}
	c := ProviderCompactor{Provider: prov, Model: "fake"}
	summary, err := c.Compact(context.Background(), []provider.Message{
		{Role: provider.RoleUser, Text: "hello"},
		{Role: provider.RoleAssistant, Text: "hi back"},
		{Role: provider.RoleAssistant, ToolCall: &provider.ToolCall{Name: "bash"}},
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{Name: "bash", Result: "stdout x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary != "First half. Second half." {
		t.Fatalf("summary not joined: %q", summary)
	}
	// Provider should have been called with NO tools (we want text only).
	if prov.n != 1 {
		t.Fatalf("expected 1 provider call, got %d", prov.n)
	}
}

func TestProviderCompactor_PropagatesError(t *testing.T) {
	prov := &scriptedProvider{
		turns: [][]provider.Event{
			{{Kind: provider.EventError, Err: errors.New("rate-limited")}},
		},
	}
	c := ProviderCompactor{Provider: prov, Model: "fake"}
	_, err := c.Compact(context.Background(), []provider.Message{{Role: provider.RoleUser, Text: "x"}})
	if err == nil || err.Error() != "rate-limited" {
		t.Fatalf("expected propagated error, got %v", err)
	}
}

func TestRenderTranscript_Shape(t *testing.T) {
	out := renderTranscript([]provider.Message{
		{Role: provider.RoleUser, Text: "hello"},
		{Role: provider.RoleAssistant, Text: "hi"},
		{Role: provider.RoleAssistant, ToolCall: &provider.ToolCall{Name: "bash"}},
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{Name: "bash", Result: "x"}},
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{Name: "bash", Error: "perm denied"}},
	})
	for _, want := range []string{"[user] hello", "[assistant] hi", "[tool_call bash]", "[tool_result bash] x", "error: perm denied"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// Sanity import to keep funcs reachable in tests of cli package.
var _ = funcs.RiskLow
