package session

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/google/uuid"
)

// Status represents the session status
type Status string

const (
	StatusQueued          Status = "queued" // Session created but not started
	StatusRunning         Status = "running"
	StatusPaused          Status = "paused"
	StatusWaitingExternal Status = "waiting_external" // Agent is waiting for an external webhook/tool callback
	StatusInputRequired   Status = "input_required"   // Agent is waiting for user input
	StatusCompleted       Status = "completed"
	StatusFailed          Status = "failed"
)

// Session represents an agent session
type Session struct {
	ID           string                 `json:"id"`
	AgentID      string                 `json:"agent_id"`
	ParentID     *string                `json:"parent_id,omitempty"`
	JobID        *string                `json:"job_id,omitempty"` // Associated recurring job
	ProjectID    *string                `json:"project_id,omitempty"`
	Title        string                 `json:"title"`
	Status       Status                 `json:"status"`
	Messages     []Message              `json:"messages"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	TaskProgress string                 `json:"task_progress,omitempty"` // Temporary task planning and progress tracking
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

// Message represents a conversation message
type Message struct {
	ID          string                 `json:"id"`
	Role        string                 `json:"role"` // "user", "assistant", "tool"
	Content     string                 `json:"content"`
	Images      []ImageAttachment      `json:"images,omitempty"`
	ToolCalls   []ToolCall             `json:"tool_calls,omitempty"`
	ToolResults []ToolResult           `json:"tool_results,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	Timestamp   time.Time              `json:"timestamp"`
}

// ImageAttachment represents an image attached to a user/assistant message.
type ImageAttachment struct {
	Name       string `json:"name,omitempty"`
	MediaType  string `json:"media_type,omitempty"`
	DataBase64 string `json:"data_base64,omitempty"`
	URL        string `json:"url,omitempty"`
}

// ToolCall represents a tool invocation request
type ToolCall struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Input            json.RawMessage `json:"input"`
	ThoughtSignature string          `json:"thought_signature,omitempty"` // Gemini-specific
}

// ToolResult represents the result of a tool execution
type ToolResult struct {
	ToolCallID string                 `json:"tool_call_id"`
	Content    string                 `json:"content"`
	IsError    bool                   `json:"is_error,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	Name       string                 `json:"name,omitempty"` // Tool name (required by Gemini)
	DurationMs int64                  `json:"duration_ms,omitempty"`
}

// New creates a new session
func New(agentID string) *Session {
	return NewWithStatus(agentID, StatusRunning)
}

// NewWithStatus creates a new session with a specific status
func NewWithStatus(agentID string, status Status) *Session {
	now := time.Now()
	return &Session{
		ID:        uuid.New().String(),
		AgentID:   agentID,
		Status:    status,
		Messages:  make([]Message, 0),
		Metadata:  make(map[string]interface{}),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// NewQueued creates a new queued session (not started)
func NewQueued(agentID string) *Session {
	return NewWithStatus(agentID, StatusQueued)
}

// NewWithParent creates a new sub-session with a parent
func NewWithParent(agentID string, parentID string) *Session {
	sess := New(agentID)
	sess.ParentID = &parentID
	return sess
}

// NewWithJob creates a new session associated with a recurring job
func NewWithJob(agentID string, jobID string) *Session {
	sess := New(agentID)
	sess.JobID = &jobID
	return sess
}

// AddMessage adds a message to the session
func (s *Session) AddMessage(msg Message) {
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	s.Messages = append(s.Messages, msg)
	s.UpdatedAt = time.Now()
}

// AddUserMessage adds a user message
func (s *Session) AddUserMessage(content string) {
	s.AddUserMessageWithImages(content, nil)
}

// AddUserMessageWithImages adds a user message with optional image attachments.
func (s *Session) AddUserMessageWithImages(content string, images []ImageAttachment) {
	s.AddUserMessageWithImagesAndMetadata(content, images, nil)
}

// AddUserMessageWithImagesAndMetadata adds a user-role message with optional images and metadata.
// The role remains "user" because LLM providers expect delegated tasks as user input, but
// metadata lets the UI distinguish real user text from internal handoffs.
func (s *Session) AddUserMessageWithImagesAndMetadata(content string, images []ImageAttachment, metadata map[string]interface{}) {
	if s.Title == "" {
		title := titleFromFirstPrompt(content)
		if title == "" && len(images) > 0 {
			title = "Image request"
		}
		s.SetTitle(title)
	}

	s.AddMessage(Message{
		Role:     "user",
		Content:  content,
		Images:   images,
		Metadata: metadata,
	})
}

func titleFromFirstPrompt(prompt string) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(prompt)), " ")
	if normalized == "" {
		return ""
	}

	const maxTitleLength = 60
	runes := []rune(normalized)
	if len(runes) <= maxTitleLength {
		return normalized
	}

	return string(runes[:maxTitleLength-3]) + "..."
}

// AddAssistantMessage adds an assistant message
func (s *Session) AddAssistantMessage(content string, toolCalls []ToolCall) {
	s.AddAssistantMessageWithImagesAndMetadata(content, nil, toolCalls, nil)
}

// AddAssistantMessageWithMetadata adds an assistant message with optional metadata.
func (s *Session) AddAssistantMessageWithMetadata(content string, toolCalls []ToolCall, metadata map[string]interface{}) {
	s.AddAssistantMessageWithImagesAndMetadata(content, nil, toolCalls, metadata)
}

// AddAssistantMessageWithImagesAndMetadata adds an assistant message with optional images and metadata.
func (s *Session) AddAssistantMessageWithImagesAndMetadata(content string, images []ImageAttachment, toolCalls []ToolCall, metadata map[string]interface{}) {
	s.AddMessage(Message{
		Role:      "assistant",
		Content:   content,
		Images:    images,
		ToolCalls: toolCalls,
		Metadata:  metadata,
	})
}

// AddToolResult adds tool results
func (s *Session) AddToolResult(results []ToolResult) {
	s.AddMessage(Message{
		Role:        "tool",
		ToolResults: results,
	})
}

// GetLastMessage returns the last message
func (s *Session) GetLastMessage() *Message {
	if len(s.Messages) == 0 {
		return nil
	}
	return &s.Messages[len(s.Messages)-1]
}

// SetStatus updates the session status
func (s *Session) SetStatus(status Status) {
	s.Status = status
	s.UpdatedAt = time.Now()
}

// SetTitle sets the session title
func (s *Session) SetTitle(title string) {
	s.Title = title
	s.UpdatedAt = time.Now()
}

// ToStorage converts to storage format
func (s *Session) ToStorage() *storage.Session {
	messages := make([]storage.Message, len(s.Messages))
	for i, m := range s.Messages {
		toolCalls, _ := json.Marshal(m.ToolCalls)
		toolResults, _ := json.Marshal(m.ToolResults)
		messages[i] = storage.Message{
			ID:          m.ID,
			Role:        m.Role,
			Content:     m.Content,
			ToolCalls:   toolCalls,
			ToolResults: toolResults,
			Metadata:    metadataWithImages(m.Metadata, m.Images),
			Timestamp:   m.Timestamp,
		}
	}

	return &storage.Session{
		ID:           s.ID,
		AgentID:      s.AgentID,
		ParentID:     s.ParentID,
		JobID:        s.JobID,
		ProjectID:    s.ProjectID,
		Title:        s.Title,
		Status:       string(s.Status),
		Messages:     messages,
		Metadata:     s.Metadata,
		TaskProgress: s.TaskProgress,
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    s.UpdatedAt,
	}
}

// FromStorage creates a Session from storage format
func FromStorage(ss *storage.Session) *Session {
	messages := make([]Message, len(ss.Messages))
	for i, m := range ss.Messages {
		var toolCalls []ToolCall
		var toolResults []ToolResult
		images := extractImagesFromMetadata(m.Metadata)
		metadata := stripImagesFromMetadata(m.Metadata)
		json.Unmarshal(m.ToolCalls, &toolCalls)
		json.Unmarshal(m.ToolResults, &toolResults)

		messages[i] = Message{
			ID:          m.ID,
			Role:        m.Role,
			Content:     m.Content,
			Images:      images,
			ToolCalls:   toolCalls,
			ToolResults: toolResults,
			Metadata:    metadata,
			Timestamp:   m.Timestamp,
		}
	}

	return &Session{
		ID:           ss.ID,
		AgentID:      ss.AgentID,
		ParentID:     ss.ParentID,
		JobID:        ss.JobID,
		ProjectID:    ss.ProjectID,
		Title:        ss.Title,
		Status:       Status(ss.Status),
		Messages:     messages,
		Metadata:     ss.Metadata,
		TaskProgress: ss.TaskProgress,
		CreatedAt:    ss.CreatedAt,
		UpdatedAt:    ss.UpdatedAt,
	}
}

const messageImagesMetadataKey = "images"

func metadataWithImages(metadata map[string]interface{}, images []ImageAttachment) map[string]interface{} {
	if len(images) == 0 {
		return metadata
	}
	next := cloneMetadata(metadata)
	imageItems := make([]map[string]string, 0, len(images))
	for _, img := range images {
		item := map[string]string{}
		if img.Name != "" {
			item["name"] = img.Name
		}
		if img.MediaType != "" {
			item["media_type"] = img.MediaType
		}
		if img.DataBase64 != "" {
			item["data_base64"] = img.DataBase64
		}
		if img.URL != "" {
			item["url"] = img.URL
		}
		if len(item) > 0 {
			imageItems = append(imageItems, item)
		}
	}
	if len(imageItems) > 0 {
		next[messageImagesMetadataKey] = imageItems
	}
	return next
}

func extractImagesFromMetadata(metadata map[string]interface{}) []ImageAttachment {
	if len(metadata) == 0 {
		return nil
	}
	raw, ok := metadata[messageImagesMetadataKey]
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var images []ImageAttachment
	if err := json.Unmarshal(encoded, &images); err != nil {
		return nil
	}
	return images
}

func cloneMetadata(metadata map[string]interface{}) map[string]interface{} {
	if len(metadata) == 0 {
		return map[string]interface{}{}
	}
	next := make(map[string]interface{}, len(metadata))
	for k, v := range metadata {
		next[k] = v
	}
	return next
}

func stripImagesFromMetadata(metadata map[string]interface{}) map[string]interface{} {
	if len(metadata) == 0 {
		return metadata
	}
	next := cloneMetadata(metadata)
	delete(next, messageImagesMetadataKey)
	return next
}
