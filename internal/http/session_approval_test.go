package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/approval"
)

func TestHandleGetSessionApprovalMissingSession(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/sessions/missing/approval", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleGetSessionApprovalNoPending(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/sessions/"+sess.ID+"/approval", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Approval *NativeToolApprovalResponse `json:"approval"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Approval != nil {
		t.Fatalf("expected null approval, got %#v", body.Approval)
	}
}

func TestHandleGetSessionApprovalReturnsPendingDTO(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	input := json.RawMessage(`{"command":"ls -la"}`)
	go func() {
		_, _ = server.approvalBroker.Request(context.Background(), approval.RequestParams{
			SessionID: sess.ID,
			ToolUseID: "tu-1",
			ToolName:  "bash",
			Input:     input,
			Reason:    "list files",
			Timeout:   time.Minute,
		})
	}()
	waitForApprovalPending(t, server.approvalBroker, 1)

	req := httptest.NewRequest(http.MethodGet, "/sessions/"+sess.ID+"/approval", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Approval NativeToolApprovalResponse `json:"approval"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Approval.SessionID != sess.ID {
		t.Fatalf("session_id = %q", body.Approval.SessionID)
	}
	if body.Approval.ToolName != "bash" {
		t.Fatalf("tool_name = %q", body.Approval.ToolName)
	}
	if body.Approval.Kind != "tool" {
		t.Fatalf("kind = %q", body.Approval.Kind)
	}
	if body.Approval.RequestID == "" {
		t.Fatal("request_id is empty")
	}
	if got := body.Approval.Input["command"]; got != "ls -la" {
		t.Fatalf("input.command = %#v", got)
	}
}

func TestHandleSubmitSessionApprovalErrors(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	otherSess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create other session: %v", err)
	}

	startPendingApproval(t, server, sess.ID, "tu-1")
	requestID := server.approvalBroker.PendingForSession(sess.ID)[0].ID

	t.Run("unknown request", func(t *testing.T) {
		rec := postSessionApproval(t, server, sess.ID, "missing", `{"decision":"allow_once"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("session mismatch", func(t *testing.T) {
		rec := postSessionApproval(t, server, otherSess.ID, requestID, `{"decision":"allow_once"}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
}

func TestHandleSubmitSessionApprovalDuplicateResolve(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	startPendingApproval(t, server, sess.ID, "tu-dup")
	requestID := server.approvalBroker.PendingForSession(sess.ID)[0].ID

	rec := postSessionApproval(t, server, sess.ID, requestID, `{"decision":"allow_once"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("first resolve status = %d", rec.Code)
	}
	rec = postSessionApproval(t, server, sess.ID, requestID, `{"decision":"deny"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409", rec.Code)
	}
}

func TestHandleSubmitSessionApprovalAuditSanitizedUserResponse(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	startPendingApproval(t, server, sess.ID, "tu-q")
	requestID := server.approvalBroker.PendingForSession(sess.ID)[0].ID

	rec := postSessionApproval(t, server, sess.ID, requestID, `{"decision":"allow_once","answers":{"Color?":"Blue"},"message":"ignored"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	fresh, err := server.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	audit, err := decodeSessionApprovalAudit(fresh.Metadata[sessionApprovalAuditMetadataKey])
	if err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	foundResolved := false
	for _, entry := range audit {
		if entry.Kind == string(approval.AuditResolved) {
			foundResolved = true
			if !entry.HasUserResponse {
				t.Fatal("expected has_user_response=true")
			}
			if entry.AnswerCount != 1 {
				t.Fatalf("answer_count = %d, want 1", entry.AnswerCount)
			}
			raw := mustJSON(entry)
			if strings.Contains(raw, "Blue") || strings.Contains(raw, "ignored") {
				t.Fatalf("audit must not include answer text: %s", raw)
			}
			if strings.Contains(strings.ToLower(raw), "message") {
				t.Fatalf("audit must not include message field: %s", raw)
			}
		}
		if strings.Contains(mustJSON(entry), "ls -la") {
			t.Fatalf("audit leaked tool input: %#v", entry)
		}
	}
	if !foundResolved {
		t.Fatalf("missing resolved audit entry: %#v", audit)
	}
}

func TestHandleSubmitSessionApprovalResolvedDoesNotConsumePayload(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	startPendingApproval(t, server, sess.ID, "tu-peek")
	requestID := server.approvalBroker.PendingForSession(sess.ID)[0].ID

	rec := postSessionApproval(t, server, sess.ID, requestID, `{"decision":"allow_once","answers":{"Q?":"A"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	payload, ok := server.peekApprovalResolvePayload(requestID)
	if !ok {
		t.Fatal("expected payload to remain after resolved event audit peek")
	}
	if payload.Answers["Q?"] != "A" {
		t.Fatalf("payload answers = %#v", payload.Answers)
	}

	taken, ok := server.takeApprovalResolvePayload(requestID)
	if !ok {
		t.Fatal("expected transport take to succeed")
	}
	if taken.Answers["Q?"] != "A" {
		t.Fatalf("taken answers = %#v", taken.Answers)
	}
	if _, stillThere := server.peekApprovalResolvePayload(requestID); stillThere {
		t.Fatal("payload should be removed after take")
	}
}

func TestApprovalResolvePayloadCleanupOnTimeout(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := server.approvalBroker.Request(context.Background(), approval.RequestParams{
			SessionID: sess.ID,
			ToolUseID: "tu-timeout-payload",
			ToolName:  "bash",
			Timeout:   30 * time.Millisecond,
		})
		done <- err
	}()
	waitForApprovalPending(t, server.approvalBroker, 1)
	requestID := server.approvalBroker.PendingForSession(sess.ID)[0].ID
	server.setApprovalResolvePayload(requestID, approvalResolvePayload{
		Answers: map[string]string{"Q?": "late"},
	})

	if err := <-done; !errors.Is(err, approval.ErrTimedOut) {
		t.Fatalf("request err = %v", err)
	}
	if _, ok := server.peekApprovalResolvePayload(requestID); ok {
		t.Fatal("expected payload cleanup on timeout")
	}
}

func TestApprovalResolvePayloadCleanupOnCancel(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := server.approvalBroker.Request(ctx, approval.RequestParams{
			SessionID: sess.ID,
			ToolUseID: "tu-cancel-payload",
			ToolName:  "bash",
			Timeout:   time.Minute,
		})
		done <- err
	}()
	waitForApprovalPending(t, server.approvalBroker, 1)
	requestID := server.approvalBroker.PendingForSession(sess.ID)[0].ID
	server.setApprovalResolvePayload(requestID, approvalResolvePayload{
		Answers: map[string]string{"Q?": "late"},
	})
	cancel()

	if err := <-done; !errors.Is(err, approval.ErrCancelled) {
		t.Fatalf("request err = %v", err)
	}
	if _, ok := server.peekApprovalResolvePayload(requestID); ok {
		t.Fatal("expected payload cleanup on cancel")
	}
}

func TestApprovalAuditNoAnswerTextInMetadata(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	startPendingApproval(t, server, sess.ID, "tu-sensitive")
	requestID := server.approvalBroker.PendingForSession(sess.ID)[0].ID
	rec := postSessionApproval(t, server, sess.ID, requestID, `{"decision":"allow_once","answers":{"Secret?":"top-secret-value"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	fresh, err := server.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	rawMeta, err := json.Marshal(fresh.Metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if strings.Contains(string(rawMeta), "top-secret-value") {
		t.Fatalf("session metadata leaked answer text: %s", string(rawMeta))
	}
}

func TestNativeToolApprovalQuestionsMultiQuestionExact(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"questions":[{"question":"Color?","header":"Palette","multiSelect":true,"options":[{"label":"Red","description":"warm"},{"label":"Blue","description":"cool"}]},{"question":"Size?","header":"Fit","options":[{"label":"Large","description":"roomy"}]}]}`)
	req := approval.Request{
		ID:        "req-multi",
		SessionID: "sess-1",
		ToolName:  "AskUserQuestion",
		Input:     input,
		AskUser:   &approval.AskUserPayload{Question: "ignored", Suggestions: []string{"fallback"}},
	}

	questions := nativeToolApprovalQuestions(req)
	if len(questions) != 2 {
		t.Fatalf("questions len = %d, want 2", len(questions))
	}
	if questions[0].Question != "Color?" || questions[0].Header != "Palette" {
		t.Fatalf("first question = %#v", questions[0])
	}
	if !questions[0].Multiple || !questions[0].Custom {
		t.Fatalf("first question flags = multiple:%v custom:%v", questions[0].Multiple, questions[0].Custom)
	}
	if len(questions[0].Options) != 2 || questions[0].Options[0].Description != "warm" {
		t.Fatalf("first options = %#v", questions[0].Options)
	}
	if questions[1].Question != "Size?" || questions[1].Header != "Fit" {
		t.Fatalf("second question = %#v", questions[1])
	}
	if questions[1].Multiple {
		t.Fatalf("second question multiple = true, want false")
	}
	if !questions[1].Custom || questions[1].Options[0].Description != "roomy" {
		t.Fatalf("second question = %#v", questions[1])
	}
}

func TestNativeToolApprovalQuestionsAskUserFallback(t *testing.T) {
	t.Parallel()

	req := approval.Request{
		ToolName: "custom_ask",
		Reason:   "Need input",
		AskUser:  &approval.AskUserPayload{Question: "Proceed?", Suggestions: []string{"yes", "no"}},
	}
	questions := nativeToolApprovalQuestions(req)
	if len(questions) != 1 {
		t.Fatalf("questions len = %d, want 1", len(questions))
	}
	if questions[0].Question != "Proceed?" || questions[0].Header != "Need input" {
		t.Fatalf("question = %#v", questions[0])
	}
	if !questions[0].Custom || questions[0].Multiple {
		t.Fatalf("flags = multiple:%v custom:%v", questions[0].Multiple, questions[0].Custom)
	}
	if len(questions[0].Options) != 2 || questions[0].Options[0].Label != "yes" {
		t.Fatalf("options = %#v", questions[0].Options)
	}
}

func TestHandleGetSessionApprovalReturnsMultiQuestionDTO(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	input := json.RawMessage(`{"questions":[{"question":"Color?","header":"Palette","multiSelect":true,"options":[{"label":"Red","description":"warm"}]},{"question":"Size?","header":"Fit","options":[{"label":"Large","description":"roomy"}]}]}`)
	go func() {
		_, _ = server.approvalBroker.Request(context.Background(), approval.RequestParams{
			SessionID: sess.ID,
			ToolUseID: "tu-multi-dto",
			ToolName:  "AskUserQuestion",
			Input:     input,
			Timeout:   time.Minute,
		})
	}()
	waitForApprovalPending(t, server.approvalBroker, 1)

	req := httptest.NewRequest(http.MethodGet, "/sessions/"+sess.ID+"/approval", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Approval NativeToolApprovalResponse `json:"approval"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Approval.Kind != "question" {
		t.Fatalf("kind = %q", body.Approval.Kind)
	}
	if len(body.Approval.Questions) != 2 {
		t.Fatalf("questions len = %d, want 2", len(body.Approval.Questions))
	}
	if body.Approval.Questions[0].Header != "Palette" || !body.Approval.Questions[0].Multiple {
		t.Fatalf("first question = %#v", body.Approval.Questions[0])
	}
	if body.Approval.Questions[0].Options[0].Description != "warm" {
		t.Fatalf("first option = %#v", body.Approval.Questions[0].Options[0])
	}
}

func TestHandleSubmitSessionApprovalRejectsAllowSessionForQuestion(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	input := json.RawMessage(`{"questions":[{"question":"Pick?","options":[{"label":"A"}]}]}`)
	go func() {
		_, _ = server.approvalBroker.Request(context.Background(), approval.RequestParams{
			SessionID: sess.ID,
			ToolUseID: "tu-ask-session",
			ToolName:  "AskUserQuestion",
			Input:     input,
			AskUser:   &approval.AskUserPayload{Question: "Pick?", Suggestions: []string{"A"}},
			Timeout:   time.Minute,
		})
	}()
	waitForApprovalPending(t, server.approvalBroker, 1)
	requestID := server.approvalBroker.PendingForSession(sess.ID)[0].ID

	rec := postSessionApproval(t, server, sess.ID, requestID, `{"decision":"allow_session"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if server.approvalBroker.SessionAllowed(sess.ID, "AskUserQuestion") {
		t.Fatal("allow_session on question must not cache")
	}
	if len(server.approvalBroker.PendingForSession(sess.ID)) != 1 {
		t.Fatalf("pending = %d, want 1", len(server.approvalBroker.PendingForSession(sess.ID)))
	}
}

func TestHandleSubmitSessionApprovalAnswersReachTransport(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	input := json.RawMessage(`{"questions":[{"question":"Color?","options":[{"label":"Red"}]},{"question":"Size?","options":[{"label":"Large"}]}]}`)
	go func() {
		_, _ = server.approvalBroker.Request(context.Background(), approval.RequestParams{
			SessionID: sess.ID,
			ToolUseID: "tu-multi",
			ToolName:  "AskUserQuestion",
			Input:     input,
			Timeout:   time.Minute,
		})
	}()
	waitForApprovalPending(t, server.approvalBroker, 1)
	requestID := server.approvalBroker.PendingForSession(sess.ID)[0].ID

	want := map[string]string{"Color?": "Red", "Size?": "Large"}
	rec := postSessionApproval(t, server, sess.ID, requestID, `{"decision":"allow_once","answers":{"Color?":"Red","Size?":"Large"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	payload, ok := server.TakeApprovalResponse(requestID)
	if !ok {
		t.Fatal("expected transport to take HTTP answers by broker request id")
	}
	for key, wantVal := range want {
		if payload.Answers[key] != wantVal {
			t.Fatalf("answers[%q] = %q, want %q", key, payload.Answers[key], wantVal)
		}
	}
}

func TestApprovalBrokerStreamEvents(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	events, unsubscribe := server.SubscribeSessionEvents(sess.ID)
	defer unsubscribe()

	startPendingApproval(t, server, sess.ID, "tu-stream")
	requestID := server.approvalBroker.PendingForSession(sess.ID)[0].ID

	required := waitForStreamApprovalEvent(t, events, "permission_required")
	if required.Approval == nil || required.Approval.RequestID != requestID {
		t.Fatalf("permission_required = %#v", required)
	}

	rec := postSessionApproval(t, server, sess.ID, requestID, `{"decision":"deny"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve status = %d", rec.Code)
	}

	resolved := waitForStreamApprovalEvent(t, events, "permission_resolved")
	if resolved.Approval == nil || resolved.Approval.Status != "resolved" {
		t.Fatalf("permission_resolved = %#v", resolved)
	}
}

func TestApprovalBrokerTimeoutEmitsResolvedEvent(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	events, unsubscribe := server.SubscribeSessionEvents(sess.ID)
	defer unsubscribe()

	done := make(chan error, 1)
	go func() {
		_, err := server.approvalBroker.Request(context.Background(), approval.RequestParams{
			SessionID: sess.ID,
			ToolUseID: "tu-timeout",
			ToolName:  "bash",
			Timeout:   30 * time.Millisecond,
		})
		done <- err
	}()

	required := waitForStreamApprovalEvent(t, events, "permission_required")
	if required.Approval == nil {
		t.Fatal("expected permission_required")
	}

	resolved := waitForStreamApprovalEvent(t, events, "permission_resolved")
	if resolved.Approval == nil || resolved.Approval.Status != "timed_out" {
		t.Fatalf("permission_resolved = %#v", resolved)
	}
	if err := <-done; !errors.Is(err, approval.ErrTimedOut) {
		t.Fatalf("request err = %v", err)
	}
}

func TestApprovalBrokerCancelEmitsResolvedEvent(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	events, unsubscribe := server.SubscribeSessionEvents(sess.ID)
	defer unsubscribe()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := server.approvalBroker.Request(ctx, approval.RequestParams{
			SessionID: sess.ID,
			ToolUseID: "tu-cancel",
			ToolName:  "bash",
			Timeout:   time.Minute,
		})
		done <- err
	}()

	waitForStreamApprovalEvent(t, events, "permission_required")
	cancel()

	resolved := waitForStreamApprovalEvent(t, events, "permission_resolved")
	if resolved.Approval == nil || resolved.Approval.Status != "cancelled" {
		t.Fatalf("permission_resolved = %#v", resolved)
	}
	if err := <-done; !errors.Is(err, approval.ErrCancelled) {
		t.Fatalf("request err = %v", err)
	}
}

func TestApprovalBrokerForSessionRequiresExistingSession(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	if _, err := server.ApprovalBrokerForSession("missing"); err == nil {
		t.Fatal("expected error for missing session")
	}

	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	broker, err := server.ApprovalBrokerForSession(sess.ID)
	if err != nil {
		t.Fatalf("ApprovalBrokerForSession: %v", err)
	}
	if broker != server.approvalBroker {
		t.Fatal("expected shared server broker")
	}
}

func startPendingApproval(t *testing.T, server *Server, sessionID, toolUseID string) {
	t.Helper()
	go func() {
		_, _ = server.approvalBroker.Request(context.Background(), approval.RequestParams{
			SessionID: sessionID,
			ToolUseID: toolUseID,
			ToolName:  "bash",
			Input:     json.RawMessage(`{"command":"ls -la"}`),
			Timeout:   time.Minute,
		})
	}()
	waitForApprovalPending(t, server.approvalBroker, 1)
}

func postSessionApproval(t *testing.T, server *Server, sessionID, requestID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sessionID+"/approval/"+requestID, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	return rec
}

func waitForApprovalPending(t *testing.T, broker *approval.Broker, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(broker.Pending()) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pending = %d, want %d", len(broker.Pending()), want)
}

func waitForStreamApprovalEvent(t *testing.T, events <-chan ChatStreamEvent, wantType string) ChatStreamEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Type == wantType {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s event", wantType)
		}
	}
}

func mustJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func TestAppendSessionApprovalAuditConcurrent(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			server.appendSessionApprovalAudit(sess.ID, sessionApprovalAuditEntry{
				Kind:      "requested",
				RequestID: fmt.Sprintf("req-%d", i),
				ToolName:  "bash",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})
		}()
	}
	wg.Wait()

	fresh, err := server.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	audit, err := decodeSessionApprovalAudit(fresh.Metadata[sessionApprovalAuditMetadataKey])
	if err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit) != n {
		t.Fatalf("audit count = %d, want %d", len(audit), n)
	}
}

func TestApprovalAuditRequestedWithoutToolInput(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		startPendingApproval(t, server, sess.ID, "tu-audit")
	}()
	waitForStreamApprovalEvent(t, mustSubscribe(t, server, sess.ID), "permission_required")

	fresh, err := server.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	audit, err := decodeSessionApprovalAudit(fresh.Metadata[sessionApprovalAuditMetadataKey])
	if err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	if len(audit) == 0 {
		t.Fatal("expected audit entries")
	}
	for _, entry := range audit {
		if strings.Contains(mustJSON(entry), "ls -la") {
			t.Fatalf("audit leaked tool input: %#v", entry)
		}
	}
	wg.Wait()
}

func mustSubscribe(t *testing.T, server *Server, sessionID string) <-chan ChatStreamEvent {
	t.Helper()
	events, _ := server.SubscribeSessionEvents(sessionID)
	return events
}
