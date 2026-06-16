package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/arcaela/mini-cli/funcs"
	"github.com/arcaela/mini-cli/provider"
)

func TestTerminalApprover_AcceptsYes(t *testing.T) {
	in := strings.NewReader("y\n")
	out := &bytes.Buffer{}
	a := NewTerminalApprover(in, out)
	d := a.Approve(context.Background(), provider.ToolCall{Name: "bash"}, funcs.RiskDestructive)
	if d != DecisionAllow {
		t.Fatalf("want allow, got %v", d)
	}
	if !strings.Contains(out.String(), "destructive") {
		t.Errorf("prompt should mention risk level; got: %s", out.String())
	}
}

func TestTerminalApprover_DefaultIsDeny(t *testing.T) {
	a := NewTerminalApprover(strings.NewReader("\n"), &bytes.Buffer{})
	if d := a.Approve(context.Background(), provider.ToolCall{Name: "bash"}, funcs.RiskHigh); d != DecisionDeny {
		t.Fatalf("blank input should deny, got %v", d)
	}
}

func TestTerminalApprover_AlwaysRemembered(t *testing.T) {
	in := strings.NewReader("a\n")
	a := NewTerminalApprover(in, &bytes.Buffer{})
	if d := a.Approve(context.Background(), provider.ToolCall{Name: "bash"}, funcs.RiskHigh); d != DecisionAllowAlways {
		t.Fatalf("first call: want allow-always, got %v", d)
	}
	// Second call should NOT prompt and just allow.
	if d := a.Approve(context.Background(), provider.ToolCall{Name: "bash"}, funcs.RiskHigh); d != DecisionAllow {
		t.Fatalf("second call: want allow, got %v", d)
	}
}

func TestTerminalApprover_NeverRemembered(t *testing.T) {
	in := strings.NewReader("never\n")
	a := NewTerminalApprover(in, &bytes.Buffer{})
	if d := a.Approve(context.Background(), provider.ToolCall{Name: "bash"}, funcs.RiskHigh); d != DecisionDeny {
		t.Fatalf("first call: want deny, got %v", d)
	}
	if d := a.Approve(context.Background(), provider.ToolCall{Name: "bash"}, funcs.RiskHigh); d != DecisionDeny {
		t.Fatalf("second call: still deny, got %v", d)
	}
}

func TestStrictApprover_AllowsLow_DeniesRest(t *testing.T) {
	a := StrictApprover{}
	if d := a.Approve(context.Background(), provider.ToolCall{Name: "read"}, funcs.RiskLow); d != DecisionAllow {
		t.Errorf("strict should allow low, got %v", d)
	}
	if d := a.Approve(context.Background(), provider.ToolCall{Name: "bash"}, funcs.RiskHigh); d != DecisionDeny {
		t.Errorf("strict should deny high, got %v", d)
	}
	if d := a.Approve(context.Background(), provider.ToolCall{Name: "bash"}, funcs.RiskDestructive); d != DecisionDeny {
		t.Errorf("strict should deny destructive, got %v", d)
	}
}

func TestYoloApprover_AlwaysAllow(t *testing.T) {
	a := YoloApprover{}
	for _, r := range []funcs.Risk{funcs.RiskLow, funcs.RiskHigh, funcs.RiskDestructive} {
		if d := a.Approve(context.Background(), provider.ToolCall{Name: "bash"}, r); d != DecisionAllow {
			t.Errorf("yolo should allow %v, got %v", r, d)
		}
	}
}

// =============================================================================
// Agent integration: a denial becomes an error ToolResult, the model sees it.
// =============================================================================

// scriptedProvider replays one or more turns of provider events.
type scriptedProvider struct {
	turns [][]provider.Event
	n     int
	mu    sync.Mutex
}

func (p *scriptedProvider) Name() string { return "scripted" }
func (p *scriptedProvider) Account(context.Context) (*provider.AccountInfo, error) {
	return &provider.AccountInfo{Provider: "scripted"}, nil
}
func (p *scriptedProvider) Generate(ctx context.Context, _ provider.GenerateRequest) (<-chan provider.Event, error) {
	p.mu.Lock()
	if p.n >= len(p.turns) {
		p.mu.Unlock()
		return nil, errors.New("scriptedProvider: out of turns")
	}
	evs := p.turns[p.n]
	p.n++
	p.mu.Unlock()
	out := make(chan provider.Event, len(evs)+1)
	go func() {
		defer close(out)
		for _, e := range evs {
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// recordingSink2 reproduces the local recording sink shape used elsewhere.
type recordingSink2 struct {
	mu     sync.Mutex
	events []string
}

func (r *recordingSink2) push(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, s)
}
func (r *recordingSink2) OnUserMessage(string)                  {}
func (r *recordingSink2) OnAssistantTextDelta(string)           {}
func (r *recordingSink2) OnAssistantThoughtDelta(string)        {}
func (r *recordingSink2) OnAssistantTextFinal(string)           {}
func (r *recordingSink2) OnToolCall(c provider.ToolCall)        { r.push("call:" + c.Name) }
func (r *recordingSink2) OnToolResult(res provider.ToolResult) {
	if res.Error != "" {
		r.push("err:" + res.Error)
	} else {
		r.push("ok:" + res.Name)
	}
}
func (r *recordingSink2) OnUsage(provider.Usage) {}
func (r *recordingSink2) OnTurnComplete(int)     {}
func (r *recordingSink2) OnError(error)          {}

func TestAgent_StrictApproverBlocksRiskyTool(t *testing.T) {
	prov := &scriptedProvider{
		turns: [][]provider.Event{
			// Turn 1: model asks for `bash rm -rf /tmp/foo`.
			{
				{Kind: provider.EventToolCallRequest, ToolCall: &provider.ToolCall{
					ID: "c1", Name: "bash",
					Args: map[string]any{"command": "rm -rf /tmp/foo", "description": "delete"},
				}},
				{Kind: provider.EventTurnDone},
			},
			// Turn 2: model reads the error and replies.
			{
				{Kind: provider.EventTextDelta, Text: "ok, won't do it"},
				{Kind: provider.EventTurnDone},
			},
		},
	}
	a := &Agent{
		Provider: prov,
		Tools:    funcs.BuiltIn(),
		Model:    "x",
		Approver: StrictApprover{},
	}
	sink := &recordingSink2{}
	_, err := a.Run(context.Background(), nil, "delete that file", sink)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	hasDenialError := false
	for _, e := range sink.events {
		if strings.HasPrefix(e, "err:denied by user") {
			hasDenialError = true
		}
	}
	if !hasDenialError {
		t.Fatalf("expected denied-by-user error tool result, got events: %v", sink.events)
	}
}

func TestAgent_NilApproverAllowsEverything(t *testing.T) {
	// Sanity: prior behavior preserved when Approver is nil. Use a benign command.
	prov := &scriptedProvider{
		turns: [][]provider.Event{
			{
				{Kind: provider.EventToolCallRequest, ToolCall: &provider.ToolCall{
					ID: "c1", Name: "bash",
					Args: map[string]any{"command": "echo hi", "description": "echo"},
				}},
				{Kind: provider.EventTurnDone},
			},
			{
				{Kind: provider.EventTextDelta, Text: "done"},
				{Kind: provider.EventTurnDone},
			},
		},
	}
	a := &Agent{Provider: prov, Tools: funcs.BuiltIn(), Model: "x"} // no Approver
	sink := &recordingSink2{}
	if _, err := a.Run(context.Background(), nil, "echo", sink); err != nil {
		t.Fatal(err)
	}
	// We just need the bash result to NOT be a denial error.
	for _, e := range sink.events {
		if strings.HasPrefix(e, "err:denied by user") {
			t.Fatalf("nil approver should not deny, got events: %v", sink.events)
		}
	}
}
