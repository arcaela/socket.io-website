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

func TestMaybeCompact_BelowThresholdIsNoOp(t *testing.T) {
	sc := &stubCompactor{summary: "should not be called"}
	a := &Agent{Compactor: sc, CompactThreshold: 1000}
	history := []provider.Message{
		{Role: provider.RoleUser, Text: "a"},
		{Role: provider.RoleAssistant, Text: "b"},
	}
	out := a.maybeCompact(context.Background(), history, &provider.Usage{PromptTokens: 500}, &recordingSink2{})
	if sc.calls != 0 {
		t.Fatalf("expected Compactor not called, got %d calls", sc.calls)
	}
	if len(out) != 2 {
		t.Fatalf("history should be unchanged, got len %d", len(out))
	}
}

func TestMaybeCompact_DisabledWhenThresholdZero(t *testing.T) {
	sc := &stubCompactor{summary: "x"}
	a := &Agent{Compactor: sc, CompactThreshold: 0}
	history := []provider.Message{
		{Role: provider.RoleUser, Text: "a"},
	}
	out := a.maybeCompact(context.Background(), history, &provider.Usage{PromptTokens: 99999}, &recordingSink2{})
	if sc.calls != 0 || len(out) != 1 {
		t.Fatal("threshold=0 should disable compaction entirely")
	}
}

func TestMaybeCompact_HappyPath(t *testing.T) {
	sc := &stubCompactor{summary: "User likes Go and tea."}
	a := &Agent{Compactor: sc, CompactThreshold: 100, CompactKeepRecent: 2}

	// 8 messages of history, with 6 old and 2 to keep verbatim.
	history := []provider.Message{
		{Role: provider.RoleUser, Text: "msg1"},
		{Role: provider.RoleAssistant, Text: "ans1"},
		{Role: provider.RoleUser, Text: "msg2"},
		{Role: provider.RoleAssistant, Text: "ans2"},
		{Role: provider.RoleUser, Text: "msg3"},
		{Role: provider.RoleAssistant, Text: "ans3"},
		{Role: provider.RoleUser, Text: "msg4 (keep)"},
		{Role: provider.RoleAssistant, Text: "ans4 (keep)"},
	}
	out := a.maybeCompact(context.Background(), history, &provider.Usage{PromptTokens: 500}, &recordingSink2{})

	if sc.calls != 1 {
		t.Fatalf("expected 1 Compact call, got %d", sc.calls)
	}
	if len(sc.seen) != 6 {
		t.Errorf("expected 6 messages forwarded to compactor, got %d", len(sc.seen))
	}
	if len(out) != 3 {
		t.Fatalf("expected 1 system summary + 2 verbatim = 3 messages, got %d", len(out))
	}
	if out[0].Role != provider.RoleSystem || !strings.Contains(out[0].Text, "User likes Go and tea") {
		t.Errorf("first message should be the summary, got %+v", out[0])
	}
	if out[1].Text != "msg4 (keep)" || out[2].Text != "ans4 (keep)" {
		t.Errorf("verbatim tail not preserved: %+v %+v", out[1], out[2])
	}
}

func TestMaybeCompact_CompactorErrorReturnsOriginal(t *testing.T) {
	sc := &stubCompactor{err: errors.New("boom")}
	a := &Agent{Compactor: sc, CompactThreshold: 100, CompactKeepRecent: 2}
	history := []provider.Message{
		{Role: provider.RoleUser, Text: "a"},
		{Role: provider.RoleAssistant, Text: "b"},
		{Role: provider.RoleUser, Text: "c"},
		{Role: provider.RoleAssistant, Text: "d"},
	}
	sink := &recordingSink2{}
	out := a.maybeCompact(context.Background(), history, &provider.Usage{PromptTokens: 999}, sink)
	if len(out) != len(history) {
		t.Fatalf("on error, full history must be preserved")
	}
}

func TestMaybeCompact_EmptySummaryReturnsOriginal(t *testing.T) {
	// If the model returns "" we shouldn't replace anything with a meaningless
	// memo. Bail and let the user /clear or /compact manually.
	sc := &stubCompactor{summary: ""}
	a := &Agent{Compactor: sc, CompactThreshold: 100, CompactKeepRecent: 1}
	history := []provider.Message{
		{Role: provider.RoleUser, Text: "x"},
		{Role: provider.RoleAssistant, Text: "y"},
	}
	out := a.maybeCompact(context.Background(), history, &provider.Usage{PromptTokens: 9999}, &recordingSink2{})
	if len(out) != 2 {
		t.Fatalf("empty summary should bail and keep history, got len %d", len(out))
	}
}

func TestMaybeCompact_TooShortForKeep(t *testing.T) {
	sc := &stubCompactor{summary: "ignored"}
	a := &Agent{Compactor: sc, CompactThreshold: 100, CompactKeepRecent: 10}
	history := []provider.Message{
		{Role: provider.RoleUser, Text: "x"},
	}
	out := a.maybeCompact(context.Background(), history, &provider.Usage{PromptTokens: 9999}, &recordingSink2{})
	if sc.calls != 0 || len(out) != 1 {
		t.Fatal("short history must not invoke compactor")
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
