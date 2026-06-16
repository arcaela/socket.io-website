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
	Provider     provider.Provider
	Tools        *funcs.Registry
	Model        string
	MaxSteps     int
	MaxParallel  int    // 0 = default (4); set to 1 for serial execution
	SystemPrompt string // optional; injected as a RoleSystem message at every Generate call (not stored in history)

	// Approver, if non-nil, gates risky tool calls. Low-risk tools always run.
	// Higher-risk tools consult the Approver and a `DecisionDeny` results in
	// an error ToolResult fed back to the model (so it can react / retry).
	Approver Approver

	// Compaction. When a turn's PromptTokens crosses the trigger (90% of
	// ContextWindow, or CompactThreshold when set as an explicit override),
	// Run summarises the whole conversation into a single system message and
	// starts fresh from it — but ONLY in interactive mode. A one-shot run that
	// overflows is meant to fail loudly rather than silently rewrite history.
	Compactor         Compactor
	ContextWindow     int  // model context size in tokens; 0 = unknown (no auto-compaction)
	CompactThreshold  int  // explicit token trigger override; 0 = use 90% of ContextWindow
	CompactKeepRecent int  // verbatim tail kept by the manual /compact path; 0 → defaultCompactKeep
	Interactive       bool // auto-compaction only runs in interactive sessions
}

const (
	defaultMaxSteps    = 25
	defaultMaxParallel = 4
	defaultCompactKeep = 6 // last 3 user/assistant pairs survive verbatim
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
			history = a.maybeCompact(ctx, history, lastUsage, sink)
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

	err := fmt.Errorf("%w (limit %d)", SentinelMaxSteps, maxSteps)
	sink.OnError(err)
	return history, err
}

// compactTriggerTokens is the prompt-token count at which auto-compaction
// fires: an explicit CompactThreshold override if set, otherwise 90% of the
// model's context window. Returns 0 (never trigger) when neither is known.
func (a *Agent) compactTriggerTokens() int {
	if a.CompactThreshold > 0 {
		return a.CompactThreshold
	}
	if a.ContextWindow > 0 {
		return a.ContextWindow * 9 / 10
	}
	return 0
}

// maybeCompact starts a fresh conversation from a summary when the prompt has
// grown past the trigger (≈90% of the context window). This runs ONLY in
// interactive mode: a one-shot run that overflows should fail loudly rather
// than silently rewrite its own history. Failures are non-fatal — the original
// history is returned and the next turn proceeds as before.
func (a *Agent) maybeCompact(ctx context.Context, history []provider.Message, lastUsage *provider.Usage, sink Sink) []provider.Message {
	if a.Compactor == nil || !a.Interactive {
		return history
	}
	trigger := a.compactTriggerTokens()
	if trigger <= 0 || lastUsage == nil || lastUsage.PromptTokens < trigger {
		return history
	}
	if len(history) <= 1 {
		return history
	}
	msg, ok, err := summarizeToSystem(ctx, a.Compactor, history, compactedResetHeader)
	if err != nil {
		sink.OnError(fmt.Errorf("compaction failed (continuing with full history): %w", err))
		return history
	}
	if !ok {
		return history
	}
	return []provider.Message{msg}
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

			// Approval gate: if the tool is risky and the agent has an
			// Approver, ask before executing. A Deny becomes an error
			// tool_result so the model is told why and can adapt.
			if a.Approver != nil {
				if tool, ok := a.Tools.Get(calls[i].Name); ok {
					risk := funcs.AssessRisk(tool, calls[i].Args)
					if risk > funcs.RiskLow {
						switch a.Approver.Approve(ctx, calls[i], risk) {
						case DecisionDeny:
							results[i] = provider.ToolResult{
								CallID: calls[i].ID,
								Name:   calls[i].Name,
								Error:  fmt.Sprintf("denied by user (risk=%s)", risk),
							}
							sink.OnToolResult(results[i])
							return
						}
					}
				}
			}

			out, err := a.Tools.Call(ctx, calls[i].Name, calls[i].Args)
			tr := provider.ToolResult{
				CallID: calls[i].ID,
				Name:   calls[i].Name,
			}
			if err != nil {
				tr.Error = err.Error()
			} else {
				tr.Result = out
				// If the tool produced image(s), carry them as model-visible
				// content so providers can render them as native image parts.
				if ip, ok := out.(funcs.ImageProducer); ok {
					for _, im := range ip.ToolImages() {
						tr.Images = append(tr.Images, provider.Image{MimeType: im.MimeType, Data: im.Data})
					}
				}
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

// SentinelMaxSteps lets callers detect the specific reason without parsing
// strings: the error returned when the step cap is hit wraps it, so
// errors.Is(err, SentinelMaxSteps) is true.
var SentinelMaxSteps = errors.New("agent: max steps reached without final answer")

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
