package tui

import (
	"github.com/A2gent/brute/internal/session"
	"time"
)

type (
	tickMsg time.Time

	agentResponseMsg struct {
		content      string
		done         bool
		err          error
		inputTokens  int
		outputTokens int
	}

	tokenUpdateMsg struct {
		inputTokens  int
		outputTokens int
	}

	memoryUpdateMsg struct {
		memoryMB float64
	}

	serverPortMsg struct {
		port int
	}

	sessionSyncMsg struct {
		session *session.Session
	}
)
type message struct {
	role        string
	content     string
	timestamp   time.Time
	toolCalls   []session.ToolCall
	toolResults []session.ToolResult
	metadata    map[string]interface{}
}
