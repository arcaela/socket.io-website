package agent

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arcaela/mini-cli/internal/provider"
	"github.com/arcaela/mini-cli/internal/tools"
)

// fakeProvider lets tests script the events for each call to Generate. The
// `turns` slice is consumed in order: each entry produces ONE turn's events.
type fakeProvider struct {
	name      string
	turns     [][]provider.Event
	callCount int
	lastReq   provider.GenerateRequest
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Account(_ context.Context) (*provider.AccountInfo, error) {
	return &provider.AccountInfo{Provider: f.name}, nil
}

func (f *fakeProvider) Generate(ctx context.Context, req provider.GenerateRequest) (<-chan provider.Event, error) {
	f.callCount++
	f.lastReq = req
	idx := f.callCount - 1
	if idx >= len(f.turns) {
		return nil, errors.New("fakeProvider: no more scripted turns")
	}
	out := make(chan provider.Event, len(f.turns[idx])+1)
	go func() {
		defer close(out)
		for _, ev := range f.turns[idx] {
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// recordingSink captures every event so we can assert on the trace.
// Thread-safe: tool execution runs in parallel, multiple goroutines may call
// OnToolCall / OnToolResult concurrently.
type recordingSink struct {
	mu     sync.Mutex
	events []string
	text   strings.Builder
}

func (r *recordingSink) push(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, s)
}
func (r *recordingSink) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	copy(out, r.events)
	return out
}

func (r *recordingSink) OnUserMessage(t string) { r.push("user:" + t) }
func (r *recordingSink) OnAssistantTextDelta(t string) {
	r.mu.Lock()
	r.text.WriteString(t)
	r.mu.Unlock()
	r.push("text:" + t)
}
func (r *recordingSink) OnAssistantThoughtDelta(t string) { r.push("thought:" + t) }
func (r *recordingSink) OnAssistantTextFinal(t string)    { r.push("final:" + t) }
func (r *recordingSink) OnToolCall(c provider.ToolCall)   { r.push("call:" + c.Name) }
func (r *recordingSink) OnToolResult(res provider.ToolResult) {
	if res.Error != "" {
		r.push("result_err:" + res.Name)
	} else {
		r.push("result:" + res.Name)
	}
}
func (r *recordingSink) OnUsage(_ provider.Usage) { r.push("usage") }
func (r *recordingSink) OnTurnComplete(steps int) { r.push("turn_done") }
func (r *recordingSink) OnError(err error)        { r.push("error:" + err.Error()) }

// ----- Test 1: text-only response, single turn -----

func TestAgent_TextOnlyTurn(t *testing.T) {
	prov := &fakeProvider{
		name: "fake",
		turns: [][]provider.Event{
			{
				{Kind: provider.EventTextDelta, Text: "Hola "},
				{Kind: provider.EventTextDelta, Text: "mundo"},
				{Kind: provider.EventTurnDone, Usage: &provider.Usage{TotalTokens: 5}},
			},
		},
	}
	a := &Agent{Provider: prov, Tools: tools.BuiltIn(), Model: "fake-1"}
	sink := &recordingSink{}
	hist, err := a.Run(context.Background(), nil, "hi", sink)
	if err != nil {
		t.Fatal(err)
	}
	if got := sink.text.String(); got != "Hola mundo" {
		t.Fatalf("text mismatch: %q", got)
	}
	if len(hist) != 2 {
		t.Fatalf("expected user+assistant in history, got %d entries", len(hist))
	}
	if hist[0].Role != provider.RoleUser || hist[0].Text != "hi" {
		t.Fatalf("user msg wrong: %+v", hist[0])
	}
	if hist[1].Role != provider.RoleAssistant || hist[1].Text != "Hola mundo" {
		t.Fatalf("assistant msg wrong: %+v", hist[1])
	}
}

// ----- Test 2: tool call → tool result → final text -----

func TestAgent_ToolCallRoundtrip(t *testing.T) {
	prov := &fakeProvider{
		name: "fake",
		turns: [][]provider.Event{
			// Turn 1: model decides to call `bash`
			{
				{Kind: provider.EventToolCallRequest, ToolCall: &provider.ToolCall{
					ID:   "c1",
					Name: "bash",
					Args: map[string]any{"command": "echo agent-saw-this", "description": "echo for test"},
				}},
				{Kind: provider.EventTurnDone},
			},
			// Turn 2: model gets the result and emits final text
			{
				{Kind: provider.EventTextDelta, Text: "Done."},
				{Kind: provider.EventTurnDone, Usage: &provider.Usage{TotalTokens: 12}},
			},
		},
	}
	a := &Agent{Provider: prov, Tools: tools.BuiltIn(), Model: "fake-1"}
	sink := &recordingSink{}
	hist, err := a.Run(context.Background(), nil, "do it", sink)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the event ordering captured by the sink.
	want := []string{
		"user:do it",
		"call:bash",
		"result:bash",
		"text:Done.",
		"final:Done.",
		"usage",
		"turn_done",
	}
	got := sink.snapshot()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("event trace mismatch:\n got: %v\nwant: %v", got, want)
	}

	// Verify history has: user, assistant(tool_call), tool(result), assistant(text)
	if len(hist) != 4 {
		t.Fatalf("expected 4 history entries, got %d: %+v", len(hist), hist)
	}
	if hist[1].ToolCall == nil || hist[1].ToolCall.Name != "bash" {
		t.Fatalf("entry 1 should be assistant tool_call, got %+v", hist[1])
	}
	if hist[2].ToolResult == nil || hist[2].ToolResult.Name != "bash" {
		t.Fatalf("entry 2 should be tool result, got %+v", hist[2])
	}

	// Verify the second model call actually saw the tool result in messages.
	lastReqMessages := prov.lastReq.Messages
	foundResult := false
	for _, m := range lastReqMessages {
		if m.ToolResult != nil && m.ToolResult.Name == "bash" {
			foundResult = true
		}
	}
	if !foundResult {
		t.Fatal("second model call did not include the tool result in its messages")
	}
}

// ----- Test 3: provider error propagates and stops the loop -----

func TestAgent_ProviderError(t *testing.T) {
	prov := &fakeProvider{
		name: "fake",
		turns: [][]provider.Event{
			{
				{Kind: provider.EventError, Err: errors.New("rate-limited")},
			},
		},
	}
	a := &Agent{Provider: prov, Tools: tools.BuiltIn(), Model: "fake-1"}
	sink := &recordingSink{}
	_, err := a.Run(context.Background(), nil, "hi", sink)
	if err == nil || err.Error() != "rate-limited" {
		t.Fatalf("expected rate-limited error, got %v", err)
	}
	events := sink.snapshot()
	if len(events) == 0 || events[0] != "user:hi" {
		t.Fatalf("expected user event first, got %v", events)
	}
	hasError := false
	for _, e := range events {
		if strings.HasPrefix(e, "error:") {
			hasError = true
		}
	}
	if !hasError {
		t.Fatalf("expected error event in trace, got %v", events)
	}
}

// ----- Test 4: max steps prevents infinite loops -----

func TestAgent_MaxStepsCap(t *testing.T) {
	// Provider always asks for a tool call: agent should give up after MaxSteps.
	loopTurn := []provider.Event{
		{Kind: provider.EventToolCallRequest, ToolCall: &provider.ToolCall{
			ID: "c", Name: "bash", Args: map[string]any{"command": "true", "description": "loop step"},
		}},
		{Kind: provider.EventTurnDone},
	}
	prov := &fakeProvider{
		name:  "fake",
		turns: [][]provider.Event{loopTurn, loopTurn, loopTurn, loopTurn},
	}
	a := &Agent{Provider: prov, Tools: tools.BuiltIn(), Model: "fake-1", MaxSteps: 3}
	sink := &recordingSink{}
	_, err := a.Run(context.Background(), nil, "loop", sink)
	if err == nil || !strings.Contains(err.Error(), "max steps") {
		t.Fatalf("expected max-steps error, got %v", err)
	}
	if prov.callCount != 3 {
		t.Fatalf("expected exactly 3 provider calls, got %d", prov.callCount)
	}
}

// ----- Test: parallel tool execution -----

// TestAgent_ParallelToolExecution verifies that when the model emits multiple
// tool calls in a single turn, the agent runs them concurrently (faster than
// the serial-equivalent) and appends their results to history in stable order.
func TestAgent_ParallelToolExecution(t *testing.T) {
	const N = 4
	const sleepMs = 200

	calls := make([]provider.Event, N+1)
	for i := 0; i < N; i++ {
		calls[i] = provider.Event{
			Kind: provider.EventToolCallRequest,
			ToolCall: &provider.ToolCall{
				ID:   "c" + string(rune('A'+i)),
				Name: "bash",
				Args: map[string]any{
					"command":     "sleep 0." + string(rune('0'+sleepMs/100)) + "; echo " + string(rune('A'+i)),
					"description": "parallel test step " + string(rune('A'+i)),
				},
			},
		}
	}
	calls[N] = provider.Event{Kind: provider.EventTurnDone}

	prov := &fakeProvider{
		name: "fake",
		turns: [][]provider.Event{
			calls,
			{
				{Kind: provider.EventTextDelta, Text: "all done"},
				{Kind: provider.EventTurnDone},
			},
		},
	}
	a := &Agent{
		Provider:    prov,
		Tools:       tools.BuiltIn(),
		Model:       "fake-1",
		MaxParallel: N,
	}
	sink := &recordingSink{}

	start := time.Now()
	hist, err := a.Run(context.Background(), nil, "do them", sink)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}

	// If executed serially, total wall time would be N * sleepMs ms.
	// We give it a generous 2× the single-call time to account for overhead.
	maxParallel := time.Duration(2*sleepMs) * time.Millisecond
	if elapsed > maxParallel {
		t.Fatalf("expected parallel execution (<%v), took %v — likely serial",
			maxParallel, elapsed)
	}

	// Verify history order matches call order: result of c<A>, c<B>, c<C>, c<D>
	// must appear in that order even though they likely completed out of order.
	wantIDs := []string{"cA", "cB", "cC", "cD"}
	gotIDs := []string{}
	for _, m := range hist {
		if m.ToolResult != nil {
			gotIDs = append(gotIDs, m.ToolResult.CallID)
		}
	}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("history order not stable:\n got: %v\nwant: %v", gotIDs, wantIDs)
	}

	// All tool calls + all results should be in the trace, in some interleaved order.
	events := sink.snapshot()
	callsSeen, resultsSeen := 0, 0
	for _, e := range events {
		if strings.HasPrefix(e, "call:") {
			callsSeen++
		}
		if strings.HasPrefix(e, "result:") {
			resultsSeen++
		}
	}
	if callsSeen != N || resultsSeen != N {
		t.Fatalf("expected %d calls + %d results, got %d/%d (events=%v)",
			N, N, callsSeen, resultsSeen, events)
	}
}

// TestAgent_OneToolFailureDoesNotCancelOthers ensures parallel execution
// isolates failures: if one tool errors, the others still complete and the
// model gets all results (success + failure) in the next turn.
func TestAgent_OneToolFailureDoesNotCancelOthers(t *testing.T) {
	prov := &fakeProvider{
		name: "fake",
		turns: [][]provider.Event{
			{
				{Kind: provider.EventToolCallRequest, ToolCall: &provider.ToolCall{
					ID: "ok", Name: "bash",
					Args: map[string]any{"command": "echo good", "description": "the good one"},
				}},
				{Kind: provider.EventToolCallRequest, ToolCall: &provider.ToolCall{
					ID: "bad", Name: "bash",
					Args: map[string]any{}, // missing required "command" → error
				}},
				{Kind: provider.EventTurnDone},
			},
			{
				{Kind: provider.EventTextDelta, Text: "noted"},
				{Kind: provider.EventTurnDone},
			},
		},
	}
	a := &Agent{Provider: prov, Tools: tools.BuiltIn(), Model: "fake-1"}
	sink := &recordingSink{}
	hist, err := a.Run(context.Background(), nil, "x", sink)
	if err != nil {
		t.Fatal(err)
	}

	// Both tool results should be in history.
	results := []provider.ToolResult{}
	for _, m := range hist {
		if m.ToolResult != nil {
			results = append(results, *m.ToolResult)
		}
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 tool results, got %d", len(results))
	}
	// Sort by CallID for stable assertions on which one errored.
	sort.Slice(results, func(i, j int) bool { return results[i].CallID < results[j].CallID })
	if results[0].CallID != "bad" || results[0].Error == "" {
		t.Fatalf("expected `bad` to have error, got %+v", results[0])
	}
	if results[1].CallID != "ok" || results[1].Error != "" {
		t.Fatalf("expected `ok` to succeed, got %+v", results[1])
	}
}

// ----- Test 5: tools schema is forwarded to the provider -----

func TestAgent_ForwardsToolDecls(t *testing.T) {
	prov := &fakeProvider{
		name: "fake",
		turns: [][]provider.Event{
			{
				{Kind: provider.EventTextDelta, Text: "ok"},
				{Kind: provider.EventTurnDone},
			},
		},
	}
	a := &Agent{Provider: prov, Tools: tools.BuiltIn(), Model: "fake-1"}
	if _, err := a.Run(context.Background(), nil, "x", &recordingSink{}); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, d := range prov.lastReq.Tools {
		names = append(names, d.Name)
	}
	want := []string{"bash", "glob", "memory", "read", "task", "write"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tool decls forwarded wrong:\n got: %v\nwant: %v", names, want)
	}
}
