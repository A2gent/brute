package http

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/A2gent/brute/internal/session"
)

var agentMentionPattern = regexp.MustCompile(`(^|\s)@([\w.-]+)`)

const (
	mentionContextMessages = 12
	mentionContextChars    = 1500
)

type agentMention struct {
	AgentID   string
	AgentName string
	Task      string
}

// resolveAgentMention finds the first "@agent" in the message that names a
// configured agent visible to the session. Unknown handles stay plain text so
// e-mail addresses or @-references in code do not hijack the turn.
func (s *Server) resolveAgentMention(sess *session.Session, message string) *agentMention {
	if !strings.Contains(message, "@") {
		return nil
	}
	agents, err := s.configuredDelegationAgents(sessionProjectIDForPrompt(sess))
	if err != nil {
		return nil
	}
	for _, loc := range agentMentionPattern.FindAllStringSubmatchIndex(message, -1) {
		handle := message[loc[4]:loc[5]]
		for _, agent := range agents {
			if !strings.EqualFold(agent.ID, handle) && !strings.EqualFold(agent.Name, handle) {
				continue
			}
			before := strings.TrimRight(message[:loc[4]-1], " \t")
			after := strings.TrimLeft(message[loc[5]:], " \t")
			task := strings.TrimSpace(before + " " + after)
			return &agentMention{AgentID: agent.ID, AgentName: agent.Name, Task: task}
		}
	}
	return nil
}

// mentionDelegationTask gives the mentioned agent the recent conversation so a
// request like "@reviewer take a look" has something to look at; the child
// session does not share this session's history.
func mentionDelegationTask(sess *session.Session, m *agentMention) string {
	var b strings.Builder
	b.WriteString("You were asked for your opinion inside another conversation. Recent messages:\n\n")
	start := 0
	if len(sess.Messages) > mentionContextMessages {
		start = len(sess.Messages) - mentionContextMessages
	}
	for _, msg := range sess.Messages[start:] {
		if (msg.Role != "user" && msg.Role != "assistant") || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		content := msg.Content
		if len(content) > mentionContextChars {
			content = content[:mentionContextChars] + "..."
		}
		fmt.Fprintf(&b, "[%s]: %s\n\n", msg.Role, content)
	}
	b.WriteString("Request for you:\n")
	b.WriteString(m.Task)
	return b.String()
}

func (s *Server) runAgentMention(ctx context.Context, sess *session.Session, m *agentMention) (string, error) {
	taskCtx := context.WithValue(ctx, "session_id", sess.ID)
	result, err := newDelegateToAgentTool(s).dispatch(taskCtx, m.AgentID, mentionDelegationTask(sess, m))
	if err != nil {
		return "", err
	}
	if !result.Success {
		return "", fmt.Errorf("%s", result.Error)
	}
	return strings.TrimSpace(result.Output), nil
}

func mentionReplyMetadata(m *agentMention) map[string]interface{} {
	return map[string]interface{}{"mentioned_agent_id": m.AgentID, "mentioned_agent_name": m.AgentName}
}
