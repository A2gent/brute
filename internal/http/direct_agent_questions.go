package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

const directAgentChildCallTimeout = 10 * time.Second

// runDirectAgentTurn wraps the child proxy run with parent bookkeeping. WHY: the
// parent run is only an HTTP proxy, so a question parked in the child was not
// visible on the parent (Caesar shows the parent), and user notes injected into
// the parent while the run was active were overwritten by the final Save.
func (s *Server) runDirectAgentTurn(ctx context.Context, sess *session.Session, task string, onEvent func(ChatStreamEvent)) (ChatResponse, error) {
	var question *session.QuestionData
	resp, err := s.runDirectAgentSession(ctx, sess, task, func(event ChatStreamEvent) {
		// The child's input_required event carries the child transcript; the
		// parent publishes its own once the mirrored question is saved.
		if event.Type == "input_required" {
			if event.Question != nil {
				question = event.Question
			}
			return
		}
		if onEvent != nil {
			onEvent(event)
		}
	})
	if err == nil {
		s.mirrorDirectAgentQuestion(ctx, sess, resp.Status, question)
	}
	// Last step: the caller saves right after, keeping the window for a lost
	// concurrent injection as small as possible.
	s.mergeInjectedParentMessages(sess)
	return resp, err
}

func (s *Server) mergeInjectedParentMessages(sess *session.Session) {
	tailID := ""
	if tail := sess.GetLastMessage(); tail != nil {
		tailID = tail.ID
	}
	fresh, err := s.sessionManager.GetLiveState(sess.ID, tailID)
	if err != nil || fresh == nil {
		return
	}
	known := make(map[string]struct{}, len(sess.Messages))
	for _, msg := range sess.Messages {
		known[msg.ID] = struct{}{}
	}
	for _, msg := range fresh.Messages {
		if _, ok := known[msg.ID]; ok || msg.Role != "user" {
			continue
		}
		known[msg.ID] = struct{}{}
		sess.Messages = append(sess.Messages, msg)
	}
}

func (s *Server) mirrorDirectAgentQuestion(ctx context.Context, sess *session.Session, status string, question *session.QuestionData) {
	if status != string(session.StatusInputRequired) {
		delete(sess.Metadata, "pending_question")
		return
	}
	if question == nil {
		question = s.fetchDirectAgentChildQuestion(ctx, sess)
	}
	if question == nil {
		logging.Warn("Direct agent: child of %s requires input but has no pending question", sess.ID)
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	sess.Metadata["pending_question"] = question
}

func (s *Server) fetchDirectAgentChildQuestion(ctx context.Context, sess *session.Session) *session.QuestionData {
	apiURL, childID, ok := s.directAgentChildEndpoint(sess)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, directAgentChildCallTimeout)
	defer cancel()
	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, apiURL+"/sessions/"+childID+"/question", nil)
	if err != nil {
		return nil
	}
	resp, err := directAgentChildClient().Do(req)
	if err != nil {
		logging.Warn("Direct agent: cannot read question of child %s: %v", childID, err)
		return nil
	}
	defer resp.Body.Close()
	var body struct {
		Question *session.QuestionData `json:"question"`
	}
	if resp.StatusCode != nethttp.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) != nil {
		return nil
	}
	return body.Question
}

// forwardDirectAgentInjection delivers a note injected into a running parent to
// its child agent, the run that actually consumes user input.
func (s *Server) forwardDirectAgentInjection(ctx context.Context, sess *session.Session, content string, images []session.ImageAttachment) error {
	apiURL, childID, ok := s.directAgentChildEndpoint(sess)
	if !ok {
		return fmt.Errorf("no direct agent child for session %s", sess.ID)
	}
	ctx, cancel := context.WithTimeout(ctx, directAgentChildCallTimeout)
	defer cancel()
	payload := map[string]interface{}{"message": content, "images": inlineImagePayloads(sess.ID, images)}
	return postLocalDockerAgentJSON(ctx, directAgentChildClient(), apiURL+"/sessions/"+childID+"/inject", payload, nil)
}

// steerDirectAgentInjection forwards an injected note when the session is a
// direct-agent proxy with an active run; otherwise it is a no-op.
func (s *Server) steerDirectAgentInjection(sess *session.Session, content string, images []session.ImageAttachment) {
	if sess == nil || !s.hasDirectAgentTarget(sess) || s.activeSessionRunCount(sess.ID) == 0 {
		return
	}
	if err := s.forwardDirectAgentInjection(s.sessionRunParentContext(), sess, content, images); err != nil {
		logging.Warn("Direct agent: failed to forward injected message of %s: %v", sess.ID, err)
	}
}

// directAgentChildEndpoint returns the newest direct child of sess, reachable
// only over the loopback API URL recorded when the child was created.
func (s *Server) directAgentChildEndpoint(sess *session.Session) (string, string, bool) {
	if sess == nil || sess.Metadata == nil {
		return "", "", false
	}
	entries := dockerChildSessionEntries(sess.Metadata["docker_child_sessions"])
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		childID := strings.TrimSpace(metadataString(entry["child_session_id"]))
		apiURL := strings.TrimRight(strings.TrimSpace(metadataString(entry["agent_api_url"])), "/")
		if parent := metadataString(entry["parent_session_id"]); childID == "" || (parent != "" && parent != sess.ID) || !isLoopbackAPIURL(apiURL) || s.isSelfAPIURL(apiURL) {
			continue
		}
		return apiURL, childID, true
	}
	return "", "", false
}

func directAgentChildClient() *nethttp.Client {
	return &nethttp.Client{CheckRedirect: func(*nethttp.Request, []*nethttp.Request) error { return nethttp.ErrUseLastResponse }}
}

func inlineImagePayloads(sessionID string, images []session.ImageAttachment) []MessageImagePayload {
	var out []MessageImagePayload
	for _, img := range images {
		resolved, err := img.Inline()
		if err != nil {
			logging.Warn("Direct agent: cannot load image %s of %s: %v", img.Path, sessionID, err)
			continue
		}
		out = append(out, MessageImagePayload{
			Name:       strings.TrimSpace(resolved.Name),
			MediaType:  strings.TrimSpace(resolved.MediaType),
			DataBase64: strings.TrimSpace(resolved.DataBase64),
			URL:        strings.TrimSpace(resolved.URL),
		})
	}
	return out
}
