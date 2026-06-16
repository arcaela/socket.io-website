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
	"runtime"
	"strings"
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
	_ "github.com/arcaela/mini-cli/provider/anthropic"
	_ "github.com/arcaela/mini-cli/provider/gemini"
	_ "github.com/arcaela/mini-cli/provider/openai"
)

// defaultProviderName is what `mini` selects when MINI_PROVIDER is unset.
// Today we only ship gemini; multi-provider users can override per-invocation.
const defaultProviderName = "gemini"

// Build metadata. Injected at build time via -ldflags "-X main.version=…"
// (see the Makefile / release workflow). The defaults keep `go run` and
// `go install` without ldflags working — they just report "dev".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

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
	case "version", "--version", "-v":
		printVersion()
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
  mini version                      print build version and exit
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

// printVersion reports the build metadata injected at link time plus the Go
// toolchain and target. `mini version`, `--version` and `-v` all route here.
func printVersion() {
	fmt.Printf("mini %s\n", version)
	fmt.Printf("  commit: %s\n", commit)
	fmt.Printf("  built:  %s\n", date)
	fmt.Printf("  go:     %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
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
	// Wire the image-generation tool only when the provider can actually
	// produce images (OpenAI, Gemini). Providers that can't simply won't
	// expose generate_image.
	if ig, ok := prov.(provider.ImageGenerator); ok {
		_ = reg.Register(cli.NewImageGenTool(ig))
	}
	a := &cli.Agent{
		Provider:     prov,
		Tools:        reg,
		Model:        model,
		MaxSteps:     maxSteps,
		MaxParallel:  maxParallel,
		SystemPrompt: defaultSystemPrompt(reg),
		// Auto-compaction (interactive only) fires at 90% of the model's
		// context window. Set MINI_COMPACT_THRESHOLD to override with an
		// explicit token trigger, or MINI_CONTEXT_WINDOW to change the window.
		Compactor:        cli.ProviderCompactor{Provider: prov, Model: model},
		ContextWindow:    cli.ContextWindowFor(model),
		CompactThreshold: envInt("MINI_COMPACT_THRESHOLD", 0),
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
		// One-shot uses the same renderer as the REPL: no TTY niceties, print
		// tokens, and let this function print the returned error (emitErrors=false).
		_, err := a.Run(ctx, nil, prompt, cli.NewStreamSink(os.Stdout, false, true, false, nil))
		return err
	}

	// REPL only: auto-compaction (summarise & restart) is safe here because a
	// human is present; one-shot runs leave Interactive=false and fail loudly.
	a.Interactive = true

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
// MINI_SYSTEM_PROMPT; memory is still appended in either case. The registry
// is passed so the tool roster is rendered from the live set (built-ins plus
// any MCP tools registered at startup).
func defaultSystemPrompt(reg *funcs.Registry) string {
	base := os.Getenv("MINI_SYSTEM_PROMPT")
	if base == "" {
		base = baseSystemPrompt(reg)
	}
	if mem, err := funcs.LoadMemoryContent(); err == nil && mem != "" {
		return base + "\n" + mem + "\n"
	}
	return base
}

func baseSystemPrompt(reg *funcs.Registry) string {
	cwd, _ := os.Getwd()

	// Render the tool list straight from the registry so this prompt can never
	// drift from the tools actually available. Each tool's own Description is
	// written for the model, so we reuse it verbatim.
	var tools strings.Builder
	for _, t := range reg.List() {
		fmt.Fprintf(&tools, "- %-12s %s\n", t.Name(), t.Description())
	}

	return fmt.Sprintf(`You are mini, a focused coding agent that operates through these tools.

TOOLS

%s
RULES

- Be concise. Don't narrate what you're about to do — just call the tool.
- Before editing with write, read the lines you'll touch so your old_block
  anchor is byte-exact (whitespace matters). Pass dry_run=true to preview the
  diff without writing.
- Investigate in parallel: tool calls in a single turn run concurrently
  (default 4 at a time). Batch independent reads/globs.
- Don't invent file paths. Use glob/read to discover them.
- Use background execution only for genuinely long-running processes (servers,
  watchers, big test suites). For short shell tasks, leave it false.
- Use memory sparingly: only for facts that genuinely matter across sessions,
  not running notes for the current task.

ENVIRONMENT

- Date: %s
- Working directory: %s
- OS: linux
`, tools.String(), time.Now().Format("2006-01-02"), cwd)
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
