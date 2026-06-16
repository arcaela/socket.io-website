// Package gui is the interactive terminal chat. It is the only package that
// renders to the user; the agent and provider know nothing about TTYs.
//
// Layout per turn (TTY):
//
//   › <user prompt>
//   ┊ thinking… (shown only if the model emits thoughts)
//   <assistant streaming text>
//   ↳ tool: <name> {"arg":"value"}
//   ↲ result: {…}
//   <assistant continues, possibly more tools…>
//   [tokens: prompt=N out=N thoughts=N total=N]
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/arcaela/mini-cli/provider"
	"github.com/arcaela/mini-cli/funcs"
)

type Options struct {
	In    io.Reader
	Out   io.Writer
	IsTTY bool
}

type Chat struct {
	Agent   *Agent
	Tools   *funcs.Registry
	History []provider.Message
	out     io.Writer
	in      io.Reader
	tty     bool

	// Cumulative token usage across the session, updated from each turn's
	// provider.Usage. Exposed via /tokens.
	totalsMu  sync.Mutex
	totals    provider.Usage
	lastUsage provider.Usage
}

func New(a *Agent, reg *funcs.Registry, opts Options) *Chat {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}
	return &Chat{
		Agent: a,
		Tools: reg,
		out:   opts.Out,
		in:    opts.In,
		tty:   opts.IsTTY,
	}
}

func (c *Chat) Run(ctx context.Context) error {
	if c.tty {
		fmt.Fprintf(c.out, "mini-cli  provider=%s  model=%s  tools=%d\n",
			c.Agent.Provider.Name(), c.Agent.Model, len(c.Tools.List()))
		fmt.Fprintln(c.out, "Type a message and press Enter. Slash commands: /help, /tools, /whoami, /clear, /history, /quit.")
	}

	scanner := bufio.NewScanner(c.in)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for {
		c.printPrompt()
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return err
			}
			if c.tty {
				fmt.Fprintln(c.out)
			}
			return nil
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			done, err := c.handleSlash(ctx, line)
			if err != nil {
				fmt.Fprintln(c.out, "error:", err)
				continue
			}
			if done {
				return nil
			}
			continue
		}

		// Live sink writes events directly to c.out AND threads usage
		// back into the Chat so /tokens has the full picture.
		sink := NewStreamSink(c.out, c.tty, c.tty, true, c.recordUsage)
		newHistory, err := c.Agent.Run(ctx, c.History, line, sink)
		if err != nil && !isCancelled(err) {
			fmt.Fprintln(c.out, "✗", err)
			// keep history including failed turn so /history reflects reality
		}
		c.History = newHistory
	}
}

func (c *Chat) printPrompt() {
	if c.tty {
		fmt.Fprint(c.out, "\n› ")
	}
}

// recordUsage is the terminalSink → Chat callback. Accumulates cumulative
// totals (used by /tokens) and remembers the last turn's numbers so the
// compaction-distance hint stays accurate even after several turns.
func (c *Chat) recordUsage(u provider.Usage) {
	c.totalsMu.Lock()
	defer c.totalsMu.Unlock()
	c.lastUsage = u
	c.totals.PromptTokens += u.PromptTokens
	c.totals.OutputTokens += u.OutputTokens
	c.totals.ThoughtTokens += u.ThoughtTokens
	c.totals.TotalTokens += u.TotalTokens
}

func (c *Chat) handleSlash(ctx context.Context, line string) (done bool, err error) {
	fields := strings.Fields(line)
	cmd := fields[0]
	args := fields[1:]

	switch cmd {
	case "/quit", "/exit", "/q":
		return true, nil
	case "/help", "/?":
		fmt.Fprintln(c.out, "Slash commands:")
		fmt.Fprintln(c.out, "  /help            this help")
		fmt.Fprintln(c.out, "  /whoami          provider account info")
		fmt.Fprintln(c.out, "  /tools           list registered base + composite tools")
		fmt.Fprintln(c.out, "  /jobs            list background jobs (started via bash_input)")
		fmt.Fprintln(c.out, "  /model [<name>]  show or change model")
		fmt.Fprintln(c.out, "  /yolo [on|off]   toggle skipping the approval prompt for risky tools")
		fmt.Fprintln(c.out, "  /compact         summarise older history into a single memo (frees tokens)")
		fmt.Fprintln(c.out, "  /tokens          last-turn + cumulative token usage; compaction headroom")
		fmt.Fprintln(c.out, "  /save [name]     persist this session to ~/.mini/sessions/<id>.jsonl")
		fmt.Fprintln(c.out, "  /load <id>       replace history with a saved session (prefix match OK)")
		fmt.Fprintln(c.out, "  /sessions        list saved sessions")
		fmt.Fprintln(c.out, "  /clear           reset conversation history")
		fmt.Fprintln(c.out, "  /history         dump current conversation")
		fmt.Fprintln(c.out, "  /quit            exit")
		return false, nil
	case "/yolo":
		mode := "on"
		if len(args) > 0 {
			mode = strings.ToLower(args[0])
		}
		switch mode {
		case "on", "true", "1":
			c.Agent.Approver = YoloApprover{}
			fmt.Fprintln(c.out, "yolo on — risky tools will NOT be prompted")
		case "off", "false", "0":
			c.Agent.Approver = NewTerminalApprover(c.in, c.out)
			fmt.Fprintln(c.out, "yolo off — risky tools will prompt for approval")
		default:
			fmt.Fprintln(c.out, "usage: /yolo [on|off]")
		}
		return false, nil
	case "/whoami":
		acc, err := c.Agent.Provider.Account(ctx)
		if err != nil {
			return false, err
		}
		fmt.Fprintf(c.out, "provider=%s  email=%s  tier=%s  project=%s  model=%s\n",
			acc.Provider, acc.Email, acc.Tier, acc.Project, c.Agent.Model)
		return false, nil
	case "/tools":
		for _, t := range c.Tools.List() {
			deps := ""
			if d := t.DependsOn(); len(d) > 0 {
				deps = "  uses=" + strings.Join(d, ",")
			}
			fmt.Fprintf(c.out, "  %-12s [%s]%s\n", t.Name(), t.Kind(), deps)
		}
		return false, nil
	case "/jobs":
		jobs := funcs.ListJobs()
		if len(jobs) == 0 {
			fmt.Fprintln(c.out, "(no background jobs)")
			return false, nil
		}
		// Stable order (by job_id which encodes timestamp).
		sort.Slice(jobs, func(i, j int) bool { return jobs[i].JobID < jobs[j].JobID })
		for _, j := range jobs {
			status := "exited"
			if j.Running {
				status = "running"
			}
			fmt.Fprintf(c.out, "  %s  pid=%-6d  %s  %s\n", j.JobID, j.PID, status, j.LogFile)
		}
		return false, nil
	case "/model":
		if len(args) == 0 {
			fmt.Fprintln(c.out, "model:", c.Agent.Model)
		} else {
			c.Agent.Model = args[0]
			fmt.Fprintln(c.out, "model set to:", c.Agent.Model)
		}
		return false, nil
	case "/clear":
		c.History = nil
		fmt.Fprintln(c.out, "conversation cleared")
		return false, nil
	case "/compact":
		// Force a compaction right now, even below threshold. Useful when the
		// user knows the next turn will be expensive.
		if c.Agent.Compactor == nil {
			fmt.Fprintln(c.out, "no compactor configured")
			return false, nil
		}
		before := len(c.History)
		keep := c.Agent.CompactKeepRecent
		if keep <= 0 {
			keep = defaultCompactKeep
		}
		fmt.Fprint(c.out, "summarising… ")
		newHist, err := keepTailAfterSummary(ctx, c.Agent.Compactor, c.History, keep)
		if err != nil {
			fmt.Fprintln(c.out, "failed:", err)
			return false, nil
		}
		if len(newHist) == before {
			fmt.Fprintln(c.out, "history too short to compact")
			return false, nil
		}
		c.History = newHist
		fmt.Fprintf(c.out, "done. history: %d → %d messages\n", before, len(c.History))
		return false, nil
	case "/save":
		name := strings.Join(args, " ")
		path, err := SaveSession(name, c.History)
		if err != nil {
			return false, err
		}
		fmt.Fprintf(c.out, "saved %d message(s) to %s\n", len(c.History), path)
		return false, nil
	case "/load":
		if len(args) == 0 {
			return false, fmt.Errorf("usage: /load <id>")
		}
		hist, err := LoadSession(args[0])
		if err != nil {
			return false, err
		}
		c.History = hist
		// Reset cumulative usage — old totals belonged to a different run.
		c.totalsMu.Lock()
		c.totals = provider.Usage{}
		c.lastUsage = provider.Usage{}
		c.totalsMu.Unlock()
		fmt.Fprintf(c.out, "loaded %d message(s) from %q\n", len(hist), args[0])
		return false, nil
	case "/sessions":
		ss, err := ListSessions()
		if err != nil {
			return false, err
		}
		if len(ss) == 0 {
			fmt.Fprintln(c.out, "(no saved sessions)")
			return false, nil
		}
		for _, s := range ss {
			fmt.Fprintf(c.out, "  %-44s  %s  msgs=%d  %s\n",
				s.ID, s.SavedAt.Local().Format("2006-01-02 15:04"), s.Messages, byteSize(s.Size))
		}
		return false, nil
	case "/tokens":
		c.totalsMu.Lock()
		t := c.totals
		last := c.lastUsage
		c.totalsMu.Unlock()
		fmt.Fprintf(c.out, "last turn:  prompt=%d  out=%d  thoughts=%d  total=%d\n",
			last.PromptTokens, last.OutputTokens, last.ThoughtTokens, last.TotalTokens)
		fmt.Fprintf(c.out, "session:    prompt=%d  out=%d  thoughts=%d  total=%d\n",
			t.PromptTokens, t.OutputTokens, t.ThoughtTokens, t.TotalTokens)
		if trigger := c.Agent.compactTriggerTokens(); trigger > 0 {
			delta := trigger - last.PromptTokens
			switch {
			case last.PromptTokens == 0:
				fmt.Fprintf(c.out, "compaction: triggers at prompt=%d tokens\n", trigger)
			case delta <= 0:
				fmt.Fprintf(c.out, "compaction: ARMED — next turn will summarise (threshold %d, last prompt %d)\n",
					trigger, last.PromptTokens)
			default:
				fmt.Fprintf(c.out, "compaction: %d tokens of headroom (threshold %d)\n",
					delta, trigger)
			}
		}
		return false, nil
	case "/history":
		for i, m := range c.History {
			summary := summarizeMessage(m)
			fmt.Fprintf(c.out, "[%d] %s: %s\n", i, m.Role, summary)
		}
		return false, nil
	default:
		return false, fmt.Errorf("unknown command: %s (try /help)", cmd)
	}
}

// ----- terminalSink: Sink that writes a clean transcript -----
//
// Tool execution runs in parallel goroutines, so OnToolCall / OnToolResult
// can be invoked concurrently. We serialize all writes with a mutex to keep
// the transcript readable (otherwise byte-level interleaving corrupts lines).

type terminalSink struct {
	out         io.Writer
	tty         bool // REPL-only niceties (e.g. the step-count line)
	showTokens  bool // print the per-turn [tokens: …] line
	emitErrors  bool // print the ✗ error line (off in one-shot; main prints it)
	mu          sync.Mutex
	thoughtOpen bool

	// onUsage is invoked once per turn with the usage emitted by the
	// provider. Set by the Chat that owns this sink so /tokens can show
	// session-wide totals.
	onUsage func(provider.Usage)
}

// NewStreamSink renders agent events to out. It is the single sink used by both
// the REPL and one-shot mode. tty toggles REPL-only niceties, showTokens prints
// per-turn usage, and emitErrors prints the ✗ line (one-shot leaves it off and
// lets main print the returned error once).
func NewStreamSink(out io.Writer, tty, showTokens, emitErrors bool, onUsage func(provider.Usage)) Sink {
	return &terminalSink{out: out, tty: tty, showTokens: showTokens, emitErrors: emitErrors, onUsage: onUsage}
}

func (s *terminalSink) OnUserMessage(_ string) {}

func (s *terminalSink) OnAssistantThoughtDelta(delta string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.thoughtOpen {
		fmt.Fprint(s.out, "\n┊ thinking… ")
		s.thoughtOpen = true
	}
	fmt.Fprint(s.out, strings.ReplaceAll(delta, "\n", " "))
}

func (s *terminalSink) OnAssistantTextDelta(delta string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.thoughtOpen {
		fmt.Fprintln(s.out)
		s.thoughtOpen = false
	}
	fmt.Fprint(s.out, delta)
}

func (s *terminalSink) OnAssistantTextFinal(_ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintln(s.out)
}

func (s *terminalSink) OnToolCall(call provider.ToolCall) {
	args, _ := json.Marshal(call.Args)
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.out, "↳ tool %s %s\n", call.Name, string(args))
}

func (s *terminalSink) OnToolResult(r provider.ToolResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Error != "" {
		fmt.Fprintf(s.out, "↲ %s error: %s\n", r.Name, r.Error)
		return
	}
	body, _ := json.Marshal(r.Result)
	const max = 600
	if len(body) > max {
		fmt.Fprintf(s.out, "↲ %s result (truncated %d→%d): %s…\n", r.Name, len(body), max, string(body[:max]))
	} else {
		fmt.Fprintf(s.out, "↲ %s result: %s\n", r.Name, string(body))
	}
}

func (s *terminalSink) OnUsage(u provider.Usage) {
	// Always forward usage to the owning chat so /tokens stays accurate,
	// even outside a TTY (one-shot mode etc. — though we mostly run in tty).
	if s.onUsage != nil {
		s.onUsage(u)
	}
	if !s.showTokens {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.out, "[tokens: prompt=%d  out=%d  thoughts=%d  total=%d]\n",
		u.PromptTokens, u.OutputTokens, u.ThoughtTokens, u.TotalTokens)
}

func (s *terminalSink) OnTurnComplete(steps int) {
	if s.tty && steps > 1 {
		s.mu.Lock()
		defer s.mu.Unlock()
		fmt.Fprintf(s.out, "[turn took %d steps]\n", steps)
	}
}

func (s *terminalSink) OnError(err error) {
	if !s.emitErrors {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.out, "\n✗ %v\n", err)
}

// ----- helpers -----

func summarizeMessage(m provider.Message) string {
	switch {
	case m.ToolCall != nil:
		args, _ := json.Marshal(m.ToolCall.Args)
		return "tool_call " + m.ToolCall.Name + " " + truncate(string(args), 120)
	case m.ToolResult != nil:
		body, _ := json.Marshal(m.ToolResult.Result)
		if m.ToolResult.Error != "" {
			return "tool_result " + m.ToolResult.Name + " ERROR: " + truncate(m.ToolResult.Error, 120)
		}
		return "tool_result " + m.ToolResult.Name + " " + truncate(string(body), 120)
	default:
		return truncate(m.Text, 200)
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func byteSize(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%dB", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	}
}

func isCancelled(err error) bool {
	return err != nil && (err == context.Canceled || strings.Contains(err.Error(), "context canceled"))
}
