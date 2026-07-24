// api_types.go keeps HTTP request/response DTOs separate from handler logic while preserving behavior.
package http

import (
	"encoding/json"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"time"
)

// CreateSessionRequest represents a request to create a new session
type CreateSessionRequest struct {
	AgentID    string                `json:"agent_id"`
	Task       string                `json:"task,omitempty"`
	Images     []MessageImagePayload `json:"images,omitempty"`
	ParentID   string                `json:"parent_id,omitempty"`
	LinkType   string                `json:"link_type,omitempty"`
	Provider   string                `json:"provider,omitempty"`
	Model      string                `json:"model,omitempty"`
	ProjectID  string                `json:"project_id,omitempty"`
	SubAgentID string                `json:"sub_agent_id,omitempty"` // Optional sub-agent to use for this session
	// Optional direct agent targets. Unified agents are saved sub-agents or YAML-backed agent definitions;
	// Docker agents reference an already running local Brute container.
	UnifiedAgentID string                 `json:"unified_agent_id,omitempty"`
	DockerAgentID  string                 `json:"docker_agent_id,omitempty"`
	Queued         bool                   `json:"queued,omitempty"`     // If true, create session without starting it
	QueueMode      string                 `json:"queue_mode,omitempty"` // Optional queue behavior. "serial" runs queued project sessions one at a time.
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

// CreateSessionResponse represents a response after creating a session
type CreateSessionResponse struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	ParentID  string    `json:"parent_id,omitempty"`
	LinkType  string    `json:"link_type,omitempty"`
	ProjectID string    `json:"project_id,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	Model     string    `json:"model,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type PromptCachePayload struct {
	Provider          string    `json:"provider"`
	Model             string    `json:"model,omitempty"`
	LastRequestAt     time.Time `json:"last_request_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	TTLSeconds        int       `json:"ttl_seconds"`
	CachedInputTokens int       `json:"cached_input_tokens"`
	HitObserved       bool      `json:"hit_observed"`
	Estimated         bool      `json:"estimated"`
}

// SessionResponse represents a session with its messages
type SessionResponse struct {
	ID                     string                       `json:"id"`
	AgentID                string                       `json:"agent_id"`
	ParentID               string                       `json:"parent_id,omitempty"`
	LinkType               string                       `json:"link_type,omitempty"`
	JobID                  string                       `json:"job_id,omitempty"`
	ProjectID              string                       `json:"project_id,omitempty"`
	Provider               string                       `json:"provider,omitempty"`
	Model                  string                       `json:"model,omitempty"`
	RoutedProvider         string                       `json:"routed_provider,omitempty"`
	RoutedModel            string                       `json:"routed_model,omitempty"`
	RoutedRule             string                       `json:"routed_rule,omitempty"`
	RoutedReason           string                       `json:"routed_reason,omitempty"`
	FallbackActiveProvider string                       `json:"fallback_active_provider,omitempty"`
	FallbackActiveModel    string                       `json:"fallback_active_model,omitempty"`
	Title                  string                       `json:"title"`
	Summary                string                       `json:"summary,omitempty"`
	Status                 string                       `json:"status"`
	ActiveRuns             int                          `json:"active_runs"`
	TotalTokens            int                          `json:"total_tokens"`
	InputTokens            int                          `json:"input_tokens"`
	OutputTokens           int                          `json:"output_tokens"`
	CachedInputTokens      int                          `json:"cached_input_tokens,omitempty"`
	ReasoningTokens        int                          `json:"reasoning_tokens,omitempty"`
	CurrentContextTokens   int                          `json:"current_context_tokens"`
	ModelContextWindow     int                          `json:"model_context_window"`
	RunDurationSeconds     int64                        `json:"run_duration_seconds"`
	TaskProgress           string                       `json:"task_progress,omitempty"`
	ProviderFailures       []ProviderFailurePayload     `json:"provider_failures,omitempty"`
	PromptCache            *PromptCachePayload          `json:"prompt_cache,omitempty"`
	CreatedAt              time.Time                    `json:"created_at"`
	UpdatedAt              time.Time                    `json:"updated_at"`
	Messages               []MessageResponse            `json:"messages"`
	SystemPromptSnapshot   *SystemPromptSnapshotPayload `json:"system_prompt_snapshot,omitempty"`
	Metadata               map[string]interface{}       `json:"metadata,omitempty"`
	// A2A outbound fields — set for sessions used to contact remote agents.
	A2AOutbound        bool   `json:"a2a_outbound,omitempty"`
	A2ATargetAgentID   string `json:"a2a_target_agent_id,omitempty"`
	A2ATargetAgentName string `json:"a2a_target_agent_name,omitempty"`
}

type ProviderFailurePayload struct {
	Timestamp     time.Time `json:"timestamp"`
	Provider      string    `json:"provider,omitempty"`
	Model         string    `json:"model,omitempty"`
	Attempt       int       `json:"attempt,omitempty"`
	MaxAttempts   int       `json:"max_attempts,omitempty"`
	NodeIndex     int       `json:"node_index,omitempty"`
	TotalNodes    int       `json:"total_nodes,omitempty"`
	Phase         string    `json:"phase,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	FallbackTo    string    `json:"fallback_to,omitempty"`
	FallbackModel string    `json:"fallback_model,omitempty"`
}

type SystemPromptSnapshotPayload struct {
	BasePrompt        string                             `json:"base_prompt"`
	CombinedPrompt    string                             `json:"combined_prompt"`
	BaseEstimated     int                                `json:"base_estimated_tokens"`
	CombinedEstimated int                                `json:"combined_estimated_tokens"`
	Blocks            []SystemPromptBlockSnapshotPayload `json:"blocks"`
}

type SystemPromptBlockSnapshotPayload struct {
	Type            string `json:"type"`
	Value           string `json:"value"`
	Enabled         bool   `json:"enabled"`
	ResolvedContent string `json:"resolved_content,omitempty"`
	SourcePath      string `json:"source_path,omitempty"`
	Error           string `json:"error,omitempty"`
	EstimatedTokens int    `json:"estimated_tokens"`
}

// MessageResponse represents a message in a session
type MessageResponse struct {
	ID           string                 `json:"id,omitempty"`
	Role         string                 `json:"role"`
	Content      string                 `json:"content"`
	Images       []MessageImagePayload  `json:"images,omitempty"`
	ToolCalls    []ToolCallResponse     `json:"tool_calls,omitempty"`
	ToolResults  []ToolResultResponse   `json:"tool_results,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	Timestamp    time.Time              `json:"timestamp"`
	InputTokens  int                    `json:"input_tokens,omitempty"`
	OutputTokens int                    `json:"output_tokens,omitempty"`
}

type MessageImagePayload struct {
	Name       string `json:"name,omitempty"`
	MediaType  string `json:"media_type,omitempty"`
	DataBase64 string `json:"data_base64,omitempty"`
	URL        string `json:"url,omitempty"`
}

// ToolCallResponse represents a tool call
type ToolCallResponse struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Input            json.RawMessage `json:"input"`
	ThoughtSignature string          `json:"thought_signature,omitempty"`
	InputTokens      int             `json:"input_tokens,omitempty"`
	OutputTokens     int             `json:"output_tokens,omitempty"`
}

// ToolResultResponse represents a tool result
type ToolResultResponse struct {
	ToolCallID string                 `json:"tool_call_id"`
	Content    string                 `json:"content"`
	IsError    bool                   `json:"is_error"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	Name       string                 `json:"name,omitempty"` // Tool name (required by Gemini)
	DurationMs int64                  `json:"duration_ms"`
}

// ChatRequest represents a chat message request
type ChatRequest struct {
	Message string                `json:"message"`
	Images  []MessageImagePayload `json:"images,omitempty"`
}

// ChatResponse represents a chat response
type ChatResponse struct {
	Content  string            `json:"content"`
	Messages []MessageResponse `json:"messages"`
	Status   string            `json:"status"`
	Usage    UsageResponse     `json:"usage"`
}

type ChatStreamEvent struct {
	Type                    string                       `json:"type"`
	Delta                   string                       `json:"delta,omitempty"`
	Content                 string                       `json:"content,omitempty"`
	Message                 *MessageResponse             `json:"message,omitempty"`
	Messages                []MessageResponse            `json:"messages,omitempty"`
	Status                  string                       `json:"status,omitempty"`
	Usage                   *UsageResponse               `json:"usage,omitempty"`
	Error                   string                       `json:"error,omitempty"`
	Question                *session.QuestionData        `json:"question,omitempty"`
	ToolCalls               []StreamToolCallEvent        `json:"tool_calls,omitempty"`
	ToolProgress            *StreamToolProgressEvent     `json:"tool_progress,omitempty"`
	ToolResult              *StreamToolResultEvent       `json:"tool_result,omitempty"`
	Provider                *StreamProviderEvent         `json:"provider,omitempty"`
	Workflow                interface{}                  `json:"workflow,omitempty"`
	WorkflowTranscriptEntry interface{}                  `json:"workflow_transcript_entry,omitempty"`
	RoutedProvider          string                       `json:"routed_provider,omitempty"`
	RoutedModel             string                       `json:"routed_model,omitempty"`
	RoutedRule              string                       `json:"routed_rule,omitempty"`
	RoutedReason            string                       `json:"routed_reason,omitempty"`
	FallbackActiveProvider  string                       `json:"fallback_active_provider,omitempty"`
	FallbackActiveModel     string                       `json:"fallback_active_model,omitempty"`
	PromptCache             *PromptCachePayload          `json:"prompt_cache,omitempty"`
	Step                    int                          `json:"step,omitempty"`
	TurnID                  string                       `json:"turn_id,omitempty"`
	RuntimeTool             *StreamRuntimeToolEvent      `json:"runtime_tool,omitempty"`
	Cost                    *StreamRuntimeCostEvent      `json:"cost,omitempty"`
	RuntimeWarning          *StreamRuntimeWarningPayload `json:"runtime_warning,omitempty"`
	Approval                *NativeToolApprovalResponse  `json:"approval,omitempty"`
}

// NativeToolApprovalResponse matches Caesar's pending approval DTO.
type NativeToolApprovalResponse struct {
	RequestID string                       `json:"request_id"`
	SessionID string                       `json:"session_id"`
	ToolUseID string                       `json:"tool_use_id,omitempty"`
	ToolName  string                       `json:"tool_name"`
	Input     map[string]interface{}       `json:"input"`
	Reason    string                       `json:"reason,omitempty"`
	Kind      string                       `json:"kind"`
	Questions []NativeToolApprovalQuestion `json:"questions,omitempty"`
	CreatedAt string                       `json:"created_at"`
	ExpiresAt string                       `json:"expires_at,omitempty"`
	Status    string                       `json:"status,omitempty"`
}

type NativeToolApprovalQuestion struct {
	Question string                             `json:"question"`
	Header   string                             `json:"header"`
	Options  []NativeToolApprovalQuestionOption `json:"options"`
	Multiple bool                               `json:"multiple"`
	Custom   bool                               `json:"custom"`
}

type NativeToolApprovalQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	ImageURL    string `json:"image_url,omitempty"`
	AudioURL    string `json:"audio_url,omitempty"`
}

// StreamToolCallEvent represents a tool call in a stream event.
type StreamToolCallEvent struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Input            json.RawMessage `json:"input"`
	ThoughtSignature string          `json:"thought_signature,omitempty"`
}

type StreamToolProgressEvent struct {
	ToolCallID string                 `json:"tool_call_id,omitempty"`
	ToolName   string                 `json:"tool_name,omitempty"`
	Status     string                 `json:"status,omitempty"`
	Content    string                 `json:"content,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// StreamToolResultEvent represents a tool result in a stream event.
type StreamToolResultEvent struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error"`
}

type StreamProviderEvent struct {
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
	Attempt       int    `json:"attempt,omitempty"`
	MaxAttempts   int    `json:"max_attempts,omitempty"`
	NodeIndex     int    `json:"node_index,omitempty"`
	TotalNodes    int    `json:"total_nodes,omitempty"`
	Phase         string `json:"phase,omitempty"`
	Reason        string `json:"reason,omitempty"`
	FallbackTo    string `json:"fallback_to,omitempty"`
	FallbackModel string `json:"fallback_model,omitempty"`
	Recovered     bool   `json:"recovered,omitempty"`
}

// StreamRuntimeToolEvent describes Claude/native runtime tool lifecycle progress.
type StreamRuntimeToolEvent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Index     int    `json:"index"`
	InputJSON string `json:"input_json"`
}

// StreamRuntimeCostEvent describes provider runtime cost metadata.
type StreamRuntimeCostEvent struct {
	TotalCostUSD  float64 `json:"total_cost_usd"`
	DurationMS    int64   `json:"duration_ms"`
	DurationAPIMS int64   `json:"duration_api_ms"`
	NumTurns      int     `json:"num_turns"`
}

// StreamRuntimeWarningPayload describes non-fatal runtime status updates.
type StreamRuntimeWarningPayload struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// UsageResponse represents token usage
type UsageResponse struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// SessionListItem represents a session in the list
type SessionListItem struct {
	ID                 string                 `json:"id"`
	AgentID            string                 `json:"agent_id"`
	ParentID           string                 `json:"parent_id,omitempty"`
	LinkType           string                 `json:"link_type,omitempty"`
	JobID              string                 `json:"job_id,omitempty"`
	ProjectID          string                 `json:"project_id,omitempty"`
	Provider           string                 `json:"provider,omitempty"`
	Model              string                 `json:"model,omitempty"`
	RoutedProvider     string                 `json:"routed_provider,omitempty"`
	RoutedModel        string                 `json:"routed_model,omitempty"`
	Title              string                 `json:"title"`
	Summary            string                 `json:"summary,omitempty"`
	Status             string                 `json:"status"`
	TotalTokens        int                    `json:"total_tokens"`
	InputTokens        int                    `json:"input_tokens"`
	OutputTokens       int                    `json:"output_tokens"`
	RunDurationSeconds int64                  `json:"run_duration_seconds"`
	TaskProgress       string                 `json:"task_progress,omitempty"`
	PromptCache        *PromptCachePayload    `json:"prompt_cache,omitempty"`
	CreatedAt          time.Time              `json:"created_at"`
	UpdatedAt          time.Time              `json:"updated_at"`
	Metadata           map[string]interface{} `json:"metadata,omitempty"`
	// A2A inbound fields — only set for sessions created from A2A tunnel requests.
	A2AInbound         bool   `json:"a2a_inbound,omitempty"`
	A2ASourceAgentID   string `json:"a2a_source_agent_id,omitempty"`
	A2ASourceAgentName string `json:"a2a_source_agent_name,omitempty"`
}

// SubAgentRequest represents a request to create or update a sub-agent.
type SubAgentRequest struct {
	Name              string   `json:"name"`
	ProjectID         string   `json:"project_id,omitempty"`
	Provider          string   `json:"provider"`
	Model             string   `json:"model,omitempty"`
	EnabledTools      []string `json:"enabled_tools,omitempty"`
	InstructionBlocks string   `json:"instruction_blocks,omitempty"`
}

// SubAgentResponse represents a sub-agent in API responses.
type SubAgentResponse struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	ProjectID         string   `json:"project_id,omitempty"`
	Provider          string   `json:"provider"`
	Model             string   `json:"model"`
	EnabledTools      []string `json:"enabled_tools"`
	InstructionBlocks string   `json:"instruction_blocks"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
}

// ToolDefinitionResponse is a minimal tool info for UI.
type ToolDefinitionResponse struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// CreateJobRequest represents a request to create a recurring job
type CreateJobRequest struct {
	Name               string                 `json:"name"`
	ProjectID          string                 `json:"project_id,omitempty"`
	ScheduleText       string                 `json:"schedule_text"` // Natural language schedule
	TaskPrompt         string                 `json:"task_prompt"`
	TaskPromptSource   string                 `json:"task_prompt_source,omitempty"` // "text" | "file"
	TaskPromptFile     string                 `json:"task_prompt_file,omitempty"`
	RunTarget          string                 `json:"run_target,omitempty"` // "workflow" | "agent"
	WorkflowID         string                 `json:"workflow_id,omitempty"`
	WorkflowName       string                 `json:"workflow_name,omitempty"`
	WorkflowDefinition map[string]interface{} `json:"workflow_definition,omitempty"`
	LaunchAgentID      string                 `json:"launch_agent_id,omitempty"`
	LaunchAgentName    string                 `json:"launch_agent_name,omitempty"`
	LaunchAgentRuntime string                 `json:"launch_agent_runtime,omitempty"`
	UnifiedAgentID     string                 `json:"unified_agent_id,omitempty"`
	DockerAgentID      string                 `json:"docker_agent_id,omitempty"`
	LLMProvider        string                 `json:"llm_provider,omitempty"`
	LLMModel           string                 `json:"llm_model,omitempty"`
	Enabled            bool                   `json:"enabled"`
}

// UpdateJobRequest represents a request to update a recurring job
type UpdateJobRequest struct {
	Name               string                  `json:"name"`
	ProjectID          *string                 `json:"project_id,omitempty"`
	ScheduleText       string                  `json:"schedule_text"`
	TaskPrompt         string                  `json:"task_prompt"`
	TaskPromptSource   string                  `json:"task_prompt_source,omitempty"` // "text" | "file"
	TaskPromptFile     string                  `json:"task_prompt_file,omitempty"`
	RunTarget          string                  `json:"run_target,omitempty"`
	WorkflowID         string                  `json:"workflow_id,omitempty"`
	WorkflowName       string                  `json:"workflow_name,omitempty"`
	WorkflowDefinition *map[string]interface{} `json:"workflow_definition,omitempty"`
	LaunchAgentID      string                  `json:"launch_agent_id,omitempty"`
	LaunchAgentName    string                  `json:"launch_agent_name,omitempty"`
	LaunchAgentRuntime string                  `json:"launch_agent_runtime,omitempty"`
	UnifiedAgentID     string                  `json:"unified_agent_id,omitempty"`
	DockerAgentID      string                  `json:"docker_agent_id,omitempty"`
	LLMProvider        *string                 `json:"llm_provider,omitempty"`
	LLMModel           string                  `json:"llm_model,omitempty"`
	Enabled            *bool                   `json:"enabled,omitempty"`
}

// JobResponse represents a recurring job response
type JobResponse struct {
	ID                 string                 `json:"id"`
	ProjectID          string                 `json:"project_id,omitempty"`
	Name               string                 `json:"name"`
	ScheduleHuman      string                 `json:"schedule_human"`
	ScheduleCron       string                 `json:"schedule_cron"`
	TaskPrompt         string                 `json:"task_prompt"`
	TaskPromptSource   string                 `json:"task_prompt_source"`
	TaskPromptFile     string                 `json:"task_prompt_file,omitempty"`
	RunTarget          string                 `json:"run_target,omitempty"`
	WorkflowID         string                 `json:"workflow_id,omitempty"`
	WorkflowName       string                 `json:"workflow_name,omitempty"`
	WorkflowDefinition map[string]interface{} `json:"workflow_definition,omitempty"`
	LaunchAgentID      string                 `json:"launch_agent_id,omitempty"`
	LaunchAgentName    string                 `json:"launch_agent_name,omitempty"`
	LaunchAgentRuntime string                 `json:"launch_agent_runtime,omitempty"`
	UnifiedAgentID     string                 `json:"unified_agent_id,omitempty"`
	DockerAgentID      string                 `json:"docker_agent_id,omitempty"`
	LLMProvider        string                 `json:"llm_provider,omitempty"`
	LLMModel           string                 `json:"llm_model,omitempty"`
	Enabled            bool                   `json:"enabled"`
	LastRunAt          *time.Time             `json:"last_run_at,omitempty"`
	NextRunAt          *time.Time             `json:"next_run_at,omitempty"`
	CreatedAt          time.Time              `json:"created_at"`
	UpdatedAt          time.Time              `json:"updated_at"`
}

type SessionTemplateRequest struct {
	Name         string `json:"name"`
	Content      string `json:"content"`
	SlashCommand string `json:"slash_command"`
}

type SessionTemplateResponse struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Content      string    `json:"content"`
	SlashCommand string    `json:"slash_command"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// JobExecutionResponse represents a job execution response
type JobExecutionResponse struct {
	ID         string     `json:"id"`
	JobID      string     `json:"job_id"`
	SessionID  string     `json:"session_id,omitempty"`
	Status     string     `json:"status"`
	Output     string     `json:"output,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type SettingsResponse struct {
	Settings                               map[string]string `json:"settings"`
	CustomEnv                              map[string]string `json:"custom_env,omitempty"`
	DefaultSystemPrompt                    string            `json:"defaultSystemPrompt"`
	DefaultSystemPromptWithoutBuiltInTools string            `json:"defaultSystemPromptWithoutBuiltInTools"`
	DefaultPromptTemplates                 map[string]string `json:"defaultPromptTemplates"`
}

type UpdateSettingsRequest struct {
	Settings  map[string]string  `json:"settings"`
	CustomEnv *map[string]string `json:"custom_env,omitempty"`
}

type ProviderConfigResponse struct {
	Type                string                     `json:"type"`
	DisplayName         string                     `json:"display_name"`
	DefaultURL          string                     `json:"default_url"`
	RequiresKey         bool                       `json:"requires_key"`
	DefaultModel        string                     `json:"default_model"`
	ContextWindow       int                        `json:"context_window"`
	IsActive            bool                       `json:"is_active"`
	Configured          bool                       `json:"configured"`
	HasAPIKey           bool                       `json:"has_api_key"`
	BaseURL             string                     `json:"base_url"`
	Model               string                     `json:"model"`
	PromptCacheKey      string                     `json:"prompt_cache_key,omitempty"`
	ReasoningEffort     string                     `json:"reasoning_effort,omitempty"`
	TextVerbosity       string                     `json:"text_verbosity,omitempty"`
	ServiceTier         string                     `json:"service_tier,omitempty"`
	MaxTokens           int                        `json:"max_tokens,omitempty"`
	StatefulResponses   bool                       `json:"stateful_responses,omitempty"`
	ProxyManaged        bool                       `json:"proxy_managed"`
	ProxyBaseURL        string                     `json:"proxy_base_url,omitempty"`
	FallbackChain       []config.FallbackChainNode `json:"fallback_chain,omitempty"`
	RouterProvider      string                     `json:"router_provider,omitempty"`
	RouterModel         string                     `json:"router_model,omitempty"`
	RouterRules         []config.RouterRule        `json:"router_rules,omitempty"`
	BinaryPath          string                     `json:"binary_path,omitempty"`
	ConfigDir           string                     `json:"config_dir,omitempty"`
	HomePath            string                     `json:"home_path,omitempty"`
	EnvOverrides        map[string]string          `json:"env_overrides,omitempty"`
	SensitiveSecretKeys []string                   `json:"sensitive_secret_keys,omitempty"`
}

type ProviderUsageResponse struct {
	Provider      string             `json:"provider"`
	Status        string             `json:"status"`
	UsageLeftText string             `json:"usage_left_text"`
	UsageBars     []ProviderUsageBar `json:"usage_bars,omitempty"`
	Source        string             `json:"source,omitempty"`
	CheckedAt     string             `json:"checked_at,omitempty"`
	Refreshable   bool               `json:"refreshable"`
	Error         string             `json:"error,omitempty"`
}

type ProviderUsageBar struct {
	Label       string `json:"label"`
	UsedPercent int    `json:"used_percent"`
	LeftPercent int    `json:"left_percent"`
	ResetText   string `json:"reset_text,omitempty"`
	Status      string `json:"status,omitempty"`
}

type UpdateProviderRequest struct {
	Name              *string                     `json:"name,omitempty"`
	APIKey            *string                     `json:"api_key,omitempty"`
	BaseURL           *string                     `json:"base_url,omitempty"`
	Model             *string                     `json:"model,omitempty"`
	PromptCacheKey    *string                     `json:"prompt_cache_key,omitempty"`
	ReasoningEffort   *string                     `json:"reasoning_effort,omitempty"`
	TextVerbosity     *string                     `json:"text_verbosity,omitempty"`
	ServiceTier       *string                     `json:"service_tier,omitempty"`
	MaxTokens         *int                        `json:"max_tokens,omitempty"`
	StatefulResponses *bool                       `json:"stateful_responses,omitempty"`
	FallbackChain     *[]config.FallbackChainNode `json:"fallback_chain,omitempty"`
	RouterProvider    *string                     `json:"router_provider,omitempty"`
	RouterModel       *string                     `json:"router_model,omitempty"`
	RouterRules       *[]config.RouterRule        `json:"router_rules,omitempty"`
	Active            *bool                       `json:"active,omitempty"`
}

type SetActiveProviderRequest struct {
	Provider string `json:"provider"`
}

type CreateFallbackAggregateRequest struct {
	Name          string                     `json:"name"`
	FallbackChain []config.FallbackChainNode `json:"fallback_chain"`
	Active        bool                       `json:"active,omitempty"`
}

type ClaudeInstanceConfigRequest struct {
	BinaryPath       *string            `json:"binary_path,omitempty"`
	ConfigDir        *string            `json:"config_dir,omitempty"`
	HomePath         *string            `json:"home_path,omitempty"`
	EnvOverrides     *map[string]string `json:"env_overrides,omitempty"`
	SensitiveSecrets *map[string]string `json:"sensitive_secrets,omitempty"`
}

type CreateClaudeInstanceRequest struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Model  *string `json:"model,omitempty"`
	Active bool    `json:"active,omitempty"`
	ClaudeInstanceConfigRequest
}

type UpdateClaudeInstanceRequest struct {
	Name   *string `json:"name,omitempty"`
	Model  *string `json:"model,omitempty"`
	Active *bool   `json:"active,omitempty"`
	ClaudeInstanceConfigRequest
}

type ListProviderModelsResponse struct {
	Models []string `json:"models"`
}

type UpdateSessionProjectRequest struct {
	ProjectID *string `json:"project_id"`
}

type UpdateSessionProviderRequest struct {
	Provider string  `json:"provider"`
	Model    *string `json:"model,omitempty"`
}

type ProjectResponse struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Folder      *string           `json:"folder,omitempty"`
	Settings    map[string]string `json:"settings"`
	URLPatterns []string          `json:"url_patterns"`
	IsSystem    bool              `json:"is_system"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

type CreateProjectRequest struct {
	Name        string            `json:"name"`
	Folder      *string           `json:"folder,omitempty"`
	Settings    map[string]string `json:"settings,omitempty"`
	URLPatterns []string          `json:"url_patterns,omitempty"`
}

type UpdateProjectRequest struct {
	Name        *string            `json:"name,omitempty"`
	Folder      *string            `json:"folder,omitempty"`
	Settings    *map[string]string `json:"settings,omitempty"`
	URLPatterns *[]string          `json:"url_patterns,omitempty"`
}

type ProjectDatabaseResponse struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	Name        string    `json:"name"`
	Engine      string    `json:"engine"`
	DSN         string    `json:"dsn"`
	Environment string    `json:"environment"`
	IsReadOnly  bool      `json:"is_read_only"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateProjectDatabaseRequest struct {
	Name        string `json:"name"`
	Engine      string `json:"engine"`
	DSN         string `json:"dsn"`
	Environment string `json:"environment"`
	IsReadOnly  bool   `json:"is_read_only"`
}

type UpdateProjectDatabaseRequest struct {
	Name        *string `json:"name,omitempty"`
	Engine      *string `json:"engine,omitempty"`
	DSN         *string `json:"dsn,omitempty"`
	Environment *string `json:"environment,omitempty"`
	IsReadOnly  *bool   `json:"is_read_only,omitempty"`
}

type ProjectDatabaseTableResponse struct {
	Name string `json:"name"`
}

type ProjectDatabaseTableColumnResponse struct {
	Name         string                                     `json:"name"`
	DataType     string                                     `json:"data_type"`
	IsPrimaryKey bool                                       `json:"is_primary_key"`
	IsNullable   bool                                       `json:"is_nullable"`
	ForeignKeys  []ProjectDatabaseColumnAnalyticsForeignKey `json:"foreign_keys"`
}

type ProjectDatabaseUpdateCellRequest struct {
	Column         string            `json:"column"`
	Value          *string           `json:"value"`
	PrimaryKey     map[string]string `json:"primary_key"`
}

type ProjectDatabaseUpdateCellResponse struct {
	Query        string `json:"query"`
	RowsAffected int64  `json:"rows_affected"`
}

type ProjectDatabaseDataRequest struct {
	Query  string `json:"query"` // Raw query, or we can use specific pagination args for read-only table view
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

type ProjectDatabaseColumnAnalyticsResponse struct {
	Table              string                                     `json:"table"`
	Column             string                                     `json:"column"`
	TotalRowCount      int64                                      `json:"total_row_count"`
	DistinctCount      int64                                      `json:"distinct_count"`
	NullCount          int64                                      `json:"null_count"`
	TopValues          []ProjectDatabaseColumnAnalyticsValue      `json:"top_values"`
	TopValuesTruncated bool                                       `json:"top_values_truncated"`
	ForeignKeys        []ProjectDatabaseColumnAnalyticsForeignKey `json:"foreign_keys"`
}

type ProjectDatabaseColumnAnalyticsValue struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

type ProjectDatabaseColumnAnalyticsForeignKey struct {
	ConstraintName   string `json:"constraint_name,omitempty"`
	ReferencedTable  string `json:"referenced_table"`
	ReferencedColumn string `json:"referenced_column"`
}

// ProviderTestResponse represents the response from testing a provider
type ProviderTestResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// ProviderTestResult represents the test result for a single provider
type ProviderTestResult struct {
	Provider string `json:"provider"`
	Success  bool   `json:"success"`
	Message  string `json:"message"`
	Duration int64  `json:"duration_ms"`
}
