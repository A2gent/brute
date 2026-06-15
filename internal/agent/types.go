package agent

// Config holds agent configuration
type Config struct {
	Name                     string
	Description              string
	Provider                 string
	Model                    string
	SystemPrompt             string
	Temperature              float64
	MaxSteps                 int
	ContextWindow            int
	CompactionTriggerPercent float64
	CompactionPrompt         string
	UsePreviousResponse      bool
	CompressToolResults      bool
}

// EventType is emitted while the agent executes a run.
type EventType string

const (
	EventAssistantDelta EventType = "assistant_delta"
	EventStepCompleted  EventType = "step_completed"
	EventToolExecuting  EventType = "tool_executing"
	EventToolProgress   EventType = "tool_progress"
	EventToolCompleted  EventType = "tool_completed"
	EventProviderTrace  EventType = "provider_trace"
)

const (
	envCompactionTriggerPercent = "AAGENT_CONTEXT_COMPACTION_TRIGGER_PERCENT"
	envCompactionPrompt         = "AAGENT_CONTEXT_COMPACTION_PROMPT"
	envSystemPrompt             = "AAGENT_SYSTEM_PROMPT"
	envSystemPromptAppend       = "AAGENT_SYSTEM_PROMPT_APPEND"
	envCompressToolResults      = "A2GENT_TOOL_RESULT_COMPRESSION_ENABLED"
)

const (
	metadataTotalInputTokens      = "total_input_tokens"
	metadataTotalOutputTokens     = "total_output_tokens"
	metadataTotalCachedTokens     = "total_cached_input_tokens"
	metadataTotalReasoningTokens  = "total_reasoning_tokens"
	metadataCurrentContextTokens  = "current_context_tokens"
	metadataContextWindow         = "context_window"
	metadataCompactionCount       = "compaction_count"
	metadataLastCompactionAt      = "last_compaction_at"
	metadataLastResponseID        = "last_response_id"
	messageMetadataCompaction     = "context_compaction"
	messageMetadataResponseID     = "response_id"
	toolMetadataExternalWait      = "external_wait"
	defaultCompactionTriggerPct   = 80.0
	emptyFinalResponseMaxRetries  = 1
	emptyFinalResponseRetryPrompt = `The previous model response was empty after tool execution. Produce a concise final response for the user now.

Summarize what was done, mention verification or blockers, and do not paste raw tool output. Call another tool only if it is truly required to complete the answer.`
	defaultCompactionPrompt = `You are compacting a coding-agent conversation because context usage is high.

Create a concise continuation summary that lets the agent continue work in a fresh context window.

Output format:
1) Goal
2) Progress so far
3) Important decisions and constraints
4) Open issues / next actions

Rules:
- Preserve critical technical details (paths, APIs, errors, constraints).
- Do not invent facts.
- Keep it compact and actionable.`
)

type compactionConfig struct {
	Enabled        bool
	ContextWindow  int
	TriggerPercent float64
	Prompt         string
}

// Event describes a streaming update from the agent.
type Event struct {
	Type         EventType
	Step         int
	Delta        string
	ToolCalls    []ToolCallEvent // Populated for EventToolExecuting
	ToolProgress *ToolProgressEvent
	ToolResult   *ToolResultEvent // Populated for EventToolCompleted (single result)
	Provider     *ProviderTraceEvent
}

// ToolCallEvent represents a tool call being executed.
type ToolCallEvent struct {
	ID               string
	Name             string
	Input            string // JSON string
	ThoughtSignature string
}

// ToolResultEvent represents the result of a tool execution.
type ToolResultEvent struct {
	ToolCallID string
	Name       string
	Content    string
	IsError    bool
	DurationMs int64
}

type ToolProgressEvent struct {
	ToolCallID string
	ToolName   string
	Status     string
	Content    string
	Metadata   map[string]interface{}
}

type ProviderTraceEvent struct {
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
