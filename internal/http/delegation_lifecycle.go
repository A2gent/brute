package http

import (
	"context"
	"fmt"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"net/http"
	"strings"
	"time"
)

func cancelDockerDelegationChild(baseURL, sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := postLocalDockerAgentJSON(ctx, &http.Client{}, baseURL+"/sessions/"+sessionID+"/cancel", map[string]interface{}{}, nil); err != nil {
		logging.Warn("Failed to cancel delegated child session %s: %v", sessionID, err)
	}
}

func streamToolCallNames(calls []StreamToolCallEvent) string {
	if len(calls) == 0 {
		return "none"
	}
	names := make([]string, 0, len(calls))
	for i, call := range calls {
		if i >= 4 {
			names = append(names, "...")
			break
		}
		names = append(names, firstNonEmptyLocalAgentString(strings.TrimSpace(call.Name), "unknown"))
	}
	return strings.Join(names, ",")
}

func emptyDockerDelegationMessage(agentName string, childSessionID string, chatResp ChatResponse) string {
	status := strings.TrimSpace(chatResp.Status)
	if status == "" {
		status = "unknown"
	}
	message := fmt.Sprintf("docker agent %q returned empty response (child session %s, status=%s)", agentName, childSessionID, status)
	if len(chatResp.Messages) == 0 {
		return message
	}

	for i := len(chatResp.Messages) - 1; i >= 0; i-- {
		msg := chatResp.Messages[i]
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}
		content := truncateForLog(strings.TrimSpace(msg.Content), 500)
		return message + "; last non-empty message: " + content
	}

	toolCalls := 0
	toolResults := 0
	lastToolName := ""
	lastToolResultLen := 0
	lastToolErrored := false
	for _, msg := range chatResp.Messages {
		toolCalls += len(msg.ToolCalls)
		for _, result := range msg.ToolResults {
			toolResults++
			lastToolName = strings.TrimSpace(result.Name)
			lastToolResultLen = len(result.Content)
			lastToolErrored = result.IsError
		}
	}
	if toolCalls > 0 || toolResults > 0 {
		// WHAT: expose enough child-session diagnostics to identify a tool loop while
		// avoiding raw tool output, which can be very large or user-sensitive.
		return fmt.Sprintf("%s; child produced no final assistant text after %d tool call(s) and %d tool result(s); last_tool=%s last_tool_result_chars=%d last_tool_error=%t", message, toolCalls, toolResults, firstNonEmptyLocalAgentString(lastToolName, "unknown"), lastToolResultLen, lastToolErrored)
	}
	return message
}

// Unlike a browser chat, a delegated stream belongs to a waiting parent. Tie
// its lifetime to the request even before run registration to close the race
// where the parent's /cancel arrives before there is an active run to cancel.
func (s *Server) chatStreamRunContext(request context.Context, sess *session.Session) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(s.sessionRunParentContext())
	if !isDelegatedSession(sess) {
		return ctx, cancel
	}
	stop := context.AfterFunc(request, cancel)
	return ctx, func() { stop(); cancel() }
}
