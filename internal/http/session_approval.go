package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/approval"
	"github.com/A2gent/brute/internal/llm/claudecli"
	"github.com/go-chi/chi/v5"
)

const sessionApprovalAuditMetadataKey = "approval_audit"

type sessionApprovalAuditEntry struct {
	Kind            string `json:"kind"`
	RequestID       string `json:"request_id"`
	ToolName        string `json:"tool_name"`
	ToolUseID       string `json:"tool_use_id,omitempty"`
	Decision        string `json:"decision,omitempty"`
	HasUserResponse bool   `json:"has_user_response,omitempty"`
	AnswerCount     int    `json:"answer_count,omitempty"`
	Timestamp       string `json:"timestamp"`
}

type approvalResolvePayload struct {
	Answers map[string]string
	Message string
}

func (s *Server) initApprovalBroker() {
	s.approvalBroker = approval.New(approval.DefaultLimits())
	s.approvalBroker.Subscribe(s.handleApprovalBrokerEvent)
}

// ApprovalBrokerForSession returns the server-owned approval broker after verifying the session exists.
func (s *Server) ApprovalBrokerForSession(sessionID string) (*approval.Broker, error) {
	if s == nil || s.approvalBroker == nil {
		return nil, errors.New("approval broker unavailable")
	}
	if _, err := s.sessionManager.Get(sessionID); err != nil {
		return nil, err
	}
	return s.approvalBroker, nil
}

func (s *Server) handleGetSessionApproval(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if _, err := s.sessionManager.Get(sessionID); err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}

	pending := s.approvalBroker.PendingForSession(sessionID)
	var dto *NativeToolApprovalResponse
	if len(pending) > 0 {
		approvalDTO := nativeToolApprovalFromRequest(pending[0])
		dto = &approvalDTO
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"approval": dto})
}

func (s *Server) handleSubmitSessionApproval(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	requestID := chi.URLParam(r, "requestID")

	if _, err := s.sessionManager.Get(sessionID); err != nil {
		s.errorResponse(w, http.StatusNotFound, "Session not found: "+err.Error())
		return
	}

	var req struct {
		Decision string            `json:"decision"`
		Message  string            `json:"message,omitempty"`
		Answers  map[string]string `json:"answers,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	decision := approval.Decision(strings.TrimSpace(req.Decision))
	if !validApprovalDecision(decision) {
		s.errorResponse(w, http.StatusBadRequest, "invalid decision")
		return
	}

	payload := approvalResolvePayload{
		Answers: req.Answers,
		Message: strings.TrimSpace(req.Message),
	}
	if len(payload.Answers) > 0 || payload.Message != "" {
		s.setApprovalResolvePayload(requestID, payload)
	}

	err := s.approvalBroker.Resolve(requestID, sessionID, decision)
	if err != nil {
		s.clearApprovalResolvePayload(requestID)
		switch {
		case errors.Is(err, approval.ErrRequestNotFound):
			s.errorResponse(w, http.StatusNotFound, "approval request not found")
		case errors.Is(err, approval.ErrSessionMismatch):
			s.errorResponse(w, http.StatusForbidden, "approval request does not belong to session")
		case errors.Is(err, approval.ErrRequestAlreadyResolved):
			s.errorResponse(w, http.StatusConflict, "approval request already resolved")
		case errors.Is(err, approval.ErrInvalidDecision):
			s.errorResponse(w, http.StatusBadRequest, "invalid decision")
		default:
			s.errorResponse(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	if decision == approval.DecisionDeny {
		s.clearApprovalResolvePayload(requestID)
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleApprovalBrokerEvent(ev approval.Event) {
	if s == nil {
		return
	}
	sessionID := strings.TrimSpace(ev.Request.SessionID)
	if sessionID == "" {
		return
	}

	dto := nativeToolApprovalFromRequest(ev.Request)
	var auditKind string
	var streamType string
	var hasUserResponse bool
	var answerCount int

	switch ev.Kind {
	case approval.EventRequested:
		auditKind = string(approval.AuditRequested)
		streamType = "permission_required"
		dto.Status = "pending"
	case approval.EventResolved:
		auditKind = string(approval.AuditResolved)
		streamType = "permission_resolved"
		dto.Status = "resolved"
		if payload, ok := s.peekApprovalResolvePayload(ev.Request.ID); ok {
			answerCount = len(payload.Answers)
			if answerCount == 0 && strings.TrimSpace(payload.Message) != "" {
				answerCount = 1
			}
			hasUserResponse = answerCount > 0
		}
	case approval.EventTimedOut:
		auditKind = string(approval.AuditTimedOut)
		streamType = "permission_resolved"
		dto.Status = "timed_out"
		s.clearApprovalResolvePayload(ev.Request.ID)
	case approval.EventCancelled:
		auditKind = string(approval.AuditCancelled)
		streamType = "permission_resolved"
		dto.Status = "cancelled"
		s.clearApprovalResolvePayload(ev.Request.ID)
	default:
		return
	}

	s.appendSessionApprovalAudit(sessionID, sessionApprovalAuditEntry{
		Kind:            auditKind,
		RequestID:       ev.Request.ID,
		ToolName:        ev.Request.ToolName,
		ToolUseID:       ev.Request.ToolUseID,
		Decision:        string(ev.Decision),
		HasUserResponse: hasUserResponse,
		AnswerCount:     answerCount,
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
	})
	s.publishSessionEvent(sessionID, ChatStreamEvent{
		Type:     streamType,
		Approval: &dto,
	})
}

func (s *Server) appendSessionApprovalAudit(sessionID string, entry sessionApprovalAuditEntry) {
	s.approvalAuditMu.Lock()
	defer s.approvalAuditMu.Unlock()

	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}

	var audit []sessionApprovalAuditEntry
	if raw, ok := sess.Metadata[sessionApprovalAuditMetadataKey]; ok {
		if decoded, err := decodeSessionApprovalAudit(raw); err == nil {
			audit = decoded
		}
	}
	audit = append(audit, entry)
	sess.Metadata[sessionApprovalAuditMetadataKey] = audit
	_ = s.sessionManager.Save(sess)
}

func decodeSessionApprovalAudit(raw interface{}) ([]sessionApprovalAuditEntry, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var audit []sessionApprovalAuditEntry
	if err := json.Unmarshal(data, &audit); err != nil {
		return nil, err
	}
	return audit, nil
}

func nativeToolApprovalFromRequest(req approval.Request) NativeToolApprovalResponse {
	kind := "tool"
	var questions []NativeToolApprovalQuestion
	if req.AskUser != nil || strings.EqualFold(req.ToolName, "AskUserQuestion") {
		kind = "question"
		questions = nativeToolApprovalQuestions(req)
	}

	input := map[string]interface{}{}
	if len(req.Input) > 0 {
		_ = json.Unmarshal(req.Input, &input)
	}
	if input == nil {
		input = map[string]interface{}{}
	}

	return NativeToolApprovalResponse{
		RequestID: req.ID,
		SessionID: req.SessionID,
		ToolUseID: req.ToolUseID,
		ToolName:  req.ToolName,
		Input:     input,
		Reason:    req.Reason,
		Kind:      kind,
		Questions: questions,
		CreatedAt: req.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func nativeToolApprovalQuestions(req approval.Request) []NativeToolApprovalQuestion {
	if questions := nativeToolApprovalQuestionsFromInput(req.Input); len(questions) > 0 {
		return questions
	}
	if question, ok := nativeToolApprovalQuestionFromBridgeInput(req.Input); ok {
		return []NativeToolApprovalQuestion{question}
	}
	if req.AskUser == nil {
		return nil
	}
	header := strings.TrimSpace(req.Reason)
	if header == "" {
		header = "Question"
	}
	options := make([]NativeToolApprovalQuestionOption, 0, len(req.AskUser.Suggestions))
	for _, suggestion := range req.AskUser.Suggestions {
		label := strings.TrimSpace(suggestion)
		if label == "" {
			continue
		}
		options = append(options, NativeToolApprovalQuestionOption{
			Label:       label,
			Description: "",
		})
	}
	return []NativeToolApprovalQuestion{{
		Question: req.AskUser.Question,
		Header:   header,
		Options:  options,
		Multiple: false,
		Custom:   true,
	}}
}

func nativeToolApprovalQuestionsFromInput(raw json.RawMessage) []NativeToolApprovalQuestion {
	if len(raw) == 0 {
		return nil
	}
	var payload struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
				ImageURL    string `json:"image_url"`
				AudioURL    string `json:"audio_url"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload.Questions) == 0 {
		return nil
	}
	out := make([]NativeToolApprovalQuestion, 0, len(payload.Questions))
	for _, q := range payload.Questions {
		header := strings.TrimSpace(q.Header)
		if header == "" {
			header = "Question"
		}
		options := make([]NativeToolApprovalQuestionOption, 0, len(q.Options))
		for _, opt := range q.Options {
			label := strings.TrimSpace(opt.Label)
			if label == "" {
				continue
			}
			options = append(options, NativeToolApprovalQuestionOption{
				Label:       label,
				Description: strings.TrimSpace(opt.Description),
				ImageURL:    strings.TrimSpace(opt.ImageURL),
				AudioURL:    strings.TrimSpace(opt.AudioURL),
			})
		}
		out = append(out, NativeToolApprovalQuestion{
			Question: q.Question,
			Header:   header,
			Options:  options,
			Multiple: q.MultiSelect,
			Custom:   true,
		})
	}
	return out
}

// nativeToolApprovalQuestionFromBridgeInput parses the A2gent question tool
// argument shape ({question, header, options, multiple, custom}) used by the
// MCP bridge, so Caesar renders the same panel as for the Claude Agent SDK.
func nativeToolApprovalQuestionFromBridgeInput(raw json.RawMessage) (NativeToolApprovalQuestion, bool) {
	if len(raw) == 0 {
		return NativeToolApprovalQuestion{}, false
	}
	var payload struct {
		Question string `json:"question"`
		Header   string `json:"header"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
			ImageURL    string `json:"image_url"`
			AudioURL    string `json:"audio_url"`
		} `json:"options"`
		Multiple bool  `json:"multiple"`
		Custom   *bool `json:"custom"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return NativeToolApprovalQuestion{}, false
	}
	if strings.TrimSpace(payload.Question) == "" && len(payload.Options) == 0 {
		return NativeToolApprovalQuestion{}, false
	}
	header := strings.TrimSpace(payload.Header)
	if header == "" {
		header = "Question"
	}
	custom := true
	if payload.Custom != nil {
		custom = *payload.Custom
	}
	options := make([]NativeToolApprovalQuestionOption, 0, len(payload.Options))
	for _, opt := range payload.Options {
		label := strings.TrimSpace(opt.Label)
		if label == "" {
			continue
		}
		options = append(options, NativeToolApprovalQuestionOption{
			Label:       label,
			Description: strings.TrimSpace(opt.Description),
			ImageURL:    strings.TrimSpace(opt.ImageURL),
			AudioURL:    strings.TrimSpace(opt.AudioURL),
		})
	}
	return NativeToolApprovalQuestion{
		Question: payload.Question,
		Header:   header,
		Options:  options,
		Multiple: payload.Multiple,
		Custom:   custom,
	}, true
}

func validApprovalDecision(decision approval.Decision) bool {
	switch decision {
	case approval.DecisionAllowOnce, approval.DecisionAllowSession, approval.DecisionDeny:
		return true
	default:
		return false
	}
}

func (s *Server) setApprovalResolvePayload(requestID string, payload approvalResolvePayload) {
	s.approvalResolvePayloadMu.Lock()
	defer s.approvalResolvePayloadMu.Unlock()
	if s.approvalResolvePayload == nil {
		s.approvalResolvePayload = make(map[string]approvalResolvePayload)
	}
	cp := approvalResolvePayload{Message: payload.Message}
	if len(payload.Answers) > 0 {
		cp.Answers = make(map[string]string, len(payload.Answers))
		for k, v := range payload.Answers {
			cp.Answers[k] = v
		}
	}
	s.approvalResolvePayload[requestID] = cp
}

func (s *Server) peekApprovalResolvePayload(requestID string) (approvalResolvePayload, bool) {
	s.approvalResolvePayloadMu.Lock()
	defer s.approvalResolvePayloadMu.Unlock()
	payload, ok := s.approvalResolvePayload[requestID]
	return payload, ok
}

func (s *Server) takeApprovalResolvePayload(requestID string) (approvalResolvePayload, bool) {
	s.approvalResolvePayloadMu.Lock()
	defer s.approvalResolvePayloadMu.Unlock()
	payload, ok := s.approvalResolvePayload[requestID]
	if ok {
		delete(s.approvalResolvePayload, requestID)
	}
	return payload, ok
}

func (s *Server) clearApprovalResolvePayload(requestID string) {
	s.approvalResolvePayloadMu.Lock()
	defer s.approvalResolvePayloadMu.Unlock()
	delete(s.approvalResolvePayload, requestID)
}

// TakeApprovalResponse implements the claudecli approval response callback.
func (s *Server) TakeApprovalResponse(requestID string) (claudecli.ApprovalResolvePayload, bool) {
	payload, ok := s.takeApprovalResolvePayload(requestID)
	if !ok {
		return claudecli.ApprovalResolvePayload{}, false
	}
	return claudecli.ApprovalResolvePayload{
		Answers: payload.Answers,
		Message: payload.Message,
	}, true
}
