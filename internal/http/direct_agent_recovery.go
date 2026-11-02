package http

import (
	"context"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

// directAgentRecoveryTimeout bounds the child lookup after a stopped or failed
// direct-agent run, including the wait for the child to save its partial turn.
const directAgentRecoveryTimeout = 5 * time.Second

const directAgentRecoveryPoll = 100 * time.Millisecond

// canceledRunSettleTimeout bounds how long a Stop request waits for the
// canceled run to save its final state.
const canceledRunSettleTimeout = 3 * time.Second

func (s *Server) waitForCanceledRunsToSettle(ctx context.Context, sessionID string) {
	ctx, cancel := context.WithTimeout(ctx, canceledRunSettleTimeout)
	defer cancel()
	for {
		sess, err := s.sessionManager.GetSummary(sessionID)
		if err != nil || sess.Status != session.StatusRunning {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(directAgentRecoveryPoll / 2):
		}
	}
}

// persistInterruptedDirectAgentTurn copies the child's partial turn into the
// parent when a direct-agent run is stopped or fails. WHY: activity was copied
// only on success, so a stopped parent kept just the request and the agent's
// work disappeared from view. The run context is already canceled, so the child
// is read with a fresh one. stop also cancels the child: without the parent
// tools header it would otherwise keep working after the user pressed Stop.
func (s *Server) persistInterruptedDirectAgentTurn(sess *session.Session, resp ChatResponse, stop bool) {
	addDirectAgentUsage(sess, resp.Usage)
	activity := directAgentInFlightActivity(resp.Messages)
	if len(activity) == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), directAgentRecoveryTimeout)
		defer cancel()
		child, ok := s.settledDirectAgentChild(ctx, sess, stop)
		if !ok {
			return
		}
		activity = directAgentInFlightActivity(child.Messages)
	}
	if directAgentActivityAlreadyCopied(sess.Messages, activity) {
		return
	}
	for _, m := range activity {
		m.Metadata[directAgentActivityMarker] = true
		sess.AddMessage(sessionMessageFromResponse(m))
	}
}

// settledDirectAgentChild returns the newest direct child of sess once it has
// stopped running (or the last snapshot when ctx expires).
func (s *Server) settledDirectAgentChild(ctx context.Context, sess *session.Session, stop bool) (SessionResponse, bool) {
	entries := dockerChildSessionEntries(sess.Metadata["docker_child_sessions"])
	for i := len(entries) - 1; i >= 0; i-- {
		childID := strings.TrimSpace(metadataString(entries[i]["child_session_id"]))
		apiURL := strings.TrimSpace(metadataString(entries[i]["agent_api_url"]))
		if parent := metadataString(entries[i]["parent_session_id"]); childID == "" || (parent != "" && parent != sess.ID) || !isLoopbackAPIURL(apiURL) || s.isSelfAPIURL(apiURL) {
			continue
		}
		client := &nethttp.Client{Timeout: directAgentRecoveryTimeout, CheckRedirect: func(*nethttp.Request, []*nethttp.Request) error { return nethttp.ErrUseLastResponse }}
		var last SessionResponse
		found, cancelSent := false, false
		for {
			child, err := fetchDockerSession(ctx, client, dockerSessionCandidate{APIURL: apiURL}, childID, true)
			if err == nil {
				if metadataString(child.Metadata["source"]) != "direct_agent_session" || metadataString(child.Metadata["parent_session_id"]) != sess.ID {
					break
				}
				last, found = child, true
				if child.Status != string(session.StatusRunning) {
					return child, true
				}
				if stop && !cancelSent {
					cancelSent = true
					if err := postLocalDockerAgentJSON(ctx, client, strings.TrimRight(apiURL, "/")+"/sessions/"+childID+"/cancel", map[string]interface{}{}, nil); err != nil {
						logging.Warn("Direct agent: failed to stop child %s of %s: %v", childID, sess.ID, err)
					}
				}
			} else {
				logging.Debug("Direct agent: recovery lookup of child %s for %s failed: %v", childID, sess.ID, err)
			}
			select {
			case <-ctx.Done():
				return last, found
			case <-time.After(directAgentRecoveryPoll):
			}
		}
	}
	return SessionResponse{}, false
}

// directAgentActivityAlreadyCopied reports whether the child's newest turn is
// already in the parent. WHY: a run stopped before the child received the
// request leaves the child's previous, already copied turn as its newest one.
func directAgentActivityAlreadyCopied(parent []session.Message, activity []MessageResponse) bool {
	seen := map[string]bool{}
	for _, m := range parent {
		if id, _ := m.Metadata["runtime_turn_id"].(string); id != "" {
			seen[id] = true
		}
	}
	for _, m := range activity {
		if id, _ := m.Metadata["runtime_turn_id"].(string); id != "" && seen[id] {
			return true
		}
	}
	return false
}
