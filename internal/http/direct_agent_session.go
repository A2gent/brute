package http

import (
	"context"
	"fmt"
	nethttp "net/http"
	neturl "net/url"
	"strings"
	"time"

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

// directAgentActivityMarker flags a parent reply whose child-turn activity was
// persisted with it, so GET hydration does not fetch the child for it again.
const directAgentActivityMarker = "direct_agent_activity"

// directAgentReplyMetadata links the parent's copy of the agent reply to the
// child's runtime turn. WHY: the UI already rendered that turn live from the
// forwarded child events (keyed by turn_id); a persisted reply without the same
// runtime_turn_id looks like a different message, so the live bubble stayed as
// "in flight" next to it and the answer showed twice until reload.
func directAgentReplyMetadata(resp ChatResponse) map[string]interface{} {
	final := directAgentFinalReplyIndex(resp.Messages)
	if final < 0 {
		return nil
	}
	// Only the latest reply: older assistant messages belong to previous turns.
	turnID, ok := resp.Messages[final].Metadata["runtime_turn_id"].(string)
	if !ok || strings.TrimSpace(turnID) == "" {
		return nil
	}
	// WHY: runtime_tools/reasoning/cost/warnings describe the steps between the
	// user request and the answer; without them a reloaded parent session shows
	// only the final text. Copy every runtime_* key of the child reply.
	return runtimeMetadataOnly(resp.Messages[final].Metadata)
}

func directAgentFinalReplyIndex(messages []MessageResponse) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			return i
		}
		if messages[i].Role == "user" && !injectedDuringRun(messages[i].Metadata) {
			break
		}
	}
	return -1
}

func runtimeMetadataOnly(md map[string]interface{}) map[string]interface{} {
	meta := map[string]interface{}{}
	for k, v := range md {
		if strings.HasPrefix(k, "runtime_") {
			meta[k] = v
		}
	}
	return meta
}

// directAgentTurnActivity returns the child messages of the turn that ended at
// messages[final], excluding that final reply (the parent stores it with the
// child's content separately). WHY: ordinary tool calls and results live in
// these intermediate messages, not in the final reply's runtime_* metadata, so
// copying only the reply lost them. The turn starts after the last user message
// that is not a note injected mid-run; without such a boundary nothing is
// returned rather than replaying earlier turns. User messages are skipped: the
// parent already holds the request. Tool results keep their metadata
// (child_session_id of nested delegations) and parallel batches stay grouped.
func directAgentTurnActivity(messages []MessageResponse, final int) []MessageResponse {
	if final < 0 || final >= len(messages) {
		return nil
	}
	start := -1
	for i := final - 1; i >= 0; i-- {
		if messages[i].Role == "user" && !injectedDuringRun(messages[i].Metadata) {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	end := final
	// A parked turn has no final answer: its last assistant still owns tool
	// calls, and the trailing results must survive alongside that message.
	if len(messages[final].ToolCalls) > 0 {
		end = final + 1
		for end < len(messages) && (messages[end].Role != "user" || injectedDuringRun(messages[end].Metadata)) {
			end++
		}
	}
	var out []MessageResponse
	for _, m := range messages[start+1 : end] {
		if m.Role != "assistant" && m.Role != "tool" {
			continue
		}
		// Only runtime_* keys: provider cursors/response ids of the child must
		// not look like the parent's own provider state.
		m.Metadata = runtimeMetadataOnly(m.Metadata)
		out = append(out, m)
	}
	return out
}

// injectedDuringRun mirrors session live-state parsing (bool or "true").
func injectedDuringRun(md map[string]interface{}) bool {
	switch v := md["injected_during_run"].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return false
}

// appendDirectAgentReply persists the child's current turn (activity, then the
// reply) into the parent. Shared by the streaming and background run paths.
func appendDirectAgentReply(sess *session.Session, resp ChatResponse) {
	final := directAgentFinalReplyIndex(resp.Messages)
	for _, m := range directAgentTurnActivity(resp.Messages, final) {
		m.Metadata[directAgentActivityMarker] = true
		sess.AddMessage(sessionMessageFromResponse(m))
	}
	// EOF recovery can use the tool-calling assistant's narration as Content.
	// It is already in the copied activity, not a separate final answer.
	if final >= 0 && len(resp.Messages[final].ToolCalls) > 0 {
		return
	}
	meta := directAgentReplyMetadata(resp)
	if meta == nil {
		meta = map[string]interface{}{}
	}
	meta[directAgentActivityMarker] = true
	sess.AddAssistantMessageWithMetadata(resp.Content, nil, meta)
}

// sessionMessageFromResponse converts a child message for parent storage. The
// ID is dropped so the parent store assigns its own; images are dropped because
// their paths belong to the child's filesystem (the final reply drops them too).
func sessionMessageFromResponse(m MessageResponse) session.Message {
	msg := session.Message{Provenance: m.Provenance, Role: m.Role, Content: m.Content, Metadata: m.Metadata, Timestamp: m.Timestamp}
	for _, tc := range m.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, session.ToolCall{ID: tc.ID, Name: tc.Name, Input: tc.Input, ThoughtSignature: tc.ThoughtSignature})
	}
	for _, tr := range m.ToolResults {
		msg.ToolResults = append(msg.ToolResults, session.ToolResult{ToolCallID: tr.ToolCallID, Content: tr.Content, IsError: tr.IsError, Metadata: tr.Metadata, Name: tr.Name, DurationMs: tr.DurationMs})
	}
	return msg
}

// directAgentHydrationTimeout bounds the whole read-time child lookup so a
// stopped or hung agent cannot stall opening an old session.
const directAgentHydrationTimeout = 2 * time.Second

// hydrateDirectAgentActivity inserts, for display only, the child-turn activity
// of replies saved before appendDirectAgentReply existed. Nothing is written
// back. Only children this session created as direct-agent children are read
// (delegate_to_agent children share docker_child_sessions), and only over the
// loopback API URL recorded in the mapping.
func (s *Server) hydrateDirectAgentActivity(ctx context.Context, sess *session.Session, messages []MessageResponse) []MessageResponse {
	if !s.hasDirectAgentTarget(sess) {
		return messages
	}
	pending := map[string]int{}
	for i, m := range messages {
		turnID, _ := m.Metadata["runtime_turn_id"].(string)
		// Do not rely on adjacent user messages: a page may start at the reply.
		// Newly persisted activity and replies are explicitly marked instead.
		if m.Role == "assistant" && turnID != "" && m.Metadata[directAgentActivityMarker] == nil {
			pending[turnID] = i
		}
	}
	if len(pending) == 0 {
		return messages
	}
	ctx, cancel := context.WithTimeout(ctx, directAgentHydrationTimeout)
	defer cancel()
	client := &nethttp.Client{Timeout: directAgentHydrationTimeout, CheckRedirect: func(*nethttp.Request, []*nethttp.Request) error { return nethttp.ErrUseLastResponse }}
	activity := map[int][]MessageResponse{}
	parked := map[int]bool{}
	entries := dockerChildSessionEntries(sess.Metadata["docker_child_sessions"])
	for i := len(entries) - 1; i >= 0 && len(pending) > 0 && ctx.Err() == nil; i-- {
		entry := entries[i]
		childID := strings.TrimSpace(metadataString(entry["child_session_id"]))
		apiURL := strings.TrimSpace(metadataString(entry["agent_api_url"]))
		if parent := metadataString(entry["parent_session_id"]); childID == "" || (parent != "" && parent != sess.ID) || !isLoopbackAPIURL(apiURL) || s.isSelfAPIURL(apiURL) {
			continue
		}
		child, err := fetchDockerSession(ctx, client, dockerSessionCandidate{APIURL: apiURL}, childID, true)
		if err != nil {
			logging.Debug("Direct agent: activity hydration skipped child %s of %s: %v", childID, sess.ID, err)
			continue
		}
		if metadataString(child.Metadata["source"]) != "direct_agent_session" || metadataString(child.Metadata["parent_session_id"]) != sess.ID {
			continue
		}
		for j := len(child.Messages) - 1; j >= 0; j-- {
			m := child.Messages[j]
			turnID, _ := m.Metadata["runtime_turn_id"].(string)
			at, ok := pending[turnID]
			if !ok || m.Role != "assistant" || turnID == "" {
				continue
			}
			delete(pending, turnID)
			activity[at] = directAgentTurnActivity(child.Messages, j)
			parked[at] = len(m.ToolCalls) > 0 && len(activity[at]) > 0
		}
	}
	if len(activity) == 0 {
		return messages
	}
	out := make([]MessageResponse, 0, len(messages))
	for i, m := range messages {
		out = append(out, activity[i]...)
		if !parked[i] {
			out = append(out, m)
		}
	}
	return out
}

func isLoopbackAPIURL(raw string) bool {
	u, err := neturl.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && isLoopbackHostname(u.Hostname())
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
	// WHY: docker_child_sessions also lists children made by delegate_to_agent.
	// Those are flagged as delegated, so the child rejects parent agent tools
	// ("require a direct host session") on a follow-up. Start a fresh direct child.
	if src, _ := child.Metadata["source"].(string); src != "direct_agent_session" {
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
