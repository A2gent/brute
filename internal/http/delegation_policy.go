package http

import (
	"context"
	"strings"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
)

var delegationToolNames = []string{"delegate_to_agent", "delegate_to_subagent", "delegate_to_external_agent"}

func isDelegatedSession(sess *session.Session) bool {
	if sess == nil {
		return false
	}
	if id, _ := sess.Metadata["sub_agent_id"].(string); strings.TrimSpace(id) != "" {
		return true
	}
	source, _ := sess.Metadata["source"].(string)
	for _, name := range delegationToolNames {
		if source == name {
			return true
		}
	}
	return false
}

// Enforce at dispatch too: wrappers and aliases must not bypass the session's
// one-hop policy by calling a registered tool directly.
func (s *Server) delegationDenied(ctx context.Context) *tools.Result {
	id, _ := ctx.Value("session_id").(string)
	if id == "" {
		return nil
	}
	sess, err := s.sessionManager.Get(id)
	if err != nil {
		return &tools.Result{Success: false, Error: "delegation parent session is unavailable"}
	}
	if isDelegatedSession(sess) {
		return &tools.Result{Success: false, Error: "nested delegation is disabled for delegated sessions"}
	}
	return nil
}

func isDelegationTool(name string) bool {
	for _, candidate := range delegationToolNames {
		if name == candidate {
			return true
		}
	}
	return false
}
