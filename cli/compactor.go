package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/arcaela/mini-cli/provider"
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
		return "error: " + truncateForPrompt(r.Error, 200)
	}
	switch v := r.Result.(type) {
	case string:
		return truncateForPrompt(v, 200)
	default:
		return truncateForPrompt(fmt.Sprintf("%v", v), 200)
	}
}
