package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/arcaela/mini-cli/provider"
)

func TestRecordUsage_AccumulatesAndRemembersLast(t *testing.T) {
	c := &Chat{}
	c.recordUsage(provider.Usage{PromptTokens: 100, OutputTokens: 10, TotalTokens: 110})
	c.recordUsage(provider.Usage{PromptTokens: 200, OutputTokens: 20, ThoughtTokens: 5, TotalTokens: 225})

	c.totalsMu.Lock()
	defer c.totalsMu.Unlock()
	if c.totals.PromptTokens != 300 || c.totals.OutputTokens != 30 ||
		c.totals.ThoughtTokens != 5 || c.totals.TotalTokens != 335 {
		t.Fatalf("totals not summed: %+v", c.totals)
	}
	if c.lastUsage.PromptTokens != 200 || c.lastUsage.TotalTokens != 225 {
		t.Fatalf("last not the most recent: %+v", c.lastUsage)
	}
}

func TestSlashTokens_FormatsAllSections(t *testing.T) {
	out := &bytes.Buffer{}
	c := &Chat{
		Agent: &Agent{CompactThreshold: 8000},
		out:   out,
	}
	c.recordUsage(provider.Usage{PromptTokens: 6500, OutputTokens: 150, TotalTokens: 6650})

	done, err := c.handleSlash(nil, "/tokens")
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("/tokens should not exit the REPL")
	}
	got := out.String()
	for _, want := range []string{
		"last turn",
		"prompt=6500",
		"session:",
		"compaction:",
		"1500 tokens of headroom", // 8000 - 6500
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestSlashTokens_ArmedWhenAboveThreshold(t *testing.T) {
	out := &bytes.Buffer{}
	c := &Chat{Agent: &Agent{CompactThreshold: 1000}, out: out}
	c.recordUsage(provider.Usage{PromptTokens: 1200, TotalTokens: 1300})
	_, _ = c.handleSlash(nil, "/tokens")
	if !strings.Contains(out.String(), "ARMED") {
		t.Fatalf("expected ARMED status; got:\n%s", out.String())
	}
}

func TestSlashTokens_NoThresholdNoCompactionLine(t *testing.T) {
	out := &bytes.Buffer{}
	c := &Chat{Agent: &Agent{CompactThreshold: 0}, out: out}
	c.recordUsage(provider.Usage{PromptTokens: 500, TotalTokens: 500})
	_, _ = c.handleSlash(nil, "/tokens")
	if strings.Contains(out.String(), "compaction:") {
		t.Fatalf("with no threshold we should NOT show a compaction line:\n%s", out.String())
	}
}

func TestTerminalSink_ForwardsUsageToCallback(t *testing.T) {
	var captured provider.Usage
	s := &terminalSink{
		out:     &bytes.Buffer{},
		tty:     false,
		onUsage: func(u provider.Usage) { captured = u },
	}
	s.OnUsage(provider.Usage{PromptTokens: 42, TotalTokens: 50})
	if captured.PromptTokens != 42 || captured.TotalTokens != 50 {
		t.Fatalf("callback didn't receive usage: %+v", captured)
	}
}
