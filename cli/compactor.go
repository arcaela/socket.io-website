package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/arcaela/mini-cli/provider"
)

// Headers prepended to a summary so the model knows it is reading a condensed
// memo rather than verbatim turns.
const (
	compactedResetHeader = "[previous conversation summarized to fit the context window]\n"
	compactedTailHeader  = "[compacted earlier history; verbatim turns continue below]\n"
)

// =============================================================================
// History compaction
//
// As a conversation grows, every turn re-sends the entire history to the
// model. Once the prompt approaches the context window the agent starts
// getting 429s and eventually fails outright. Compaction trades one extra
// LLM call for a much shorter prompt: the older messages are summarised
// into a single system blob, and only the most recent N turns are kept
// verbatim.
//
// The agent triggers Compactor when `Agent.CompactThreshold > 0` and the
// last turn's PromptTokens exceeded that threshold.
// =============================================================================

// Compactor turns a slice of messages into a short prose summary.
type Compactor interface {
	Compact(ctx context.Context, history []provider.Message) (string, error)
}

// ProviderCompactor asks the LLM itself to summarise the history. Uses a
// short, opinionated prompt so the output is predictable and brief.
type ProviderCompactor struct {
	Provider provider.Provider
	Model    string
}

const compactInstruction = `Summarize the conversation below into a compact memo for a future agent turn.

Goals:
- Preserve key facts the user shared (preferences, constraints, identities).
- Preserve concrete results from earlier tool calls (file paths discovered,
  values read, anything the next turn might rely on).
- Preserve open questions or decisions still pending.

Skip greetings, restated questions, and verbose tool output bodies.

Output ONLY the summary, in plain prose. No preamble, no markdown headings,
no quotes around it.`

func (c ProviderCompactor) Compact(ctx context.Context, history []provider.Message) (string, error) {
	if len(history) == 0 {
		return "", nil
	}

	transcript := renderTranscript(history)
	req := provider.GenerateRequest{
		Model: c.Model,
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Text: compactInstruction},
			{Role: provider.RoleUser, Text: "CONVERSATION TO SUMMARIZE:\n\n" + transcript},
		},
		// No Tools on purpose — we want text output, never tool calls.
	}
	events, err := c.Provider.Generate(ctx, req)
	if err != nil {
		return "", err
	}

	var summary strings.Builder
	for ev := range events {
		switch ev.Kind {
		case provider.EventTextDelta:
			summary.WriteString(ev.Text)
		case provider.EventError:
			return "", ev.Err
		}
	}
	return strings.TrimSpace(summary.String()), nil
}

// keepTailAfterSummary summarises the older part of `history` and keeps the
// most recent `keep` messages verbatim, returning [summary, tail...]. The split
// snaps to a clean boundary so a tool_result is never separated from the
// tool_call it answers (which OpenAI/Anthropic reject). Used by manual /compact.
// Returns the history unchanged when it is too short or the summary is empty.
func keepTailAfterSummary(ctx context.Context, comp Compactor, history []provider.Message, keep int) ([]provider.Message, error) {
	split := safeCompactBoundary(history, keep)
	if split < 1 {
		return history, nil
	}
	summary, err := comp.Compact(ctx, history[:split])
	if err != nil {
		return history, err
	}
	if strings.TrimSpace(summary) == "" {
		return history, nil
	}
	tail := append([]provider.Message{}, history[split:]...)
	return append([]provider.Message{{Role: provider.RoleSystem, Text: compactedTailHeader + summary}}, tail...), nil
}

// safeCompactBoundary returns the index at which to split history into a
// summarised prefix (history[:idx]) and a verbatim tail (history[idx:]). It
// starts from len-keep and snaps BACKWARD until the tail begins on a clean
// boundary, so compaction never separates a tool_result from its tool_call.
// Returns 0 (or less) when no clean boundary exists below len-keep.
func safeCompactBoundary(history []provider.Message, keep int) int {
	idx := len(history) - keep
	for idx > 0 && !isCleanBoundary(history[idx]) {
		idx--
	}
	return idx
}

// isCleanBoundary reports whether a message can safely START a verbatim tail —
// i.e. it does not depend on an earlier message to be valid. A tool_result
// depends on its tool_call; an assistant tool_call may be mid multi-call block.
func isCleanBoundary(m provider.Message) bool {
	if m.ToolResult != nil {
		return false
	}
	if m.Role == provider.RoleAssistant && m.ToolCall != nil {
		return false
	}
	return true
}

// ContextWindowFor returns a model's context window in tokens. Override with
// MINI_CONTEXT_WINDOW; otherwise matched by known model-name prefixes, with a
// conservative default for anything unrecognised.
func ContextWindowFor(model string) int {
	if v := os.Getenv("MINI_CONTEXT_WINDOW"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	switch m := strings.ToLower(model); {
	case strings.HasPrefix(m, "gemini"):
		return 1_000_000
	case strings.HasPrefix(m, "claude"):
		return 200_000
	case strings.HasPrefix(m, "gpt-5"):
		return 400_000
	default:
		return 128_000
	}
}

// renderTranscript flattens the provider message slice into a single string
// the summariser can read. Tool calls and results are abbreviated to keep
// the summary call cheap.
func renderTranscript(history []provider.Message) string {
	var b strings.Builder
	for _, m := range history {
		switch {
		case m.ToolCall != nil:
			fmt.Fprintf(&b, "[tool_call %s]\n", m.ToolCall.Name)
		case m.ToolResult != nil:
			body := summarizeToolResult(m.ToolResult)
			fmt.Fprintf(&b, "[tool_result %s] %s\n", m.ToolResult.Name, body)
		case m.Text != "":
			fmt.Fprintf(&b, "[%s] %s\n", m.Role, m.Text)
		}
	}
	return b.String()
}

func summarizeToolResult(r *provider.ToolResult) string {
	if r == nil {
		return ""
	}
	if r.Error != "" {
		return "error: " + truncate(r.Error, 200)
	}
	switch v := r.Result.(type) {
	case string:
		return truncate(v, 200)
	default:
		return truncate(fmt.Sprintf("%v", v), 200)
	}
}
