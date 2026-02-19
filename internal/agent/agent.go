package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

// Config holds agent configuration
type Config struct {
	Name                     string
	Description              string
	Model                    string
	SystemPrompt             string
	Temperature              float64
	MaxSteps                 int
	ContextWindow            int
	CompactionTriggerPercent float64
	CompactionPrompt         string
}

// Agent represents an AI agent that can execute tasks
type Agent struct {
	config         Config
	llmClient      llm.Client
	toolManager    *tools.Manager
	sessionManager *session.Manager
}

// EventType is emitted while the agent executes a run.
type EventType string

const (
	EventAssistantDelta EventType = "assistant_delta"
	EventStepCompleted  EventType = "step_completed"
	EventToolExecuting  EventType = "tool_executing"
	EventToolCompleted  EventType = "tool_completed"
)

const (
	envCompactionTriggerPercent = "AAGENT_CONTEXT_COMPACTION_TRIGGER_PERCENT"
	envCompactionPrompt         = "AAGENT_CONTEXT_COMPACTION_PROMPT"
	envSystemPrompt             = "AAGENT_SYSTEM_PROMPT"
	envSystemPromptAppend       = "AAGENT_SYSTEM_PROMPT_APPEND"
)

const (
	metadataTotalInputTokens     = "total_input_tokens"
	metadataTotalOutputTokens    = "total_output_tokens"
	metadataCurrentContextTokens = "current_context_tokens"
	metadataCompactionCount      = "compaction_count"
	metadataLastCompactionAt     = "last_compaction_at"
	messageMetadataCompaction    = "context_compaction"
	defaultCompactionTriggerPct  = 80.0
	defaultCompactionPrompt      = `You are compacting a coding-agent conversation because context usage is high.

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
	Type       EventType
	Step       int
	Delta      string
	ToolCalls  []ToolCallEvent  // Populated for EventToolExecuting
	ToolResult *ToolResultEvent // Populated for EventToolCompleted (single result)
}

// ToolCallEvent represents a tool call being executed.
type ToolCallEvent struct {
	ID    string
	Name  string
	Input string // JSON string
}

// ToolResultEvent represents the result of a tool execution.
type ToolResultEvent struct {
	ToolCallID string
	Name       string
	Content    string
	IsError    bool
}

// New creates a new agent
func New(config Config, llmClient llm.Client, toolManager *tools.Manager, sessionManager *session.Manager) *Agent {
	if config.MaxSteps == 0 {
		config.MaxSteps = 50
	}
	systemPromptExplicit := config.SystemPrompt != ""
	if config.SystemPrompt == "" {
		config.SystemPrompt = strings.TrimSpace(os.Getenv(envSystemPrompt))
		if config.SystemPrompt == "" {
			config.SystemPrompt = defaultSystemPrompt
		}
	}
	appendPrompt := strings.TrimSpace(os.Getenv(envSystemPromptAppend))
	if appendPrompt != "" && !systemPromptExplicit {
		config.SystemPrompt = strings.TrimSpace(config.SystemPrompt) + "\n\n" + appendPrompt
	}

	return &Agent{
		config:         config,
		llmClient:      llmClient,
		toolManager:    toolManager,
		sessionManager: sessionManager,
	}
}

// Run executes the agent with the given task
// Returns the response content and total token usage
func (a *Agent) Run(ctx context.Context, sess *session.Session, task string) (string, llm.TokenUsage, error) {
	return a.RunWithEvents(ctx, sess, task, nil)
}

// RunWithEvents executes the agent and emits streaming events when available.
func (a *Agent) RunWithEvents(ctx context.Context, sess *session.Session, task string, onEvent func(Event)) (string, llm.TokenUsage, error) {
	logging.Info("Agent run started: session=%s", sess.ID)
	// Note: User message is already added by the TUI before calling Run
	// Run the agentic loop
	result, usage, err := a.loop(ctx, sess, onEvent)
	if err != nil {
		logging.Error("Agent run failed: %v", err)
	} else {
		logging.Info("Agent run completed: total_input=%d total_output=%d", usage.InputTokens, usage.OutputTokens)
	}
	return result, usage, err
}

// loop implements the main agentic loop
// Returns the response content and total token usage
func (a *Agent) loop(ctx context.Context, sess *session.Session, onEvent func(Event)) (string, llm.TokenUsage, error) {
	step := 0
	totalUsage := llm.TokenUsage{}

	// Add session ID to context for tools that need it (e.g., question tool)
	ctx = context.WithValue(ctx, "session_id", sess.ID)

	// Clean up incomplete tool calls before starting
	a.cleanupIncompleteToolCalls(sess)

	for {
		// Check context - distinguish between user cancellation and timeouts
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				// Explicit user cancellation (e.g., user clicked cancel, closed browser)
				// Pause immediately - user wants to stop
				logging.Info("User cancelled session %s", sess.ID)
				sess.SetStatus(session.StatusPaused)
				a.sessionManager.Save(sess)
				return "", totalUsage, ctx.Err()
			}
			// For context.DeadlineExceeded, we continue and let the agent see tool errors
			// The agent can then decide whether to retry or give up
			logging.Info("Context deadline exceeded for session %s, continuing to let agent handle errors", sess.ID)
		}

		// Check step limit
		if step >= a.config.MaxSteps {
			sess.SetStatus(session.StatusCompleted)
			a.sessionManager.Save(sess)
			return a.getLastAssistantContent(sess), totalUsage, nil
		}

		step++
		logging.Debug("Agent step %d/%d", step, a.config.MaxSteps)

		// Compact conversation before the next normal step once threshold is reached.
		compactionUsage, compacted, err := a.maybeCompactContext(ctx, sess, step)
		if err != nil {
			logging.Warn("Context compaction failed (continuing without compaction): %v", err)
		} else if compacted {
			totalUsage.InputTokens += compactionUsage.InputTokens
			totalUsage.OutputTokens += compactionUsage.OutputTokens
		}

		// Build chat request
		request := a.buildRequest(sess)

		// Call LLM (streaming when supported)
		response, err := a.callLLM(ctx, request, step, onEvent)
		if err != nil {
			sess.SetStatus(session.StatusFailed)
			a.sessionManager.Save(sess)
			return "", totalUsage, fmt.Errorf("LLM error: %w", err)
		}

		// Accumulate token usage
		totalUsage.InputTokens += response.Usage.InputTokens
		totalUsage.OutputTokens += response.Usage.OutputTokens
		a.addTokenUsageMetadata(sess, response.Usage)

		// Check if we have tool calls
		if len(response.ToolCalls) == 0 {
			// No tool calls - agent is done
			sess.AddAssistantMessage(response.Content, nil)
			sess.SetStatus(session.StatusCompleted)
			a.sessionManager.Save(sess)
			if onEvent != nil {
				onEvent(Event{Type: EventStepCompleted, Step: step})
			}
			return response.Content, totalUsage, nil
		}

		// Convert tool calls for session storage
		sessionToolCalls := make([]session.ToolCall, len(response.ToolCalls))
		for i, tc := range response.ToolCalls {
			sessionToolCalls[i] = session.ToolCall{
				ID:               tc.ID,
				Name:             tc.Name,
				Input:            []byte(tc.Input),
				ThoughtSignature: tc.ThoughtSignature,
			}
		}

		// Add assistant message with tool calls
		sess.AddAssistantMessage(response.Content, sessionToolCalls)

		// Execute tools
		if onEvent != nil {
			toolCallEvents := make([]ToolCallEvent, len(response.ToolCalls))
			for i, tc := range response.ToolCalls {
				toolCallEvents[i] = ToolCallEvent{
					ID:    tc.ID,
					Name:  tc.Name,
					Input: tc.Input,
				}
			}
			onEvent(Event{Type: EventToolExecuting, Step: step, ToolCalls: toolCallEvents})
		}
		toolResults := a.toolManager.ExecuteParallel(ctx, response.ToolCalls)

		// Convert results
		sessionResults := make([]session.ToolResult, len(toolResults))
		for i, tr := range toolResults {
			sessionResults[i] = session.ToolResult{
				ToolCallID: tr.ToolCallID,
				Content:    tr.Content,
				IsError:    tr.IsError,
				Metadata:   tr.Metadata,
				Name:       tr.Name,
			}
		}

		// Add tool results to session
		sess.AddToolResult(sessionResults)

		// Reload session to check if status was changed by tools (e.g., question tool)
		// IMPORTANT: Do this BEFORE Save() so we can detect status changes made by tools
		freshSess, reloadErr := a.sessionManager.Get(sess.ID)
		if reloadErr == nil && freshSess.Status == session.StatusInputRequired {
			logging.Info("Session %s requires user input (detected after tool execution), pausing", sess.ID)
			// Don't save the local sess changes - use the fresh one with input_required status
			if onEvent != nil {
				onEvent(Event{Type: EventToolCompleted, Step: step})
				onEvent(Event{Type: EventStepCompleted, Step: step})
			}
			return "", totalUsage, nil
		}

		// Save session after each step
		if err := a.sessionManager.Save(sess); err != nil {
			// Silently continue on save errors
			_ = err
		}

		if onEvent != nil {
			onEvent(Event{Type: EventToolCompleted, Step: step})
			onEvent(Event{Type: EventStepCompleted, Step: step})
		}
	}
}

func (a *Agent) callLLM(ctx context.Context, request *llm.ChatRequest, step int, onEvent func(Event)) (*llm.ChatResponse, error) {
	// When no event sink is provided, use non-streaming Chat.
	// This avoids "partial stream emitted" fallback lock-in and lets fallback chains
	// seamlessly move to the next provider on retryable failures.
	if onEvent == nil {
		return a.llmClient.Chat(ctx, request)
	}

	streamClient, ok := a.llmClient.(llm.StreamingClient)
	if !ok {
		return a.llmClient.Chat(ctx, request)
	}

	return streamClient.ChatStream(ctx, request, func(ev llm.StreamEvent) error {
		if onEvent == nil {
			return nil
		}
		if ev.Type == llm.StreamEventContentDelta && ev.ContentDelta != "" {
			onEvent(Event{
				Type:  EventAssistantDelta,
				Step:  step,
				Delta: ev.ContentDelta,
			})
		}
		return nil
	})
}

// buildRequest builds a chat request from the session
func (a *Agent) buildRequest(sess *session.Session) *llm.ChatRequest {
	// Convert session messages to LLM messages
	activeMessages := a.getActiveConversationMessages(sess)
	messages := make([]llm.Message, 0, len(activeMessages))

	for _, m := range activeMessages {
		msg := llm.Message{
			Role:    m.Role,
			Content: m.Content,
		}

		// Convert tool calls
		if len(m.ToolCalls) > 0 {
			msg.ToolCalls = make([]llm.ToolCall, len(m.ToolCalls))
			for i, tc := range m.ToolCalls {
				msg.ToolCalls[i] = llm.ToolCall{
					ID:               tc.ID,
					Name:             tc.Name,
					Input:            string(tc.Input),
					ThoughtSignature: tc.ThoughtSignature,
				}
			}
		}

		// Convert tool results
		if len(m.ToolResults) > 0 {
			msg.ToolResults = make([]llm.ToolResult, len(m.ToolResults))
			for i, tr := range m.ToolResults {
				msg.ToolResults[i] = llm.ToolResult{
					ToolCallID: tr.ToolCallID,
					Content:    tr.Content,
					IsError:    tr.IsError,
					Metadata:   tr.Metadata,
					Name:       tr.Name,
				}
			}
		}

		messages = append(messages, msg)
	}

	return &llm.ChatRequest{
		Model:        a.config.Model,
		Messages:     messages,
		Tools:        a.toolManager.GetDefinitions(),
		Temperature:  a.config.Temperature,
		SystemPrompt: a.config.SystemPrompt,
	}
}

func (a *Agent) buildCompactionRequest(sess *session.Session, prompt string) *llm.ChatRequest {
	activeMessages := a.getActiveConversationMessages(sess)
	messages := make([]llm.Message, 0, len(activeMessages))

	for _, m := range activeMessages {
		msg := llm.Message{
			Role:    m.Role,
			Content: m.Content,
		}

		if len(m.ToolCalls) > 0 {
			msg.ToolCalls = make([]llm.ToolCall, len(m.ToolCalls))
			for i, tc := range m.ToolCalls {
				msg.ToolCalls[i] = llm.ToolCall{
					ID:               tc.ID,
					Name:             tc.Name,
					Input:            string(tc.Input),
					ThoughtSignature: tc.ThoughtSignature,
				}
			}
		}

		if len(m.ToolResults) > 0 {
			msg.ToolResults = make([]llm.ToolResult, len(m.ToolResults))
			for i, tr := range m.ToolResults {
				msg.ToolResults[i] = llm.ToolResult{
					ToolCallID: tr.ToolCallID,
					Content:    tr.Content,
					IsError:    tr.IsError,
					Metadata:   tr.Metadata,
					Name:       tr.Name,
				}
			}
		}

		messages = append(messages, msg)
	}

	return &llm.ChatRequest{
		Model:        a.config.Model,
		Messages:     messages,
		Temperature:  0.2,
		MaxTokens:    4096,
		SystemPrompt: prompt,
	}
}

// getLastAssistantContent returns the content of the last assistant message
func (a *Agent) getLastAssistantContent(sess *session.Session) string {
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if sess.Messages[i].Role == "assistant" && sess.Messages[i].Content != "" {
			return sess.Messages[i].Content
		}
	}
	return ""
}

// cleanupIncompleteToolCalls removes assistant messages with tool calls that don't have corresponding tool results
// This can happen when the user interrupts a tool execution
func (a *Agent) cleanupIncompleteToolCalls(sess *session.Session) {
	if len(sess.Messages) == 0 {
		return
	}

	// Find the last assistant message with tool calls
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		msg := sess.Messages[i]

		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			// Check if there's a following tool message with results
			hasResults := false
			if i+1 < len(sess.Messages) && sess.Messages[i+1].Role == "tool" {
				hasResults = true
			}

			if !hasResults {
				// Remove this incomplete assistant message
				logging.Warn("Removing incomplete tool call message (no results)")
				sess.Messages = append(sess.Messages[:i], sess.Messages[i+1:]...)
				// Continue checking in case there are more
				continue
			}
		}
	}
}

func (a *Agent) maybeCompactContext(ctx context.Context, sess *session.Session, step int) (llm.TokenUsage, bool, error) {
	cfg := a.resolveCompactionConfig()
	if !cfg.Enabled || sess == nil {
		return llm.TokenUsage{}, false, nil
	}

	currentTokens := metadataFloat(sess.Metadata, metadataCurrentContextTokens)
	if currentTokens <= 0 {
		return llm.TokenUsage{}, false, nil
	}

	usagePercent := (currentTokens / float64(cfg.ContextWindow)) * 100.0
	if usagePercent < cfg.TriggerPercent {
		return llm.TokenUsage{}, false, nil
	}

	// If the latest message is a user prompt awaiting the next response, keep it after compaction.
	var pendingUser *session.Message
	if len(sess.Messages) > 0 && sess.Messages[len(sess.Messages)-1].Role == "user" {
		last := sess.Messages[len(sess.Messages)-1]
		pendingUser = &last
		sess.Messages = sess.Messages[:len(sess.Messages)-1]
	}

	// Calculate which messages to summarize and which to keep
	// We want to keep the last N messages raw to preserve context
	const parserKeepMessages = 2

	activeMessages := a.getActiveConversationMessages(sess)
	if len(activeMessages) == 0 {
		if pendingUser != nil {
			sess.AddMessage(*pendingUser)
		}
		return llm.TokenUsage{}, false, nil
	}

	var messagesToSummarize []session.Message
	var messagesToKeep []session.Message

	// If we have enough messages, split them. Otherwise, summarize everything (fallback)
	// But usually we should have at least keep + 1 to make compaction worth it.
	// If len <= parserKeepMessages, we compact everything to avoid infinite loops if messages are just huge.
	if len(activeMessages) > parserKeepMessages {
		splitIdx := len(activeMessages) - parserKeepMessages
		messagesToSummarize = activeMessages[:splitIdx]
		messagesToKeep = activeMessages[splitIdx:]
	} else {
		messagesToSummarize = activeMessages
		messagesToKeep = []session.Message{}
	}

	request := a.buildCompactionRequestFromMessages(messagesToSummarize, cfg.Prompt)
	if len(request.Messages) == 0 {
		// Nothing to summarize? Should not happen if logic above is correct
		if pendingUser != nil {
			sess.AddMessage(*pendingUser)
		}
		return llm.TokenUsage{}, false, nil
	}

	logging.Info("Context compaction starting: session=%s messages_to_summarize=%d", sess.ID, len(messagesToSummarize))

	response, err := a.llmClient.Chat(ctx, request)
	if err != nil {
		logging.Warn("Context compaction LLM error: %v", err)
		if pendingUser != nil {
			sess.AddMessage(*pendingUser)
		}
		return llm.TokenUsage{}, false, fmt.Errorf("compaction LLM error: %w", err)
	}

	logging.Info("Context compaction LLM response: content_len=%d usage=%+v", len(response.Content), response.Usage)

	a.addTokenUsageMetadata(sess, response.Usage)
	metadataSetFloat(sess, metadataCurrentContextTokens, 0)

	compactionCount := int(metadataFloat(sess.Metadata, metadataCompactionCount)) + 1
	metadataSetFloat(sess, metadataCompactionCount, float64(compactionCount))
	metadataSetString(sess, metadataLastCompactionAt, time.Now().UTC().Format(time.RFC3339))

	summary := strings.TrimSpace(response.Content)
	if summary == "" {
		logging.Warn("Context compaction returned empty content, using fallback")
		summary = "Context was compacted to continue in a fresh window."
	}

	// Create summary message
	summaryMsg := session.Message{
		ID:        "", // will be generated
		Role:      "assistant",
		Content:   summary,
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			messageMetadataCompaction: true,
			"compaction_index":        compactionCount,
			"trigger_percent":         cfg.TriggerPercent,
			"triggered_at_step":       step,
		},
	}

	// Insert summary message BEFORE the kept messages.
	// We need to find the insertion point in the original sess.Messages.
	// activeMessages is a slice of sess.Messages.
	// The split point in activeMessages corresponds to `splitIdx`.
	// However, since we might have popped pendingUser, we just need to append Summary then Kept?
	// No, we want to maintain history.

	// Strategy: Rebuild sess.Messages.
	// [ ... (Old Inactive) ... , (Summarized Active) ... , (Kept Active) ... ]
	// becomes
	// [ ... (Old Inactive) ... , (Summarized Active) ... , NEW_SUMMARY, (Kept Active) ... ]

	// Finding the index of the first kept message in the main slice
	insertionIdx := len(sess.Messages) // Default to end
	if len(messagesToKeep) > 0 {
		// Find the index of the first kept message
		// Since activeMessages is a slice of sess.Messages, and messagesToKeep is a slice of activeMessages,
		// they share the same backing array (mostly). But safely, let's just use the ID or reference.
		firstKeptID := messagesToKeep[0].ID
		for i, m := range sess.Messages {
			if m.ID == firstKeptID {
				insertionIdx = i
				break
			}
		}
	}

	// Do the insertion
	// Extend slice
	sess.Messages = append(sess.Messages, session.Message{})
	copy(sess.Messages[insertionIdx+1:], sess.Messages[insertionIdx:])
	sess.Messages[insertionIdx] = summaryMsg

	// Ensure ID is generated
	if sess.Messages[insertionIdx].ID == "" {
		sess.Messages[insertionIdx].ID = uuid.New().String()
	}

	if pendingUser != nil {
		sess.AddMessage(*pendingUser)
	} else {
		// Add a synthetic user message to prompt the agent to continue working.
		// Without this, the LLM may interpret the compaction summary as a final response
		// and return without tool calls, causing premature completion.
		sess.AddMessage(session.Message{
			Role:      "user",
			Content:   "Continue with the task based on the summary above.",
			Timestamp: time.Now(),
			Metadata: map[string]interface{}{
				"synthetic_continuation": true,
			},
		})
	}

	if err := a.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to save compacted session state: %v", err)
	}

	logging.Info("Context compaction completed: session=%s current_tokens=%.0f threshold=%.1f%% kept=%d", sess.ID, currentTokens, cfg.TriggerPercent, len(messagesToKeep))
	return response.Usage, true, nil
}

func (a *Agent) getActiveConversationMessages(sess *session.Session) []session.Message {
	if sess == nil || len(sess.Messages) == 0 {
		return nil
	}

	start := 0
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if isCompactionMessage(sess.Messages[i]) {
			start = i
			break
		}
	}
	return sess.Messages[start:]
}

func isCompactionMessage(msg session.Message) bool {
	if msg.Metadata == nil {
		return false
	}
	raw, ok := msg.Metadata[messageMetadataCompaction]
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}

func (a *Agent) resolveCompactionConfig() compactionConfig {
	contextWindow := a.config.ContextWindow
	if contextWindow <= 0 {
		return compactionConfig{Enabled: false}
	}

	trigger := a.config.CompactionTriggerPercent
	if trigger <= 0 {
		trigger = defaultCompactionTriggerPct
	}
	if envTrigger := strings.TrimSpace(os.Getenv(envCompactionTriggerPercent)); envTrigger != "" {
		if parsed, err := strconv.ParseFloat(envTrigger, 64); err == nil {
			trigger = parsed
		}
	}
	if trigger <= 0 {
		return compactionConfig{Enabled: false}
	}
	if trigger > 100 {
		trigger = 100
	}

	prompt := strings.TrimSpace(a.config.CompactionPrompt)
	if envPrompt := strings.TrimSpace(os.Getenv(envCompactionPrompt)); envPrompt != "" {
		prompt = envPrompt
	}
	if prompt == "" {
		prompt = defaultCompactionPrompt
	}

	return compactionConfig{
		Enabled:        true,
		ContextWindow:  contextWindow,
		TriggerPercent: trigger,
		Prompt:         prompt,
	}
}

func (a *Agent) addTokenUsageMetadata(sess *session.Session, usage llm.TokenUsage) {
	if sess == nil {
		return
	}

	// Get previous context size before updating
	prevContext := metadataFloat(sess.Metadata, metadataCurrentContextTokens)

	// OutputTokens are new tokens generated in this step - accumulate them
	metadataSetFloat(sess, metadataTotalOutputTokens, metadataFloat(sess.Metadata, metadataTotalOutputTokens)+float64(usage.OutputTokens))

	// InputTokens from API represent the FULL context size (system prompt + all history)
	// NOT incremental tokens, so we should NOT accumulate them.
	// Store the current full context size.
	metadataSetFloat(sess, metadataCurrentContextTokens, float64(usage.InputTokens))

	// For total_input_tokens, we track cumulative input by calculating the delta:
	// new tokens = current input - previous context size
	if prevContext == 0 {
		// First call - use InputTokens as-is (includes system prompt)
		metadataSetFloat(sess, metadataTotalInputTokens, float64(usage.InputTokens))
	} else {
		// Subsequent calls - add only the delta (new user message + new output from previous step)
		deltaTokens := float64(usage.InputTokens) - prevContext
		if deltaTokens > 0 {
			metadataSetFloat(sess, metadataTotalInputTokens, metadataFloat(sess.Metadata, metadataTotalInputTokens)+deltaTokens)
		}
	}
}

func metadataFloat(metadata map[string]interface{}, key string) float64 {
	if metadata == nil {
		return 0
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return 0
	}
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case int32:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f
	default:
		return 0
	}
}

func metadataSetFloat(sess *session.Session, key string, value float64) {
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	sess.Metadata[key] = value
}

func metadataSetString(sess *session.Session, key string, value string) {
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	sess.Metadata[key] = value
}

// DefaultSystemPrompt returns the built-in baseline system prompt.
func DefaultSystemPrompt() string {
	return defaultSystemPrompt
}

// DefaultSystemPromptWithoutBuiltInTools returns the baseline prompt without built-in tool guidance.
func DefaultSystemPromptWithoutBuiltInTools() string {
	return defaultSystemPromptWithoutBuiltInTools
}

// defaultSystemPrompt is the default system prompt for the agent
const defaultSystemPrompt = `You are an AI coding assistant. You help users with software engineering tasks by using the available tools.

Guidelines:
- Use tools to explore and modify the codebase
- Read files before editing to understand context
- Make minimal, targeted changes
- When tasks are independent, issue multiple tool calls in one response so they can run in parallel
- Explain your reasoning before making changes
- If a task is unclear, ask for clarification
- If you encounter errors, try to understand and fix them

Available tools allow you to:
- Execute shell commands (bash)
- Read file contents (read)
- Write new files (write)
- Edit existing files with string replacement (edit)
- Replace exact line ranges (replace_lines)
- Insert lines at specific positions (insert_lines)
- Find files by pattern (glob)
- Find files with include/exclude filters (find_files)
- Search file contents (grep)

Be concise but thorough. Complete the user's task step by step.`

const defaultSystemPromptWithoutBuiltInTools = `You are an AI coding assistant. You help users with software engineering tasks.

Guidelines:
- Explore and modify the codebase as needed
- Read files before editing to understand context
- Make minimal, targeted changes
- When tasks are independent, issue multiple tool calls in one response so they can run in parallel
- Explain your reasoning before making changes
- If a task is unclear, ask for clarification
- If you encounter errors, try to understand and fix them

Be concise but thorough. Complete the user's task step by step.`

func (a *Agent) buildCompactionRequestFromMessages(messagesToSummarize []session.Message, prompt string) *llm.ChatRequest {
	var sb strings.Builder
	sb.WriteString("Here is the conversation history to summarize:\n\n")

	for _, m := range messagesToSummarize {
		switch m.Role {
		case "user":
			sb.WriteString("USER:\n")
			if m.Content != "" {
				sb.WriteString(m.Content)
				sb.WriteString("\n")
			}
		case "assistant":
			sb.WriteString("ASSISTANT:\n")
			if m.Content != "" {
				sb.WriteString(m.Content)
				sb.WriteString("\n")
			}
			for _, tc := range m.ToolCalls {
				sb.WriteString(fmt.Sprintf("[Called tool: %s]\n", tc.Name))
			}
		case "tool":
			for _, tr := range m.ToolResults {
				if tr.IsError {
					sb.WriteString(fmt.Sprintf("[Tool error: %s]\n", truncateForCompaction(tr.Content, 500)))
				} else {
					sb.WriteString(fmt.Sprintf("[Tool result: %s]\n", truncateForCompaction(tr.Content, 500)))
				}
			}
		}
		sb.WriteString("\n")
	}

	userMessage := llm.Message{
		Role:    "user",
		Content: sb.String(),
	}

	return &llm.ChatRequest{
		Model:        a.config.Model,
		Messages:     []llm.Message{userMessage},
		Temperature:  0.2,
		MaxTokens:    4096,
		SystemPrompt: prompt,
	}
}

func truncateForCompaction(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
