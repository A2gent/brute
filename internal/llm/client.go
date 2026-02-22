package llm

import (
	"context"
)

// Client defines the interface for LLM providers
type Client interface {
	Chat(ctx context.Context, request *ChatRequest) (*ChatResponse, error)
}

// StreamingClient defines optional streaming support for LLM providers.
type StreamingClient interface {
	ChatStream(ctx context.Context, request *ChatRequest, onEvent func(StreamEvent) error) (*ChatResponse, error)
}

// ChatRequest represents a chat completion request
type ChatRequest struct {
	Model        string
	Messages     []Message
	Tools        []ToolDefinition
	Temperature  float64
	MaxTokens    int
	SystemPrompt string
}

// Message represents a chat message
type Message struct {
	Role        string       `json:"role"` // "user", "assistant", "tool"
	Content     string       `json:"content"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	ToolResults []ToolResult `json:"tool_results,omitempty"`
}

// ToolCall represents a tool invocation
type ToolCall struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Input            string `json:"input"`                       // JSON string
	ThoughtSignature string `json:"thought_signature,omitempty"` // Gemini-specific field
}

// ToolResult represents a tool result
type ToolResult struct {
	ToolCallID string                 `json:"tool_call_id"`
	Content    string                 `json:"content"`
	IsError    bool                   `json:"is_error,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	Name       string                 `json:"name,omitempty"` // Tool name (required by Gemini)
}

// ToolDefinition defines a tool for the LLM
type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// ChatResponse represents a chat completion response
type ChatResponse struct {
	Content    string
	ToolCalls  []ToolCall
	Usage      TokenUsage
	StopReason string
}

// StreamEventType is the type of a streaming event.
type StreamEventType string

const (
	StreamEventContentDelta  StreamEventType = "content_delta"
	StreamEventToolCallDelta StreamEventType = "tool_call_delta"
	StreamEventUsage         StreamEventType = "usage"
	StreamEventProviderTrace StreamEventType = "provider_trace"
)

// StreamEvent is emitted during a streaming LLM response.
type StreamEvent struct {
	Type StreamEventType

	ContentDelta string

	ToolCallIndex  int
	ToolCallID     string
	ToolCallName   string
	ToolInputDelta string

	Usage TokenUsage

	Provider      string
	Model         string
	Attempt       int
	MaxAttempts   int
	NodeIndex     int
	TotalNodes    int
	Phase         string
	Reason        string
	FallbackTo    string
	FallbackModel string
	Recovered     bool
}

// TokenUsage tracks token consumption
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
}
