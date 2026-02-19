package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/session"
)

// QuestionTool allows agents to ask user for input when encountering ambiguous situations
type QuestionTool struct {
	sessionMetadataStore QuestionSessionStore
}

// QuestionSessionStore interface for storing question data in session
type QuestionSessionStore interface {
	SetPendingQuestion(sessionID string, data *session.QuestionData) error
	SetSessionStatus(sessionID string, status string) error
}

// QuestionParams defines parameters for the question tool
type QuestionParams struct {
	Question string                   `json:"question"`
	Header   string                   `json:"header,omitempty"`
	Options  []session.QuestionOption `json:"options"`
	Multiple bool                     `json:"multiple,omitempty"`
	Custom   *bool                    `json:"custom,omitempty"` // Pointer to distinguish between false and not set
}

// NewQuestionTool creates a new question tool
func NewQuestionTool(store QuestionSessionStore) *QuestionTool {
	return &QuestionTool{
		sessionMetadataStore: store,
	}
}

func (t *QuestionTool) Name() string {
	return "question"
}

func (t *QuestionTool) Description() string {
	return `Ask the user a question when you need their input to decide how to proceed.

Use this when:
- A tool operation times out or hangs (e.g., browser page loading)
- Multiple valid approaches exist and you need user preference
- You encounter an ambiguous situation requiring human judgment
- An operation fails and you need to know whether to retry or skip

The session will pause and wait for user input. Once they respond, execution continues.

Guidelines:
- Use clear, specific questions
- Provide 2-4 actionable options
- Keep option labels short (1-5 words)
- Add descriptions explaining each option
- Enable 'custom' to allow freeform text input (enabled by default)`
}

func (t *QuestionTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"question": map[string]interface{}{
				"type":        "string",
				"description": "The question to ask the user. Be specific and provide context.",
			},
			"header": map[string]interface{}{
				"type":        "string",
				"description": "Short header/title for the question (max 50 characters). If not provided, will be auto-generated.",
			},
			"options": map[string]interface{}{
				"type":        "array",
				"description": "List of answer options to present to the user",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"label": map[string]interface{}{
							"type":        "string",
							"description": "Button text (1-5 words, concise)",
						},
						"description": map[string]interface{}{
							"type":        "string",
							"description": "Explanation of what this option means",
						},
					},
					"required": []string{"label", "description"},
				},
			},
			"multiple": map[string]interface{}{
				"type":        "boolean",
				"description": "Allow selecting multiple options (default: false)",
			},
			"custom": map[string]interface{}{
				"type":        "boolean",
				"description": "Allow user to type a custom answer (default: true)",
			},
		},
		"required": []string{"question", "options"},
	}
}

func (t *QuestionTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p QuestionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	// Validate parameters
	if p.Question == "" {
		return &Result{Success: false, Error: "question is required"}, nil
	}
	if len(p.Options) == 0 {
		return &Result{Success: false, Error: "at least one option is required"}, nil
	}

	// Validate options
	for i, opt := range p.Options {
		if opt.Label == "" {
			return &Result{Success: false, Error: fmt.Sprintf("option %d is missing label", i+1)}, nil
		}
		if opt.Description == "" {
			return &Result{Success: false, Error: fmt.Sprintf("option %d is missing description", i+1)}, nil
		}
	}

	// Auto-generate header if not provided
	header := p.Header
	if header == "" {
		header = generateHeader(p.Question)
	}

	// Default custom to true
	custom := true
	if p.Custom != nil {
		custom = *p.Custom
	}

	// Build question data
	questionData := &session.QuestionData{
		Question: p.Question,
		Header:   header,
		Options:  p.Options,
		Multiple: p.Multiple,
		Custom:   custom,
	}

	// Extract session ID from context
	sessionID := getSessionIDFromContext(ctx)
	if sessionID == "" {
		return &Result{Success: false, Error: "session ID not found in context"}, nil
	}

	// Store question in session metadata
	if err := t.sessionMetadataStore.SetPendingQuestion(sessionID, questionData); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to store question: %v", err)}, nil
	}

	// Change session status to input_required
	if err := t.sessionMetadataStore.SetSessionStatus(sessionID, "input_required"); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to set status: %v", err)}, nil
	}

	// Return success - agent loop will pause when it sees input_required status
	output := fmt.Sprintf("Question asked: %s\nAwaiting user response...", header)
	return &Result{Success: true, Output: output}, nil
}

// generateHeader creates a short header from the question
func generateHeader(question string) string {
	// Take first sentence or first 50 characters
	sentences := strings.Split(question, ".")
	if len(sentences) > 0 {
		header := strings.TrimSpace(sentences[0])
		if len(header) > 50 {
			header = header[:47] + "..."
		}
		return header
	}
	return question[:minInt(50, len(question))]
}

// getSessionIDFromContext extracts session ID from context
// This should match how session ID is stored in agent execution context
func getSessionIDFromContext(ctx context.Context) string {
	// Try to get from context value
	if val := ctx.Value("session_id"); val != nil {
		if sessionID, ok := val.(string); ok {
			return sessionID
		}
	}
	return ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
