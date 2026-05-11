// Package tools defines the Tool interface plus a Registry that hosts both
// "base" tools (no dependencies) and "composite" tools (built on top of other
// tools, invoked via the Caller interface — never imported directly).
//
// Adding a new tool: implement Tool, then call Registry.Register or extend
// BuiltIn(). Composites should keep their dependencies named (string) so the
// registry stays the single source of truth.
package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

type Kind int

const (
	KindBase      Kind = iota // No dependencies on other tools.
	KindComposite             // Calls one or more other tools via Caller.
)

func (k Kind) String() string {
	switch k {
	case KindBase:
		return "base"
	case KindComposite:
		return "composite"
	default:
		return "unknown"
	}
}

// Tool is the contract every function (base or composite) must satisfy.
type Tool interface {
	Name() string                 // unique identifier, snake_case
	Description() string          // one-line description for the LLM and humans
	Kind() Kind                   // base or composite
	Schema() map[string]any       // JSON Schema for parameters (subset compatible with Gemini function declarations)
	DependsOn() []string          // list of other tool names this tool calls (composites only); empty for base
	Execute(ctx context.Context, args map[string]any, c Caller) (any, error)
}

// Caller is what composites use to invoke other tools without importing them.
// The Registry implements this interface.
type Caller interface {
	Call(ctx context.Context, name string, args map[string]any) (any, error)
	Has(name string) bool
}

// Registry holds tools by name. Concurrent-safe for reads after registration.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register adds a tool. Re-registering the same name returns an error.
func (r *Registry) Register(t Tool) error {
	if t == nil {
		return fmt.Errorf("nil tool")
	}
	if t.Name() == "" {
		return fmt.Errorf("tool has empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Name()]; exists {
		return fmt.Errorf("tool already registered: %s", t.Name())
	}
	r.tools[t.Name()] = t
	return nil
}

// MustRegister panics on error. Use only at startup with built-in tools.
func (r *Registry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) Has(name string) bool {
	_, ok := r.Get(name)
	return ok
}

// List returns all registered tools, sorted by name.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Call resolves a tool by name and executes it. Composites use this.
func (r *Registry) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	t, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown tool: %q", name)
	}
	if args == nil {
		args = map[string]any{}
	}
	return t.Execute(ctx, args, r)
}

// Validate checks that every composite's declared dependencies actually exist.
// Useful as a startup sanity check.
func (r *Registry) Validate() error {
	for _, t := range r.List() {
		if t.Kind() == KindBase && len(t.DependsOn()) > 0 {
			return fmt.Errorf("base tool %q must not declare dependencies", t.Name())
		}
		for _, dep := range t.DependsOn() {
			if !r.Has(dep) {
				return fmt.Errorf("tool %q depends on unregistered %q", t.Name(), dep)
			}
		}
	}
	return nil
}

// BuiltIn returns a registry pre-populated with the canonical tools:
//   - bash, write           (base)
//   - read, glob, task      (composite)
//   - memory                (third-level: composite-of-composites; uses read + write)
func BuiltIn() *Registry {
	r := NewRegistry()
	// Base
	r.MustRegister(BashTool{})
	r.MustRegister(WriteTool{})
	// Composite
	r.MustRegister(ReadTool{})
	r.MustRegister(GlobTool{})
	r.MustRegister(TaskTool{})
	// Third level (uses other composites)
	r.MustRegister(MemoryTool{})
	if err := r.Validate(); err != nil {
		panic(err)
	}
	return r
}
