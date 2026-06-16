// Package provider defines the LLM-agnostic interface every backend must
// implement (Gemini today, OpenAI/Anthropic/local in the future).
//
// The Agent talks ONLY to this interface. Anything provider-specific
// (Gemini's `Part`, OpenAI's `tool_calls` JSON, etc.) is translated inside
// each provider's package before reaching the agent.
package provider

import "context"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one entry in a conversation history.
// Exactly one of Text / ToolCall / ToolResult should be populated.
type Message struct {
	Role       Role        `json:"role"`
	Text       string      `json:"text,omitempty"`
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
}

type ToolCall struct {
	ID   string         `json:"id"`           // unique within turn (provider-generated if absent)
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type ToolResult struct {
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`

	// Images carries any image(s) a tool wants the model to actually SEE
	// (e.g. `read` on a PNG). The wire layer in each provider renders these
	// as native image content; they are not part of the JSON `Result` text.
	// Excluded from JSON so saved sessions stay text-only (re-read to refetch).
	Images []Image `json:"-"`
}

// Image is raw image bytes plus their MIME type. Providers base64-encode the
// data into whatever multimodal shape their API expects.
type Image struct {
	MimeType string
	Data     []byte
}

// ToolDecl is what we pass to the provider so the model can call tools.
type ToolDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"parameters"`
}

// Usage is per-turn token accounting. Optional; some providers may omit.
type Usage struct {
	PromptTokens  int `json:"prompt_tokens"`
	OutputTokens  int `json:"output_tokens"`
	ThoughtTokens int `json:"thought_tokens"`
	TotalTokens   int `json:"total_tokens"`
}

// AccountInfo lets the GUI render `/whoami` without knowing the provider.
type AccountInfo struct {
	Provider string         `json:"provider"`
	Email    string         `json:"email,omitempty"`
	Tier     string         `json:"tier,omitempty"`
	Project  string         `json:"project,omitempty"`
	Extra    map[string]any `json:"extra,omitempty"`
}

// ----- Streaming events -----
//
// Every provider's Generate() returns a channel of Event. The agent reads
// until the channel is closed; that signals the turn is over.

type EventKind int

const (
	EventTextDelta EventKind = iota
	EventThoughtDelta
	EventToolCallRequest
	EventTurnDone
	EventError
)

func (k EventKind) String() string {
	switch k {
	case EventTextDelta:
		return "text"
	case EventThoughtDelta:
		return "thought"
	case EventToolCallRequest:
		return "tool_call"
	case EventTurnDone:
		return "turn_done"
	case EventError:
		return "error"
	default:
		return "unknown"
	}
}

type Event struct {
	Kind     EventKind
	Text     string    // for EventTextDelta / EventThoughtDelta
	ToolCall *ToolCall // for EventToolCallRequest
	Usage    *Usage    // for EventTurnDone
	Err      error     // for EventError
}

// GenerateRequest is what the agent sends to a provider for one turn.
type GenerateRequest struct {
	Model    string
	Messages []Message
	Tools    []ToolDecl
}

// Provider is the contract for any LLM backend.
type Provider interface {
	// Name is a stable identifier ("gemini", "openai", ...).
	Name() string

	// Generate streams events for one model turn. The returned channel is
	// closed when the turn is finished (success or error). Cancel via ctx.
	Generate(ctx context.Context, req GenerateRequest) (<-chan Event, error)

	// Account returns user/account/tier info for the GUI's /whoami.
	// Providers without account state may return a minimal AccountInfo.
	Account(ctx context.Context) (*AccountInfo, error)
}
