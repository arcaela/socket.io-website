package provider

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Registry holds every provider implementation the binary knows about. Each
// provider package registers itself via `init()` so adding a new backend is
// strictly additive — no central file lists them, and the agent / tools / GUI
// never reference a specific provider by name.
//
// Factory bundles everything the CLI needs to dispatch a provider without
// importing its package directly:
//
//   - New          constructs the Provider (typically requires auth setup).
//   - DefaultModel the model name to use when MINI_MODEL is unset.
//   - Commands     provider-specific subcommands surfaced as `mini provider
//                  <name> <subcommand>` (e.g. Gemini's `auth`).
//
// `New` is intentionally separate from Commands: a provider's `auth`
// subcommand must run BEFORE credentials exist, so we cannot require
// Provider construction to look up its commands.

type Factory struct {
	New          func(ctx context.Context) (Provider, error)
	DefaultModel string
	Commands     map[string]Command
}

// Command is one provider-specific CLI subcommand.
type Command struct {
	Description string
	Run         func(ctx context.Context, args []string) error
}

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register adds a provider. Called from each provider package's init().
// Panics on duplicate registration — that's a programmer error caught at
// program start, not runtime.
func Register(name string, f Factory) {
	if name == "" {
		panic("provider.Register: empty name")
	}
	if f.New == nil {
		panic(fmt.Sprintf("provider.Register(%q): missing New factory", name))
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("provider.Register(%q): already registered", name))
	}
	registry[name] = f
}

// GetFactory returns the factory for the given name, or (zero, false) if
// not registered.
func GetFactory(name string) (Factory, bool) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := registry[name]
	return f, ok
}

// List returns the sorted names of all registered providers.
func List() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// New constructs a Provider by name.
func New(ctx context.Context, name string) (Provider, error) {
	f, ok := GetFactory(name)
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (registered: %v)", name, List())
	}
	return f.New(ctx)
}

// RunCommand looks up the provider and dispatches one of its subcommands.
// Returns a friendly error if the provider or subcommand is not found.
func RunCommand(ctx context.Context, providerName, subcommand string, args []string) error {
	f, ok := GetFactory(providerName)
	if !ok {
		return fmt.Errorf("unknown provider %q (registered: %v)", providerName, List())
	}
	if f.Commands == nil {
		return fmt.Errorf("provider %q exposes no subcommands", providerName)
	}
	cmd, ok := f.Commands[subcommand]
	if !ok {
		names := make([]string, 0, len(f.Commands))
		for n := range f.Commands {
			names = append(names, n)
		}
		sort.Strings(names)
		return fmt.Errorf("unknown subcommand %q for provider %q (available: %v)", subcommand, providerName, names)
	}
	return cmd.Run(ctx, args)
}
