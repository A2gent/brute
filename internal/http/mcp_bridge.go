// mcp_bridge.go implements the session-scoped MCP server that exposes A2gent
// tools (question, image generation, integrations) to the Claude Code CLI.
// The CLI runs as a subprocess of brute and calls back over loopback HTTP, so
// brute is both the parent (blocked on the subprocess) and the callee; the
// per-invocation bearer token is what binds MCP requests to the owning session.
package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/approval"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
	"github.com/go-chi/chi/v5"
)

const (
	mcpBridgeDefaultTimeout  = 5 * time.Minute
	mcpBridgeProtocolVersion = "2024-11-05"
	mcpBridgeMaxBodyBytes    = 4 << 20
)

// mcpBridgeExcludedTools keeps the CLI on its own native equivalents and
// blocks re-entrant categories such as delegation and MCP-through-MCP.
var mcpBridgeExcludedTools = map[string]struct{}{
	"bash": {}, "code_execution": {}, "read": {}, "write": {}, "edit": {},
	"replace_lines": {}, "insert_lines": {}, "file_search": {}, "content_search": {},
	"glob": {}, "find_files": {}, "grep": {}, "filter": {}, "man": {},
	"delegate_to_subagent": {}, "delegate_to_agent": {}, "delegate_to_external_agent": {},
	"parallel": {}, "pipeline": {},
	"mcp_call": {}, "mcp_list_tools": {}, "mcp_manage": {},
}

func mcpBridgeToolExposed(name string) bool {
	_, excluded := mcpBridgeExcludedTools[name]
	return !excluded
}

// mcpBridgeToken binds one CLI invocation to exactly one session. cancel is
// invoked on revoke so pending blocked calls (a held question) do not leak.
type mcpBridgeToken struct {
	sessionID string
	ctx       context.Context
	cancel    context.CancelFunc
}

type mcpBridgeState struct {
	mu              sync.Mutex
	tokens          map[string]*mcpBridgeToken
	questions       map[string]int
	questionTimeout time.Duration
}

func newMCPBridgeState() *mcpBridgeState {
	return &mcpBridgeState{
		tokens:          make(map[string]*mcpBridgeToken),
		questions:       make(map[string]int),
		questionTimeout: mcpBridgeDefaultTimeout,
	}
}

func (st *mcpBridgeState) mint(sessionID string) (string, func(), error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, err
	}
	token := hex.EncodeToString(buf)
	ctx, cancel := context.WithCancel(context.Background())

	st.mu.Lock()
	st.tokens[token] = &mcpBridgeToken{sessionID: sessionID, ctx: ctx, cancel: cancel}
	st.mu.Unlock()

	var once sync.Once
	revoke := func() {
		once.Do(func() {
			cancel()
			st.mu.Lock()
			delete(st.tokens, token)
			st.mu.Unlock()
		})
	}
	return token, revoke, nil
}

func (st *mcpBridgeState) resolve(token string) (string, context.Context, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	entry, ok := st.tokens[token]
	if !ok || entry == nil {
		return "", nil, false
	}
	return entry.sessionID, entry.ctx, true
}

func (st *mcpBridgeState) trackQuestionStart(sessionID string) {
	st.mu.Lock()
	st.questions[sessionID]++
	st.mu.Unlock()
}

func (st *mcpBridgeState) trackQuestionEnd(sessionID string) {
	st.mu.Lock()
	if st.questions[sessionID] <= 1 {
		delete(st.questions, sessionID)
	} else {
		st.questions[sessionID]--
	}
	st.mu.Unlock()
}

func (st *mcpBridgeState) hasPendingQuestion(sessionID string) bool {
	if st == nil {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.questions[sessionID] > 0
}

// claudecliMCPBridgeHook implements claudecli.MCPBridgeHook: it mints a
// per-invocation token and returns the inline --mcp-config JSON for the CLI.
func (s *Server) claudecliMCPBridgeHook(_ context.Context, sessionID string) (string, func(), error) {
	if s == nil || s.mcpBridge == nil {
		return "", nil, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", nil, nil
	}
	if _, err := s.sessionManager.Get(sessionID); err != nil {
		return "", nil, nil
	}
	token, revoke, err := s.mcpBridge.mint(sessionID)
	if err != nil {
		return "", nil, err
	}
	cfg := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"a2gent": map[string]interface{}{
				"type": "http",
				"url":  fmt.Sprintf("http://127.0.0.1:%d/mcp/sessions/%s", s.Port(), sessionID),
				"headers": map[string]string{
					"Authorization": "Bearer " + token,
				},
			},
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		revoke()
		return "", nil, err
	}
	return string(data), revoke, nil
}

type mcpBridgeRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpBridgeRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpBridgeRPCResponse struct {
	JSONRPC string             `json:"jsonrpc"`
	ID      json.RawMessage    `json:"id"`
	Result  interface{}        `json:"result,omitempty"`
	Error   *mcpBridgeRPCError `json:"error,omitempty"`
}

func (m *mcpBridgeRPCMessage) isNotification() bool {
	return len(m.ID) == 0 || string(m.ID) == "null"
}

func (s *Server) handleMCPBridge(w http.ResponseWriter, r *http.Request) {
	if s.mcpBridge == nil {
		s.errorResponse(w, http.StatusServiceUnavailable, "MCP bridge is unavailable")
		return
	}
	// WHY: brute binds 0.0.0.0 without authentication, and this endpoint can
	// execute the session's full tool surface; loopback-only keeps it local.
	if !isLoopbackRemoteAddr(r.RemoteAddr) {
		s.errorResponse(w, http.StatusForbidden, "MCP bridge is only reachable from loopback")
		return
	}
	sessionID := chi.URLParam(r, "sessionID")
	token := mcpBridgeBearerToken(r)
	boundSessionID, tokenCtx, ok := s.mcpBridge.resolve(token)
	if !ok {
		s.errorResponse(w, http.StatusUnauthorized, "invalid or revoked MCP bridge token")
		return
	}
	if boundSessionID != sessionID {
		s.errorResponse(w, http.StatusForbidden, "MCP bridge token does not match session")
		return
	}
	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcpBridgeMaxBodyBytes))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "failed to read request body: "+err.Error())
		return
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		s.errorResponse(w, http.StatusBadRequest, "empty request body")
		return
	}

	if strings.HasPrefix(trimmed, "[") {
		var batch []mcpBridgeRPCMessage
		if err := json.Unmarshal(body, &batch); err != nil {
			s.writeMCPBridgeRPCError(w, json.RawMessage("null"), -32700, "parse error: "+err.Error())
			return
		}
		responses := make([]mcpBridgeRPCResponse, 0, len(batch))
		for i := range batch {
			if resp := s.dispatchMCPBridgeMessage(tokenCtx, sess, &batch[i]); resp != nil {
				responses = append(responses, *resp)
			}
		}
		if len(responses) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		s.jsonResponse(w, http.StatusOK, responses)
		return
	}

	var msg mcpBridgeRPCMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		s.writeMCPBridgeRPCError(w, json.RawMessage("null"), -32700, "parse error: "+err.Error())
		return
	}
	resp := s.dispatchMCPBridgeMessage(tokenCtx, sess, &msg)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) dispatchMCPBridgeMessage(tokenCtx context.Context, sess *session.Session, msg *mcpBridgeRPCMessage) *mcpBridgeRPCResponse {
	if msg.isNotification() {
		return nil
	}
	resp := &mcpBridgeRPCResponse{JSONRPC: "2.0", ID: msg.ID}

	switch strings.TrimSpace(msg.Method) {
	case "initialize":
		protocolVersion := mcpBridgeProtocolVersion
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(msg.Params, &params); err == nil && strings.TrimSpace(params.ProtocolVersion) != "" {
			protocolVersion = strings.TrimSpace(params.ProtocolVersion)
		}
		resp.Result = map[string]interface{}{
			"protocolVersion": protocolVersion,
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{"listChanged": false},
			},
			"serverInfo": map[string]interface{}{
				"name":    "a2gent-brute",
				"version": "1.0.0",
			},
		}
	case "ping":
		resp.Result = map[string]interface{}{}
	case "tools/list":
		resp.Result = map[string]interface{}{"tools": s.mcpBridgeToolList(sess)}
	case "tools/call":
		text, isError, rpcErr := s.mcpBridgeCallTool(tokenCtx, sess, msg.Params)
		if rpcErr != nil {
			resp.Error = rpcErr
			break
		}
		resp.Result = map[string]interface{}{
			"content": []map[string]interface{}{{"type": "text", "text": text}},
			"isError": isError,
		}
	default:
		resp.Error = &mcpBridgeRPCError{Code: -32601, Message: "method not found: " + msg.Method}
	}
	return resp
}

func (s *Server) mcpBridgeToolList(sess *session.Session) []map[string]interface{} {
	manager := s.toolManagerForSession(sess)
	if manager == nil {
		return []map[string]interface{}{}
	}
	defs := manager.GetDefinitions()
	out := make([]map[string]interface{}, 0, len(defs))
	for _, def := range defs {
		name := strings.TrimSpace(def.Name)
		if name == "" || !mcpBridgeToolExposed(name) {
			continue
		}
		out = append(out, map[string]interface{}{
			"name":        name,
			"description": def.Description,
			"inputSchema": def.InputSchema,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["name"].(string) < out[j]["name"].(string)
	})
	return out
}

func (s *Server) mcpBridgeCallTool(tokenCtx context.Context, sess *session.Session, params json.RawMessage) (string, bool, *mcpBridgeRPCError) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return "", false, &mcpBridgeRPCError{Code: -32602, Message: "invalid tools/call params: " + err.Error()}
	}
	name := strings.TrimSpace(call.Name)
	if name == "" {
		return "", false, &mcpBridgeRPCError{Code: -32602, Message: "tool name is required"}
	}
	if !mcpBridgeToolExposed(name) {
		return fmt.Sprintf("tool %q is not exposed over the MCP bridge", name), true, nil
	}
	if len(call.Arguments) == 0 {
		call.Arguments = json.RawMessage("{}")
	}

	if name == "question" {
		text, isError := s.mcpBridgeAskQuestion(tokenCtx, sess.ID, call.Arguments)
		return text, isError, nil
	}

	manager := s.toolManagerForSession(sess)
	if manager == nil {
		return "tool manager unavailable", true, nil
	}
	if _, ok := manager.Get(name); !ok {
		return fmt.Sprintf("tool not found: %s", name), true, nil
	}

	ctx := context.WithValue(tokenCtx, "session_id", sess.ID)
	ctx = tools.WithProgressCallback(ctx, func(ev tools.ProgressEvent) {
		s.publishSessionEvent(sess.ID, ChatStreamEvent{
			Type: "tool_progress",
			ToolProgress: &StreamToolProgressEvent{
				ToolCallID: ev.ToolCallID,
				ToolName:   ev.ToolName,
				Status:     ev.Status,
				Content:    ev.Content,
				Metadata:   ev.Metadata,
			},
		})
	})

	result, err := manager.Execute(ctx, name, call.Arguments)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), true, nil
	}
	if result == nil {
		return "tool returned no result", true, nil
	}
	if !result.Success {
		message := strings.TrimSpace(result.Error)
		if message == "" {
			message = strings.TrimSpace(result.Output)
		}
		if message == "" {
			message = "tool returned unsuccessful result"
		}
		return "Error: " + message, true, nil
	}
	return result.Output, false, nil
}

// mcpBridgeAskQuestion intercepts the question tool before tools.Manager so the
// CLI run blocks on the approval broker instead of pausing the brute loop; the
// same CLI loop continues once Caesar resolves the approval.
func (s *Server) mcpBridgeAskQuestion(ctx context.Context, sessionID string, args json.RawMessage) (string, bool) {
	var parsed struct {
		Question string `json:"question"`
		Header   string `json:"header"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return "invalid question parameters: " + err.Error(), true
	}
	question := strings.TrimSpace(parsed.Question)
	if question == "" {
		return "question is required", true
	}
	reason := strings.TrimSpace(parsed.Header)
	if reason == "" {
		reason = question
	}

	s.mcpBridge.trackQuestionStart(sessionID)
	defer s.mcpBridge.trackQuestionEnd(sessionID)

	result, err := s.approvalBroker.Request(ctx, approval.RequestParams{
		SessionID: sessionID,
		ToolName:  "question",
		Input:     args,
		Reason:    reason,
		AskUser:   &approval.AskUserPayload{Question: question},
		Timeout:   s.mcpBridge.questionTimeout,
	})
	if err != nil {
		return fmt.Sprintf("question was not answered: %v", err), true
	}
	if result.Decision == approval.DecisionDeny {
		return "User declined to answer the question", true
	}
	payload, _ := s.takeApprovalResolvePayload(result.RequestID)
	answer := mcpBridgeFormatAnswer(payload)
	if answer == "" {
		answer = "User approved without providing a text answer"
	}
	return answer, false
}

func mcpBridgeFormatAnswer(payload approvalResolvePayload) string {
	parts := make([]string, 0, len(payload.Answers)+1)
	if len(payload.Answers) > 0 {
		keys := make([]string, 0, len(payload.Answers))
		for key := range payload.Answers {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if value := strings.TrimSpace(payload.Answers[key]); value != "" {
				parts = append(parts, value)
			}
		}
	}
	if message := strings.TrimSpace(payload.Message); message != "" {
		parts = append(parts, message)
	}
	return strings.Join(parts, "\n")
}

// resolveMCPBridgeQuestion unblocks a bridge-held question with an answer that
// arrived through POST /sessions/{id}/answer. It must not trigger a session
// resume; the owning CLI run is still alive and continues on its own.
func (s *Server) resolveMCPBridgeQuestion(sessionID, answer string) {
	if s.approvalBroker == nil {
		return
	}
	for _, req := range s.approvalBroker.PendingForSession(sessionID) {
		if req.ToolName != "question" {
			continue
		}
		s.setApprovalResolvePayload(req.ID, approvalResolvePayload{Message: strings.TrimSpace(answer)})
		if err := s.approvalBroker.Resolve(req.ID, sessionID, approval.DecisionAllowOnce); err != nil {
			s.clearApprovalResolvePayload(req.ID)
		}
		return
	}
}

func (s *Server) writeMCPBridgeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	s.jsonResponse(w, http.StatusOK, mcpBridgeRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &mcpBridgeRPCError{Code: code, Message: message},
	})
}

func mcpBridgeBearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) > len("Bearer ") && strings.EqualFold(header[:len("Bearer ")], "Bearer ") {
		return strings.TrimSpace(header[len("Bearer "):])
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

func isLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.TrimSpace(remoteAddr)
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
