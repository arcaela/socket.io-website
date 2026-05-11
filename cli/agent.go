// Package agent runs the iterative loop: ask provider → handle tool calls →
// feed results back → repeat until the model emits text-only or we hit max
// steps. It is provider-agnostic; the only LLM-specific code lives in
// internal/provider/<vendor>.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/arcaela/mini-cli/provider"
	"github.com/arcaela/mini-cli/funcs"
)

// Sink is how the agent reports progress to whoever started it (the GUI in
// production, a recording sink in tests). Every method runs synchronously on
// the agent's goroutine.
type Sink interface {
	OnUserMessage(text string)
	OnAssistantTextDelta(delta string)
	OnAssistantThoughtDelta(delta string)
	OnAssistantTextFinal(text string) // called once the assistant turn is fully accumulated
	OnToolCall(call provider.ToolCall)
	OnToolResult(result provider.ToolResult)
	OnUsage(usage provider.Usage)
	OnTurnComplete(steps int)
	OnError(err error)
}

type Agent struct {
	Provider    provider.Provider
	Tools       *funcs.Registry
	Model       string
	MaxSteps    int
	MaxParallel int    // 0 = default (4); set to 1 for serial execution
	SystemPrompt string // optional; injected as a RoleSystem message at every Generate call (not stored in history)
}

const (
	defaultMaxSteps    = 25
	defaultMaxParallel = 4
)

// Run sends `userPrompt` to the model, executes any tool calls it requests,
// and keeps iterating until the model returns text-only (or limit hit).
// Returns the updated history (caller decides whether to keep it).
//
// `history` is the conversation up to and NOT including this turn's user
// message. The user message is appended internally.
func (a *Agent) Run(
	ctx context.Context,
	history []provider.Message,
	userPrompt string,
	sink Sink,
) ([]provider.Message, error) {
	if sink == nil {
		sink = noopSink{}
	}
	maxSteps := a.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}

	// Append the user turn.
	history = append(history, provider.Message{Role: provider.RoleUser, Text: userPrompt})
	sink.OnUserMessage(userPrompt)

	// Translate the registered tools into provider.ToolDecl once.
	decls := buildToolDecls(a.Tools)

	for step := 0; step < maxSteps; step++ {
		// Inject the system prompt at the head of every request. We do not
		// store it in `history` because (a) it doesn't change between turns
		// and (b) including it in history would pollute /history output.
		msgs := history
		if a.SystemPrompt != "" {
			msgs = append([]provider.Message{
				{Role: provider.RoleSystem, Text: a.SystemPrompt},
			}, history...)
		}
		req := provider.GenerateRequest{
			Model:    a.Model,
			Messages: msgs,
			Tools:    decls,
		}
		events, err := a.Provider.Generate(ctx, req)
		if err != nil {
			sink.OnError(err)
			return history, err
		}

		var assistantText string
		toolCalls := []provider.ToolCall{}
		var lastUsage *provider.Usage

	streamLoop:
		for {
			select {
			case <-ctx.Done():
				return history, ctx.Err()
			case ev, ok := <-events:
				if !ok {
					break streamLoop
				}
				switch ev.Kind {
				case provider.EventTextDelta:
					assistantText += ev.Text
					sink.OnAssistantTextDelta(ev.Text)
				case provider.EventThoughtDelta:
					sink.OnAssistantThoughtDelta(ev.Text)
				case provider.EventToolCallRequest:
					if ev.ToolCall != nil {
						toolCalls = append(toolCalls, *ev.ToolCall)
					}
				case provider.EventTurnDone:
					if ev.Usage != nil {
						lastUsage = ev.Usage
					}
				case provider.EventError:
					sink.OnError(ev.Err)
					return history, ev.Err
				}
			}
		}

		// Persist assistant content into history before deciding next step.
		if assistantText != "" {
			history = append(history, provider.Message{Role: provider.RoleAssistant, Text: assistantText})
			sink.OnAssistantTextFinal(assistantText)
		}
		for i := range toolCalls {
			tc := toolCalls[i]
			history = append(history, provider.Message{Role: provider.RoleAssistant, ToolCall: &tc})
		}
		if lastUsage != nil {
			sink.OnUsage(*lastUsage)
		}

		// Termination: no tool calls means the assistant produced its final reply.
		if len(toolCalls) == 0 {
			sink.OnTurnComplete(step + 1)
			return history, nil
		}

		// Execute tool calls in parallel. Sink methods may be called from any
		// goroutine — implementations must be thread-safe (terminalSink and
		// cliSink use a mutex for this reason).
		results := a.executeToolsParallel(ctx, toolCalls, sink)

		// Append results to history in stable order matching the original
		// tool calls. Order matters because the model relies on positional
		// correspondence between tool_call → tool_result.
		for i := range results {
			r := results[i]
			history = append(history, provider.Message{
				Role:       provider.RoleTool,
				ToolResult: &r,
			})
		}
	}

	err := fmt.Errorf("agent: max steps (%d) reached without final answer", maxSteps)
	sink.OnError(err)
	return history, err
}

// executeToolsParallel runs tool calls concurrently with a bounded semaphore.
// All OnToolCall events fire upfront so the user sees what is being launched;
// OnToolResult events fire as each tool finishes (real-time, may interleave).
// The returned slice preserves the input order for stable history append.
func (a *Agent) executeToolsParallel(
	ctx context.Context,
	calls []provider.ToolCall,
	sink Sink,
) []provider.ToolResult {
	// Announce all calls before launching — gives the user a glimpse of the
	// fan-out before any results stream back.
	for _, tc := range calls {
		sink.OnToolCall(tc)
	}

	parallel := a.MaxParallel
	if parallel <= 0 {
		parallel = defaultMaxParallel
	}
	if parallel > len(calls) {
		parallel = len(calls)
	}

	results := make([]provider.ToolResult, len(calls))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup

	for i := range calls {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = provider.ToolResult{
					CallID: calls[i].ID,
					Name:   calls[i].Name,
					Error:  ctx.Err().Error(),
				}
				sink.OnToolResult(results[i])
				return
			}
			defer func() { <-sem }()

			out, err := a.Tools.Call(ctx, calls[i].Name, calls[i].Args)
			tr := provider.ToolResult{
				CallID: calls[i].ID,
				Name:   calls[i].Name,
			}
			if err != nil {
				tr.Error = err.Error()
			} else {
				tr.Result = out
			}
			results[i] = tr
			sink.OnToolResult(tr)
		}(i)
	}
	wg.Wait()
	return results
}

// ----- helpers -----

func buildToolDecls(reg *funcs.Registry) []provider.ToolDecl {
	if reg == nil {
		return nil
	}
	out := []provider.ToolDecl{}
	for _, t := range reg.List() {
		out = append(out, provider.ToolDecl{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
		})
	}
	return out
}

// PrettyJSON for logging.
func PrettyJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("(unprintable: %v)", err)
	}
	return string(b)
}

// SentinelMaxSteps lets callers detect the specific reason without parsing strings.
var SentinelMaxSteps = errors.New("agent: max steps reached")

// noopSink is used when callers pass nil.
type noopSink struct{}

func (noopSink) OnUserMessage(string)                {}
func (noopSink) OnAssistantTextDelta(string)         {}
func (noopSink) OnAssistantThoughtDelta(string)      {}
func (noopSink) OnAssistantTextFinal(string)         {}
func (noopSink) OnToolCall(provider.ToolCall)        {}
func (noopSink) OnToolResult(provider.ToolResult)    {}
func (noopSink) OnUsage(provider.Usage)              {}
func (noopSink) OnTurnComplete(int)                  {}
func (noopSink) OnError(error)                       {}
