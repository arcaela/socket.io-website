package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/arcaela/mini-cli/funcs"
	"github.com/arcaela/mini-cli/provider"
)

// =============================================================================
// Approver — gates risky tool calls behind a user decision.
//
// The Agent consults the approver before executing any tool whose RiskOf
// returns higher than RiskLow. Low-risk reads (read, glob, web_*) never
// prompt. Default policy denies destructive operations unless explicitly
// allowed; "always" decisions are remembered for the rest of the session.
//
// Implementations:
//
//   - TerminalApprover: prompts on stdin. Used by the REPL.
//   - YoloApprover:     never blocks. Used when MINI_YOLO=1 or in one-shot
//                       mode where the user opted in.
//   - StrictApprover:   denies everything Risky. Used by default in one-shot
//                       mode so unattended runs never delete data.
// =============================================================================

type Decision int

const (
	DecisionAllow       Decision = iota // run this call only
	DecisionAllowAlways                 // run this call AND every future call of the same tool
	DecisionDeny                        // do not run; inject an error tool result
)

func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionAllowAlways:
		return "allow-always"
	case DecisionDeny:
		return "deny"
	default:
		return "unknown"
	}
}

// Approver decides whether a single tool call may proceed.
type Approver interface {
	Approve(ctx context.Context, call provider.ToolCall, risk funcs.Risk) Decision
}

// TerminalApprover prompts the user via stdin/stdout. Safe for concurrent
// turns because the prompt is mutex-guarded (parallel tool execution may
// race otherwise).
type TerminalApprover struct {
	In  io.Reader
	Out io.Writer

	mu        sync.Mutex
	allowAll  map[string]bool // tool name → "user said allow-always"
	denyAll   map[string]bool // tool name → "user said deny-always"
}

func NewTerminalApprover(in io.Reader, out io.Writer) *TerminalApprover {
	return &TerminalApprover{
		In:       in,
		Out:      out,
		allowAll: map[string]bool{},
		denyAll:  map[string]bool{},
	}
}

func (a *TerminalApprover) Approve(_ context.Context, call provider.ToolCall, risk funcs.Risk) Decision {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.allowAll[call.Name] {
		return DecisionAllow
	}
	if a.denyAll[call.Name] {
		return DecisionDeny
	}

	args, _ := json.Marshal(call.Args)
	fmt.Fprintf(a.Out, "\n⚠  [%s risk] %s %s\n", risk, call.Name, truncate(string(args), 200))
	fmt.Fprintf(a.Out, "   approve? [y]es / [n]o / [a]lways / [N]ever (default no): ")

	scanner := bufio.NewScanner(a.In)
	if !scanner.Scan() {
		return DecisionDeny
	}
	switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
	case "y", "yes":
		return DecisionAllow
	case "a", "always":
		a.allowAll[call.Name] = true
		return DecisionAllowAlways
	case "never":
		a.denyAll[call.Name] = true
		return DecisionDeny
	default:
		return DecisionDeny
	}
}

// YoloApprover always allows. Enable with MINI_YOLO=1 or via /yolo in REPL.
type YoloApprover struct{}

func (YoloApprover) Approve(context.Context, provider.ToolCall, funcs.Risk) Decision {
	return DecisionAllow
}

// StrictApprover denies anything that is High or Destructive. Used in
// one-shot mode by default so unattended `mini chat "..."` runs cannot lose
// data without explicit opt-in.
type StrictApprover struct{}

func (StrictApprover) Approve(_ context.Context, _ provider.ToolCall, risk funcs.Risk) Decision {
	if risk <= funcs.RiskLow {
		return DecisionAllow
	}
	return DecisionDeny
}

// PolicyApproverForOneShot picks the right approver based on env. Used by
// `mini chat "prompt"` (no REPL).
func PolicyApproverForOneShot() Approver {
	if os.Getenv("MINI_YOLO") == "1" {
		return YoloApprover{}
	}
	return StrictApprover{}
}
