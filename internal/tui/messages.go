package tui

import (
	"github.com/A2gent/brute/internal/agent"
	httpserver "github.com/A2gent/brute/internal/http"
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

	startInitialRunMsg struct {
		task string
	}

	externalSessionEventMsg struct {
		event httpserver.ChatStreamEvent
	}

	agentStreamMsg struct {
		stream      <-chan agentStreamMsg
		event       agent.Event
		hasEvent    bool
		response    agentResponseMsg
		hasResponse bool
	}

	agentStreamClosedMsg struct {
		stream <-chan agentStreamMsg
	}
)
type message struct {
	id          string
	role        string
	content     string
	timestamp   time.Time
	toolCalls   []session.ToolCall
	toolResults []session.ToolResult
	metadata    map[string]interface{}
}
