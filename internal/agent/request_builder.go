package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/claudecli"
	"github.com/A2gent/brute/internal/session"
)

// buildRequest builds a chat request from the session
func (a *Agent) buildRequest(sess *session.Session) *llm.ChatRequest {
	// Convert session messages to LLM messages
	activeMessages := a.getActiveConversationMessages(sess)
	previousResponseID := ""
	if a.config.UsePreviousResponse {
		previousResponseID = lastResponseIDForStatefulRequest(sess)
	}
	if previousResponseID != "" {
		activeMessages = messagesAfterResponseID(activeMessages, previousResponseID)
	}
	providerSessionCursor := ""
	if a.config.UseProviderSession {
		storedIdentity := lastProviderSessionIdentityForRequest(sess)
		storedCursor := lastProviderSessionCursorForRequest(sess)
		configIdentity := strings.TrimSpace(a.config.ProviderSessionIdentity)
		if storedCursor != "" && storedIdentity == configIdentity {
			providerSessionCursor = claudecli.BindProviderSessionCursor(configIdentity, storedCursor)
			activeMessages = messagesAfterProviderSessionCursor(activeMessages, storedCursor)
		}
	}
	messages := make([]llm.Message, 0, len(activeMessages))

	for _, m := range activeMessages {
		msg := llm.Message{
			Role:    m.Role,
			Content: m.Content,
			Images:  sessionImagesToLLM(m.Images),
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
					DurationMs: tr.DurationMs,
				}
			}
		}

		messages = append(messages, msg)
	}

	request := &llm.ChatRequest{
		Model:                 a.config.Model,
		Messages:              messages,
		Tools:                 a.toolManager.GetDefinitions(),
		Temperature:           a.config.Temperature,
		SystemPrompt:          a.config.SystemPrompt,
		SessionID:             sess.ID,
		PreviousResponseID:    previousResponseID,
		ProviderSessionCursor: providerSessionCursor,
	}
	if a.config.CompressToolResults && a.compressor != nil {
		compressed, _ := a.compressor.CompressRequest(context.Background(), sess.ID, request)
		return compressed
	}
	return request
}

func sessionImagesToLLM(images []session.ImageAttachment) []llm.Image {
	if len(images) == 0 {
		return nil
	}
	out := make([]llm.Image, 0, len(images))
	for _, img := range images {
		out = append(out, llm.Image{
			Name:       img.Name,
			MediaType:  img.MediaType,
			DataBase64: img.DataBase64,
			URL:        img.URL,
		})
	}
	return out
}

func llmImagesToSession(images []llm.Image) []session.ImageAttachment {
	if len(images) == 0 {
		return nil
	}
	out := make([]session.ImageAttachment, 0, len(images))
	for _, img := range images {
		out = append(out, session.ImageAttachment{
			Name:       img.Name,
			MediaType:  img.MediaType,
			DataBase64: img.DataBase64,
			URL:        img.URL,
		})
	}
	return out
}

// DefaultSystemPrompt returns the built-in baseline system prompt.
func DefaultSystemPrompt() string {
	return defaultSystemPrompt
}

// DefaultSystemPromptWithoutBuiltInTools returns the baseline prompt without built-in tool guidance.
func DefaultSystemPromptWithoutBuiltInTools() string {
	return defaultSystemPromptWithoutBuiltInTools
}

// DefaultBuiltInToolsGuidance returns the built-in tools guidance section.
func DefaultBuiltInToolsGuidance() string {
	return defaultBuiltInToolsGuidance
}

// defaultSystemPrompt is the default system prompt for the agent
const defaultSystemPrompt = `You are an AI coding assistant. You help users with software engineering tasks by using the available tools.

Guidelines:
- Use tools to explore and modify the codebase
- Read files before editing to understand context
- Make minimal, targeted changes
- When tasks are independent, always issue multiple tool calls in one response so they run in parallel
- During codebase exploration, batch independent reads/searches early: prefer 3-6 parallel grep/read/find_files/bash calls before reasoning again
- If your model or provider emits only one tool call at a time, use the parallel tool to run independent exploration or delegation steps concurrently
- Explain your reasoning before making changes
- If a task is unclear, ask for clarification
- If you encounter errors, try to understand and fix them

Available built-in tools:
- man
- bash
- code_execution
- pipeline
- parallel
- read
- write
- edit
- replace_lines
- insert_lines
- file_search
- content_search
- glob
- find_files
- grep
- filter
- suggest_session
- suggest_git_commit

Use man with a tool name to read detailed usage and input schema only when needed.

Output/widget conventions:
- Reference project files as plain text paths with optional line ranges, e.g. src/app.ts:42 or src/app.ts:42-48. Avoid wrapping file references in code when they should be clickable in UI.
- When you finish a task that changed files and the changes are ready for the user to commit, call suggest_git_commit exactly once with the commit message and project-relative files that belong to this session. Include the returned a2gent-git-commit block in your final answer. Do not include unrelated files from other concurrent sessions, and do not claim the files were staged or committed.
- For location results, include normal readable addresses first, then add an optional fenced a2gent-map JSON block with {"title":"...","points":[{"label":"...","address":"...","lat":0,"lng":0}]}. The terminal fallback is the address list; Caesar renders the map when coordinates or addresses are present.

Be concise but thorough. Complete the user's task step by step.`

const defaultSystemPromptWithoutBuiltInTools = `You are an AI coding assistant. You help users with software engineering tasks.

Guidelines:
- Explore and modify the codebase as needed
- Read files before editing to understand context
- Make minimal, targeted changes
- When tasks are independent, always issue multiple tool calls in one response so they run in parallel
- During codebase exploration, batch independent reads/searches early: prefer 3-6 parallel grep/read/find_files/bash calls before reasoning again
- If your model or provider emits only one tool call at a time, use the parallel tool to run independent exploration or delegation steps concurrently
- Explain your reasoning before making changes
- If a task is unclear, ask for clarification
- If you encounter errors, try to understand and fix them

Be concise but thorough. Complete the user's task step by step.`

const defaultBuiltInToolsGuidance = `Available built-in tools:
- man
- bash
- code_execution
- pipeline
- parallel
- read
- write
- edit
- replace_lines
- insert_lines
- file_search
- content_search
- glob
- find_files
- grep
- filter
- suggest_session
- suggest_git_commit

Use man with a tool name to read detailed usage and input schema only when needed.

Output/widget conventions:
- Reference project files as plain text paths with optional line ranges, e.g. src/app.ts:42 or src/app.ts:42-48. Avoid wrapping file references in code when they should be clickable in UI.
- When you finish a task that changed files and the changes are ready for the user to commit, call suggest_git_commit exactly once with the commit message and project-relative files that belong to this session. Include the returned a2gent-git-commit block in your final answer. Do not include unrelated files from other concurrent sessions, and do not claim the files were staged or committed.
- For location results, include normal readable addresses first, then add an optional fenced a2gent-map JSON block with {"title":"...","points":[{"label":"...","address":"...","lat":0,"lng":0}]}. The terminal fallback is the address list; Caesar renders the map when coordinates or addresses are present.`

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
