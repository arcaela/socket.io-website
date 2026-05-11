// Package main is the mini CLI. It contains no provider-specific code:
// providers (gemini, future openai, etc.) register themselves via init() and
// the dispatcher routes by name only.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/arcaela/mini-cli/config"
	"github.com/arcaela/mini-cli/cli"
	"github.com/arcaela/mini-cli/funcs"
	"github.com/arcaela/mini-cli/mcp"
	"github.com/arcaela/mini-cli/provider"

	// Blank-import the providers we ship so their init() runs and they
	// self-register with the provider registry. To add another backend, add
	// its blank import here and that's it.
	_ "github.com/arcaela/mini-cli/provider/gemini"
	_ "github.com/arcaela/mini-cli/provider/openai"
)

// defaultProviderName is what `mini` selects when MINI_PROVIDER is unset.
// Today we only ship gemini; multi-provider users can override per-invocation.
const defaultProviderName = "gemini"

func main() {
	os.Exit(mainImpl())
}

func mainImpl() int {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// One-time relocation from ~/.mini-cli to ~/.mini. No-op after the first
	// successful run. Provider-specific data migrations (e.g. moving Gemini's
	// creds.json into ~/.mini/providers/gemini/) happen inside each provider.
	if err := config.MigrateFromOldDir(); err != nil && os.Getenv("MINI_QUIET") != "1" {
		fmt.Fprintf(os.Stderr, "[mini] migration warning: %v\n", err)
	}

	// Any background process the agent spawned dies when we exit, no matter
	// which subcommand we ran.
	defer func() {
		killed := funcs.ShutdownBackgroundJobs(2 * time.Second)
		if killed > 0 && os.Getenv("MINI_QUIET") != "1" {
			fmt.Fprintf(os.Stderr, "[mini] cleaned up %d background job(s) on exit\n", killed)
		}
	}()

	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}

	var err error
	switch cmd {
	case "", "chat":
		err = runChat(ctx, args)
	case "whoami":
		err = runWhoami(ctx)
	case "tools":
		err = runTools(ctx, args[1:])
	case "provider":
		err = runProvider(ctx, args[1:])
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", cmd)
		printUsage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return 1
	}
	return 0
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `mini — provider-agnostic CLI agent

Usage:
  mini                              start interactive chat
  mini chat ["prompt"]              one-shot if prompt given, else interactive
  mini whoami                       account info from the active provider
  mini tools                        list registered base + composite tools
  mini tools schema <name>          show JSON Schema for a tool
  mini tools call <name> '<json>'   invoke a tool directly (debug)
  mini provider                     list available providers
  mini provider <name>              list that provider's subcommands
  mini provider <name> <sub> [...]  run a provider-specific subcommand

Flags (on 'mini chat'):
  --provider, -p <name>    override MINI_PROVIDER for this run
  --model, -m <name>       override MINI_MODEL for this run
  --yolo                   skip the approval prompt for risky tool calls

Examples:
  mini provider gemini auth                         start Gemini OAuth flow
  mini provider gemini auth-complete "<url-or-code>"  finish Gemini OAuth
  mini chat -p openai -m gpt-5 "Refactor X"          one-shot, openai
  mini chat --yolo                                   REPL, auto-approve

Env (neutral):
  MINI_PROVIDER=<name>     pick the provider (default `+defaultProviderName+`)
  MINI_MODEL=<name>        model id (default comes from the provider)
  MINI_MAX_STEPS=<n>       agent loop iteration cap (default 25)
  MINI_MAX_PARALLEL=<n>    max concurrent tool executions per turn (default 4)
  MINI_SYSTEM_PROMPT=<s>   replace the default system prompt (memory still appended)
  MINI_QUIET=1             suppress retry/cleanup log lines on stderr
  MINI_JOBS_DIR=<path>     override ~/.mini/jobs/
  MINI_MEMORY_DIR=<path>   override ~/.mini/memory/

Each provider may define its own env vars (e.g. GEMINI_RETRY_MAX,
GEMINI_RETRY_MAX_WAIT). See the provider's package for details.`)
}

// ---------------- provider subcommands ----------------

func runProvider(ctx context.Context, args []string) error {
	if len(args) == 0 {
		// `mini provider` — list registered providers.
		names := provider.List()
		if len(names) == 0 {
			fmt.Println("(no providers registered)")
			return nil
		}
		fmt.Println("Available providers:")
		for _, n := range names {
			f, _ := provider.GetFactory(n)
			fmt.Printf("  %-10s  default model: %s\n", n, f.DefaultModel)
		}
		fmt.Println("\nRun `mini provider <name>` to see that provider's subcommands.")
		return nil
	}
	name := args[0]
	f, ok := provider.GetFactory(name)
	if !ok {
		return fmt.Errorf("unknown provider %q (registered: %v)", name, provider.List())
	}
	if len(args) == 1 {
		// `mini provider <name>` — list its subcommands.
		fmt.Printf("Subcommands for provider %q:\n", name)
		if len(f.Commands) == 0 {
			fmt.Println("  (none)")
			return nil
		}
		for sub, c := range f.Commands {
			fmt.Printf("  %-15s  %s\n", sub, c.Description)
		}
		return nil
	}
	return provider.RunCommand(ctx, name, args[1], args[2:])
}

// ---------------- whoami ----------------

func runWhoami(ctx context.Context) error {
	prov, err := buildProvider(ctx)
	if err != nil {
		return err
	}
	acc, err := prov.Account(ctx)
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(acc, "", "  ")
	fmt.Println(string(b))
	return nil
}

// ---------------- tools ----------------

func runTools(ctx context.Context, args []string) error {
	// Load MCP servers so `mini tools` reflects everything an agent run
	// would see. Their lifecycles are bound to this subcommand only.
	mcpClients, _ := mcp.RegisterAll(ctx)
	defer func() {
		for _, c := range mcpClients {
			_ = c.Close()
		}
	}()
	reg := funcs.BuiltIn()
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "", "list":
		fmt.Println("Registered tools:")
		fmt.Println()
		for _, t := range reg.List() {
			deps := ""
			if d := t.DependsOn(); len(d) > 0 {
				deps = "  uses=" + strings.Join(d, ",")
			}
			fmt.Printf("  %-12s [%s]%s\n", t.Name(), t.Kind(), deps)
			fmt.Printf("              %s\n", t.Description())
		}
		return nil
	case "schema":
		if len(args) < 2 {
			return fmt.Errorf("usage: mini tools schema <name>")
		}
		t, ok := reg.Get(args[1])
		if !ok {
			return fmt.Errorf("unknown tool: %s", args[1])
		}
		b, _ := json.MarshalIndent(t.Schema(), "", "  ")
		fmt.Println(string(b))
		return nil
	case "call":
		if len(args) < 2 {
			return fmt.Errorf("usage: mini tools call <name> ['<json-args>']")
		}
		name := args[1]
		var argMap map[string]any
		if len(args) >= 3 {
			raw := strings.Join(args[2:], " ")
			if err := json.Unmarshal([]byte(raw), &argMap); err != nil {
				return fmt.Errorf("invalid JSON args: %w", err)
			}
		}
		out, err := reg.Call(ctx, name, argMap)
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	default:
		return fmt.Errorf("unknown tools subcommand: %s (try: list, schema, call)", sub)
	}
}

// ---------------- chat (one-shot or interactive) ----------------

func runChat(ctx context.Context, args []string) error {
	// Parse `mini chat [--provider X] [--model Y] [--yolo] [prompt...]`.
	// Flags must come before the positional prompt; everything after the
	// last flag is joined as the prompt. Env vars remain the fallback so
	// `MINI_PROVIDER=openai mini chat hi` still works.
	chatArgs, flags, err := parseChatFlags(args)
	if err != nil {
		return err
	}
	prompt := ""
	if len(chatArgs) > 1 {
		prompt = strings.Join(chatArgs[1:], " ")
	}

	providerName := flags.Provider
	if providerName == "" {
		providerName = getEnv("MINI_PROVIDER", defaultProviderName)
	}
	f, ok := provider.GetFactory(providerName)
	if !ok {
		return fmt.Errorf("unknown provider %q (registered: %v)", providerName, provider.List())
	}

	// Load MCP servers (if ~/.mini/mcp.json exists) and register their tools.
	// Failure to start any one server is logged, not fatal.
	mcpClients, _ := mcp.RegisterAll(ctx)
	defer func() {
		for _, c := range mcpClients {
			_ = c.Close()
		}
	}()

	model := flags.Model
	if model == "" {
		model = getEnv("MINI_MODEL", f.DefaultModel)
	}
	maxSteps := envInt("MINI_MAX_STEPS", 25)
	maxParallel := envInt("MINI_MAX_PARALLEL", 4)

	prov, err := f.New(ctx)
	if err != nil {
		return err
	}
	reg := funcs.BuiltIn()
	a := &cli.Agent{
		Provider:     prov,
		Tools:        reg,
		Model:        model,
		MaxSteps:     maxSteps,
		MaxParallel:  maxParallel,
		SystemPrompt: defaultSystemPrompt(),
		// Compact when prompt > 8k tokens (≈ 30% of free-tier flash window).
		// Override with MINI_COMPACT_THRESHOLD=0 to disable, or a different N.
		Compactor:        cli.ProviderCompactor{Provider: prov, Model: model},
		CompactThreshold: envInt("MINI_COMPACT_THRESHOLD", 8000),
	}

	yolo := flags.Yolo || os.Getenv("MINI_YOLO") == "1"

	if prompt != "" {
		// One-shot: --yolo (or env) bypasses approval; otherwise strict.
		if yolo {
			a.Approver = cli.YoloApprover{}
		} else {
			a.Approver = cli.StrictApprover{}
		}
		fmt.Printf("[provider=%s  model=%s  tools=%d]\n", prov.Name(), model, len(reg.List()))
		_, err := a.Run(ctx, nil, prompt, &cliSink{})
		return err
	}

	isTTY := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	// REPL: prompt the user before risky tools, unless --yolo / MINI_YOLO.
	if yolo {
		a.Approver = cli.YoloApprover{}
	} else {
		a.Approver = cli.NewTerminalApprover(os.Stdin, os.Stdout)
	}
	chat := cli.New(a, reg, cli.Options{In: os.Stdin, Out: os.Stdout, IsTTY: isTTY})
	return chat.Run(ctx)
}

// ---------------- system prompt ----------------

// defaultSystemPrompt is sent on every model call. Override with
// MINI_SYSTEM_PROMPT; memory is still appended in either case.
func defaultSystemPrompt() string {
	base := os.Getenv("MINI_SYSTEM_PROMPT")
	if base == "" {
		base = baseSystemPrompt()
	}
	if mem, err := funcs.LoadMemoryContent(); err == nil && mem != "" {
		return base + "\n" + mem + "\n"
	}
	return base
}

func baseSystemPrompt() string {
	cwd, _ := os.Getwd()
	return fmt.Sprintf(`You are mini, a focused coding agent that operates through these funcs.

TOOLS

- bash      Execute a shell command. Foreground (default) blocks up to
            timeout_seconds (default 120). Background=true returns
            immediately with a log_file path; the process keeps running and
            you can read its output later. Always include a one-line
            "description" of what the command does.
- write     Edit a file by replacing one EXACT block of text. Args:
            path, old_block, new_block. The tool fails if old_block is
            missing from the file or appears more than once — when that
            happens, read the file first to get the real content. To
            create a brand-new file, pass empty old_block.
- read      Read line-numbered content from a file. Args: path, start, end,
            max_lines, raw. Default output prefixes every line with its
            1-indexed number ("    42→...") so write anchors line up.
- glob      Find files or directories by pattern. Args: pattern, path,
            type (f|d|a), max_results, include_hidden. Use "**" for
            recursive matching.
- task      Delegate a sub-task to a fresh sub-cli. Args: prompt,
            background. Foreground blocks until the sub-agent finishes and
            returns its full transcript. Background returns a log_file you
            can poll later. Use this to fan out long investigations.
- memory    Save persistent information for future sessions. Args: name,
            content, mode (set|append). Stored under ~/.mini/memory/<name>.md
            and auto-loaded into every future system prompt. Use for stable
            facts about the user, project conventions, or anything you want
            to remember across conversations.

RULES

- Be concise. Don't narrate what you're about to do — just call the tool.
- Before editing, read the lines you'll touch so your old_block anchor is
  byte-exact (whitespace matters).
- Investigate in parallel: tool calls in a single turn run concurrently
  (default 4 at a time). Batch independent reads/globs.
- Don't invent file paths. Use glob/read to discover them.
- Use background only for genuinely long-running processes (servers,
  watchers, big test suites). For short shell tasks, leave it false.
- Use memory sparingly: only for facts that genuinely matter across
  sessions, not running notes for the current task.

ENVIRONMENT

- Date: %s
- Working directory: %s
- OS: linux
`, time.Now().Format("2006-01-02"), cwd)
}

// ---------------- helpers ----------------

func buildProvider(ctx context.Context) (provider.Provider, error) {
	name := getEnv("MINI_PROVIDER", defaultProviderName)
	return provider.New(ctx, name)
}

// chatFlags collects everything `mini chat` accepts as a CLI flag. We
// hand-roll the parser instead of using stdlib `flag` because we want flags
// to interleave with the prompt-as-positional-args without requiring `--`
// separators ("mini chat --model gpt-5 hi there" should Just Work).
type chatFlags struct {
	Provider string
	Model    string
	Yolo     bool
}

// parseChatFlags walks `args` (which still includes the subcommand name as
// args[0]) and pulls recognised flags out. The first non-flag token starts
// the prompt; anything after is treated as prompt text verbatim.
//
// Accepted forms: --flag=value, --flag value, -f value. Unknown flags are
// reported as errors so typos like `--moedel` don't get silently treated as
// the start of the prompt.
func parseChatFlags(args []string) (rest []string, flags chatFlags, err error) {
	rest = append(rest, args[0]) // keep the subcommand name in slot 0

	for i := 1; i < len(args); i++ {
		a := args[i]

		// `--` terminates flags; the rest is prompt.
		if a == "--" {
			rest = append(rest, args[i+1:]...)
			return
		}

		// Once we see a non-flag token, treat it and everything after as prompt.
		if !strings.HasPrefix(a, "-") {
			rest = append(rest, args[i:]...)
			return
		}

		key := a
		var val string
		hasVal := false
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			key = a[:eq]
			val = a[eq+1:]
			hasVal = true
		}

		readVal := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s expects a value", key)
			}
			i++
			return args[i], nil
		}

		switch key {
		case "--provider", "-p":
			v, err := readVal()
			if err != nil {
				return rest, flags, err
			}
			flags.Provider = v
		case "--model", "-m":
			v, err := readVal()
			if err != nil {
				return rest, flags, err
			}
			flags.Model = v
		case "--yolo":
			if hasVal {
				switch strings.ToLower(val) {
				case "1", "true", "yes":
					flags.Yolo = true
				case "0", "false", "no":
					flags.Yolo = false
				default:
					return rest, flags, fmt.Errorf("--yolo: invalid value %q", val)
				}
			} else {
				flags.Yolo = true
			}
		default:
			return rest, flags, fmt.Errorf("unknown flag %q for `mini chat`", key)
		}
	}
	return rest, flags, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n := 0
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
		return def
	}
	return n
}

// cliSink renders agent events for one-shot mode. Tool execution may run
// concurrently, so writes are guarded by a mutex.
type cliSink struct {
	mu          sync.Mutex
	thoughtOpen bool
}

func (*cliSink) OnUserMessage(_ string) {}

func (s *cliSink) OnAssistantThoughtDelta(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.thoughtOpen {
		fmt.Print("┊ thinking… ")
		s.thoughtOpen = true
	}
	fmt.Print(strings.ReplaceAll(t, "\n", " "))
}

func (s *cliSink) OnAssistantTextDelta(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.thoughtOpen {
		fmt.Println()
		s.thoughtOpen = false
	}
	fmt.Print(t)
}

func (s *cliSink) OnAssistantTextFinal(_ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Println()
}

func (s *cliSink) OnToolCall(c provider.ToolCall) {
	args, _ := json.Marshal(c.Args)
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Printf("↳ tool %s %s\n", c.Name, string(args))
}

func (s *cliSink) OnToolResult(r provider.ToolResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Error != "" {
		fmt.Printf("↲ %s error: %s\n", r.Name, r.Error)
		return
	}
	body, _ := json.Marshal(r.Result)
	const max = 600
	if len(body) > max {
		fmt.Printf("↲ %s result (truncated %d→%d): %s…\n", r.Name, len(body), max, string(body[:max]))
	} else {
		fmt.Printf("↲ %s result: %s\n", r.Name, string(body))
	}
}

func (s *cliSink) OnUsage(u provider.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Printf("[tokens: prompt=%d out=%d thoughts=%d total=%d]\n",
		u.PromptTokens, u.OutputTokens, u.ThoughtTokens, u.TotalTokens)
}

func (*cliSink) OnTurnComplete(int) {}
func (*cliSink) OnError(_ error)    {}
