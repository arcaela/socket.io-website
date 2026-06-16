package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/arcaela/mini-cli/provider"
)

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Refactor Plan!", "refactor-plan"},
		{"   spaces   ", "spaces"},
		{"a/b/c", "a-b-c"},
		{"---", "session"},
		{"", "session"},
		// length cap
		{strings.Repeat("x", 100), strings.Repeat("x", 48)},
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestSaveAndLoad_Roundtrip(t *testing.T) {
	t.Setenv("MINI_SESSIONS_DIR", t.TempDir())
	history := []provider.Message{
		{Role: provider.RoleSystem, Text: "be brief"},
		{Role: provider.RoleUser, Text: "hi"},
		{Role: provider.RoleAssistant, Text: "hello"},
		{Role: provider.RoleAssistant, ToolCall: &provider.ToolCall{
			ID: "c1", Name: "bash", Args: map[string]any{"command": "ls"},
		}},
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{
			CallID: "c1", Name: "bash", Result: "ok",
		}},
	}
	path, err := SaveSession("my plan", history)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "my-plan") {
		t.Errorf("slug not in path: %q", path)
	}

	got, err := LoadSession(filenameOf(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(history) {
		t.Fatalf("len mismatch: %d vs %d", len(got), len(history))
	}
	if got[3].ToolCall == nil || got[3].ToolCall.Name != "bash" {
		t.Errorf("tool_call not roundtripped: %+v", got[3])
	}
	if got[4].ToolResult == nil || got[4].ToolResult.CallID != "c1" {
		t.Errorf("tool_result not roundtripped: %+v", got[4])
	}
}

func TestLoadSession_PrefixMatch(t *testing.T) {
	t.Setenv("MINI_SESSIONS_DIR", t.TempDir())
	_, err := SaveSession("alpha", []provider.Message{{Role: provider.RoleUser, Text: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	// Just the timestamp prefix (first 8 chars: YYYYMMDD) should match uniquely.
	hist, err := LoadSession("20")
	if err != nil {
		t.Fatalf("prefix load failed: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("expected 1 msg, got %d", len(hist))
	}
}

func TestLoadSession_NoMatch(t *testing.T) {
	t.Setenv("MINI_SESSIONS_DIR", t.TempDir())
	_, err := LoadSession("zzz-no-such")
	if err == nil || !strings.Contains(err.Error(), "no session matches") {
		t.Fatalf("expected no-match error, got %v", err)
	}
}

func TestListSessions_OrderAndCounts(t *testing.T) {
	t.Setenv("MINI_SESSIONS_DIR", t.TempDir())
	_, _ = SaveSession("first", []provider.Message{{Role: provider.RoleUser, Text: "a"}})
	_, _ = SaveSession("second", []provider.Message{
		{Role: provider.RoleUser, Text: "b"},
		{Role: provider.RoleAssistant, Text: "c"},
	})
	ss, err := ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(ss))
	}
	if !ss[0].SavedAt.After(ss[1].SavedAt) && !ss[0].SavedAt.Equal(ss[1].SavedAt) {
		t.Fatalf("listing not newest-first: %+v", ss)
	}
	// Newest is the 2-message session.
	if ss[0].Messages != 2 || ss[1].Messages != 1 {
		t.Errorf("message counts wrong: %+v", ss)
	}
}

func TestSlashSave_PersistsCurrentHistory(t *testing.T) {
	t.Setenv("MINI_SESSIONS_DIR", t.TempDir())
	out := &bytes.Buffer{}
	c := &Chat{
		Agent:   &Agent{},
		out:     out,
		History: []provider.Message{{Role: provider.RoleUser, Text: "x"}},
	}
	if _, err := c.handleSlash(nil, "/save test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "saved 1 message") {
		t.Fatalf("save message wrong: %s", out.String())
	}
	ss, err := ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 {
		t.Fatalf("expected 1 saved session, got %d", len(ss))
	}
}

func TestSlashLoad_ResetsHistoryAndUsage(t *testing.T) {
	t.Setenv("MINI_SESSIONS_DIR", t.TempDir())
	saved := []provider.Message{
		{Role: provider.RoleUser, Text: "loaded-1"},
		{Role: provider.RoleAssistant, Text: "loaded-2"},
	}
	path, err := SaveSession("seed", saved)
	if err != nil {
		t.Fatal(err)
	}

	c := &Chat{
		Agent:   &Agent{},
		out:     &bytes.Buffer{},
		History: []provider.Message{{Role: provider.RoleUser, Text: "stale"}},
	}
	c.recordUsage(provider.Usage{PromptTokens: 999, TotalTokens: 999})

	if _, err := c.handleSlash(nil, "/load "+filenameOf(path)); err != nil {
		t.Fatal(err)
	}
	if len(c.History) != 2 || c.History[0].Text != "loaded-1" {
		t.Fatalf("history not replaced: %+v", c.History)
	}
	c.totalsMu.Lock()
	defer c.totalsMu.Unlock()
	if c.totals.TotalTokens != 0 || c.lastUsage.TotalTokens != 0 {
		t.Errorf("usage not reset after /load: totals=%+v last=%+v", c.totals, c.lastUsage)
	}
}

// filenameOf strips the directory + .jsonl extension so tests can pass the
// bare id to LoadSession the way users do.
func filenameOf(path string) string {
	parts := strings.Split(path, "/")
	return strings.TrimSuffix(parts[len(parts)-1], ".jsonl")
}
