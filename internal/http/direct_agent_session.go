package http

import (
	"context"
	"fmt"
	nethttp "net/http"
	"strings"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

func (s *Server) hasDirectAgentTarget(sess *session.Session) bool {
	if sess == nil || sess.Metadata == nil {
		return false
	}
	return strings.TrimSpace(metadataString(sess.Metadata["unified_agent_id"])) != "" || strings.TrimSpace(metadataString(sess.Metadata["docker_agent_id"])) != ""
}

func (s *Server) unifiedAgentDefinitionForSession(sess *session.Session) (*agentdef.Definition, error) {
	if sess == nil || sess.Metadata == nil {
		return nil, fmt.Errorf("session has no direct agent target")
	}
	agentID := strings.TrimSpace(metadataString(sess.Metadata["unified_agent_id"]))
	if agentID == "" {
		return nil, fmt.Errorf("session has no unified agent target")
	}
	currentProjectID := ""
	if sess.ProjectID != nil {
		currentProjectID = strings.TrimSpace(*sess.ProjectID)
	}
	def, discoveredProjectID, err := s.definitionForUnifiedAgent(agentID, currentProjectID)
	if err != nil {
		return nil, fmt.Errorf("unified agent %q not found", agentID)
	}
	if !agentVisibleInProjectSession(discoveredProjectID, currentProjectID) {
		return nil, fmt.Errorf("unified agent %q belongs to another project", agentID)
	}
	return def, nil
}

func (s *Server) resolveDirectAgentContainer(ctx context.Context, sess *session.Session) (*LocalDockerAgent, *dockerWorkspaceBinding, error) {
	if sess == nil || sess.Metadata == nil {
		return nil, nil, fmt.Errorf("session has no direct agent target")
	}
	currentProjectID := ""
	if sess.ProjectID != nil {
		currentProjectID = strings.TrimSpace(*sess.ProjectID)
	}
	if agentID := strings.TrimSpace(metadataString(sess.Metadata["unified_agent_id"])); agentID != "" {
		def, err := s.unifiedAgentDefinitionForSession(sess)
		if err != nil {
			return nil, nil, err
		}
		workspace, err := s.resolveDockerWorkspaceBinding(def, currentProjectID)
		if err != nil {
			return nil, nil, err
		}
		agent, err := s.dockerRuntime.ensureAgentContainerForWorkspace(ctx, def, workspace)
		if err != nil {
			return nil, nil, err
		}
		return agent, &workspace, nil
	}
	dockerID := strings.TrimSpace(metadataString(sess.Metadata["docker_agent_id"]))
	if dockerID == "" {
		return nil, nil, fmt.Errorf("session has no direct agent target")
	}
	agent, err := findLocalBruteContainer(ctx, dockerID)
	if err != nil {
		return nil, nil, err
	}
	return agent, nil, nil
}

func (s *Server) runDirectAgentSession(ctx context.Context, sess *session.Session, task string, onEvent func(ChatStreamEvent)) (ChatResponse, error) {
	agent, workspace, err := s.resolveDirectAgentContainer(ctx, sess)
	if err != nil {
		return ChatResponse{}, err
	}
	if !agent.Running {
		startCtx, cancel := context.WithTimeout(ctx, dockerDelegationStartTimeout)
		_, err := runCommand(startCtx, "docker", "start", agent.ID)
		cancel()
		if err != nil {
			return ChatResponse{}, fmt.Errorf("failed to start docker agent: %w", err)
		}
		agent, err = findLocalBruteContainer(ctx, agent.ID)
		if err != nil {
			return ChatResponse{}, err
		}
	}
	if agent.HostPort == 0 || strings.TrimSpace(agent.APIURL) == "" {
		return ChatResponse{}, fmt.Errorf("docker agent does not expose Brute API")
	}
	// A redirect must not forward the internal per-turn capability elsewhere.
	client := &nethttp.Client{CheckRedirect: func(*nethttp.Request, []*nethttp.Request) error { return nethttp.ErrUseLastResponse }}
	healthCtx, healthCancel := context.WithTimeout(ctx, dockerDelegationStartTimeout)
	err = waitForLocalDockerAgentHTTP(healthCtx, client, agent.APIURL)
	healthCancel()
	if err != nil {
		return ChatResponse{}, err
	}
	baseURL := strings.TrimRight(agent.APIURL, "/")
	childID := s.reusableDirectAgentChild(ctx, sess, agent)
	if childID == "" {
		createMetadata := map[string]interface{}{"source": "direct_agent_session", "parent_session_id": sess.ID}
		if workspace != nil {
			createMetadata["docker_workspace"] = dockerWorkspaceMetadata(*workspace)
		}
		var created CreateSessionResponse
		if err := postLocalDockerAgentJSON(ctx, client, baseURL+"/sessions", CreateSessionRequest{AgentID: "build", Metadata: createMetadata}, &created); err != nil {
			return ChatResponse{}, err
		}
		childID = created.ID
		// Record on the in-memory session: the caller saves it after the run and
		// would overwrite a mapping written only to a freshly loaded copy.
		addDockerChildSessionEntry(sess, agent, childID, workspace)
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Direct agent: failed to save child mapping for %s: %v", sess.ID, err)
		}
	}
	prompt := task
	if workspace != nil {
		prompt = s.rewriteDockerDelegationTask(task, *workspace)
	}
	headers, revoke, err := s.directAgentToolsHeaders(ctx, sess, agent.Runtime)
	if err != nil {
		return ChatResponse{}, err
	}
	defer revoke()
	return postLocalDockerAgentChatStreamWithHeaders(ctx, client, baseURL+"/sessions/"+childID+"/chat/stream", ChatRequest{Message: prompt}, headers, onEvent)
}

// directAgentReplyMetadata links the parent's copy of the agent reply to the
// child's runtime turn. WHY: the UI already rendered that turn live from the
// forwarded child events (keyed by turn_id); a persisted reply without the same
// runtime_turn_id looks like a different message, so the live bubble stayed as
// "in flight" next to it and the answer showed twice until reload.
func directAgentReplyMetadata(resp ChatResponse) map[string]interface{} {
	for i := len(resp.Messages) - 1; i >= 0; i-- {
		msg := resp.Messages[i]
		if msg.Role != "assistant" {
			continue
		}
		// Only the latest reply: older assistant messages belong to previous turns.
		turnID, ok := msg.Metadata["runtime_turn_id"].(string)
		if !ok || strings.TrimSpace(turnID) == "" {
			return nil
		}
		// WHY: runtime_tools/reasoning/cost/warnings describe the steps between the
		// user request and the answer; without them a reloaded parent session shows
		// only the final text. Copy every runtime_* key of the child reply.
		meta := map[string]interface{}{}
		for k, v := range msg.Metadata {
			if strings.HasPrefix(k, "runtime_") {
				meta[k] = v
			}
		}
		return meta
	}
	return nil
}

// reusableDirectAgentChild returns the agent session used by the previous turn.
// WHY: a fresh child per turn dropped the conversation context and re-ran the
// automatic router on every follow-up, so models switched without the user
// asking (e.g. a pasted password was routed to a local model). Reusing the child
// keeps its sticky route and history. A transition still awaiting confirmation is
// rejected: sending a new message instead of approving keeps the current provider.
func (s *Server) reusableDirectAgentChild(ctx context.Context, sess *session.Session, agent *LocalDockerAgent) string {
	entries := dockerChildSessionEntries(sess.Metadata["docker_child_sessions"])
	if len(entries) == 0 {
		return ""
	}
	last := entries[len(entries)-1]
	childID := strings.TrimSpace(metadataString(last["child_session_id"]))
	if childID == "" || metadataString(last["agent_name"]) != agent.Name {
		return ""
	}
	lookupCtx, cancel := context.WithTimeout(ctx, dockerSessionProxyLookupTimeout)
	defer cancel()
	client := &nethttp.Client{Timeout: dockerSessionProxyLookupTimeout}
	child, err := fetchDockerSession(lookupCtx, client, dockerSessionCandidate{APIURL: agent.APIURL}, childID, false)
	if err != nil || child.Status == string(session.StatusRunning) {
		logging.Warn("Direct agent: not reusing child %s of %s (status=%q): %v", childID, sess.ID, child.Status, err)
		return ""
	}
	if t := session.CurrentProviderTransition(&session.Session{Metadata: child.Metadata}); t != nil && t.Confirmation == "pending" {
		url := strings.TrimRight(agent.APIURL, "/") + "/sessions/" + childID + "/provider-transitions/" + t.ID + "/decision"
		if err := postLocalDockerAgentJSON(lookupCtx, client, url, map[string]string{"decision": "reject"}, nil); err != nil {
			logging.Warn("Direct agent: failed to reject pending transition in child %s: %v", childID, err)
			return ""
		}
	}
	return childID
}
