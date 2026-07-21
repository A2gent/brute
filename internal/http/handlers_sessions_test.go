package http

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/session"
)

func TestHandleListSessionsFiltersProjectAndMetadataKeys(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)

	projectID := "project-a"
	otherProjectID := "project-b"
	keepSession, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create keep session: %v", err)
	}
	keepSession.ProjectID = &projectID
	keepSession.Metadata = map[string]interface{}{
		"keep": "yes",
		"drop": "no",
	}
	if err := server.sessionManager.Save(keepSession); err != nil {
		t.Fatalf("save keep session: %v", err)
	}

	otherSession, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create other session: %v", err)
	}
	otherSession.ProjectID = &otherProjectID
	otherSession.Metadata = map[string]interface{}{"keep": "other"}
	if err := server.sessionManager.Save(otherSession); err != nil {
		t.Fatalf("save other session: %v", err)
	}

	req := httptest.NewRequest(stdhttp.MethodGet, "/sessions/?include_metadata=true&project_id=project-a&metadata_keys=keep", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var items []SessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode sessions response: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one filtered session, got %d: %#v", len(items), items)
	}
	if items[0].ID != keepSession.ID {
		t.Fatalf("expected session %q, got %q", keepSession.ID, items[0].ID)
	}
	if items[0].ProjectID != projectID {
		t.Fatalf("expected project %q, got %q", projectID, items[0].ProjectID)
	}
	if got := items[0].Metadata["keep"]; got != "yes" {
		t.Fatalf("expected kept metadata, got %#v", items[0].Metadata)
	}
	if _, ok := items[0].Metadata["drop"]; ok {
		t.Fatalf("expected drop metadata to be omitted, got %#v", items[0].Metadata)
	}
}

func TestHandleListSessionsIncludesSummary(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess.AddUserMessage("Investigate session summaries. Keep labels concise.")
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatalf("save session: %v", err)
	}

	req := httptest.NewRequest(stdhttp.MethodGet, "/sessions/", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var items []SessionListItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode sessions response: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected at least one session")
	}
	if got, want := items[0].Summary, "Investigate session summaries."; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestHandleGetSessionFiltersMetadataKeys(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess.Metadata = map[string]interface{}{
		"keep": "yes",
		"drop": "no",
	}
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatalf("save session: %v", err)
	}

	req := httptest.NewRequest(stdhttp.MethodGet, "/sessions/"+sess.ID+"?include_messages=false&include_metadata=true&metadata_keys=keep", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw session response: %v", err)
	}
	if _, ok := raw["active_runs"]; !ok {
		t.Fatalf("expected active_runs to be present even when zero, got %s", rec.Body.String())
	}
	var item SessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if got := item.Metadata["keep"]; got != "yes" {
		t.Fatalf("expected kept metadata, got %#v", item.Metadata)
	}
	if _, ok := item.Metadata["drop"]; ok {
		t.Fatalf("expected drop metadata to be omitted, got %#v", item.Metadata)
	}
	if len(item.Messages) != 0 {
		t.Fatalf("expected messages to be omitted, got %#v", item.Messages)
	}
}

func TestHandleDownloadSessionLogServesJSONLAttachment(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	logDir := t.TempDir()
	server.sessionManager.SetJSONLFolder(logDir)

	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess.AddUserMessage("download me")
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatalf("save session: %v", err)
	}

	req := httptest.NewRequest(stdhttp.MethodGet, "/sessions/"+sess.ID+"/log", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/x-ndjson" {
		t.Fatalf("expected ndjson content type, got %q", got)
	}
	wantDisposition := `attachment; filename="session-` + sess.ID + `.jsonl"`
	if got := rec.Header().Get("Content-Disposition"); got != wantDisposition {
		t.Fatalf("expected disposition %q, got %q", wantDisposition, got)
	}
	if !strings.Contains(rec.Body.String(), `"event_type":"message"`) || !strings.Contains(rec.Body.String(), `"download me"`) {
		t.Fatalf("expected session log body, got %s", rec.Body.String())
	}
}

func TestHandleDownloadSessionLogRejectsMissingLog(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	req := httptest.NewRequest(stdhttp.MethodGet, "/sessions/missing/log", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != stdhttp.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleDownloadSessionLogRejectsTraversalSessionID(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	server.sessionManager.SetJSONLFolder(t.TempDir())

	req := httptest.NewRequest(stdhttp.MethodGet, "/sessions/..%2Fsettings/log", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != stdhttp.StatusNotFound {
		t.Fatalf("expected 404 for unsafe session log path, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUpdateQueuedSessionMessageUpdatesPausedInitialPrompt(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	sess, err := server.sessionManager.CreateQueued("build")
	if err != nil {
		t.Fatalf("create queued session: %v", err)
	}
	sess.AddUserMessage("Original queued prompt")
	setSessionQueuePaused(sess, true)
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatalf("save queued session: %v", err)
	}

	messageID := sess.Messages[0].ID
	req := httptest.NewRequest(stdhttp.MethodPut, "/sessions/"+sess.ID+"/messages/"+messageID, strings.NewReader(`{"content":"Updated queued prompt with more detail"}`))
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var item SessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if got := item.Messages[0].Content; got != "Updated queued prompt with more detail" {
		t.Fatalf("message content = %q", got)
	}
	if got := item.Title; got != "Updated queued prompt with more detail" {
		t.Fatalf("title = %q", got)
	}
	if got := item.Summary; got != "Updated queued prompt with more detail" {
		t.Fatalf("summary = %q", got)
	}
}

func TestHandleUpdateQueuedSessionMessageRejectsUnsafeEdits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		prepare    func(*session.Session)
		messageID  func(*session.Session) string
		body       string
		wantStatus int
	}{
		{
			name: "queue is not paused",
			prepare: func(sess *session.Session) {
				sess.AddUserMessage("Queued prompt")
			},
			messageID:  func(sess *session.Session) string { return sess.Messages[0].ID },
			body:       `{"content":"Changed"}`,
			wantStatus: stdhttp.StatusConflict,
		},
		{
			name: "session already has a response",
			prepare: func(sess *session.Session) {
				sess.AddUserMessage("Queued prompt")
				sess.AddAssistantMessage("Already processed", nil)
				setSessionQueuePaused(sess, true)
			},
			messageID:  func(sess *session.Session) string { return sess.Messages[0].ID },
			body:       `{"content":"Changed"}`,
			wantStatus: stdhttp.StatusConflict,
		},
		{
			name: "message id does not match initial prompt",
			prepare: func(sess *session.Session) {
				sess.AddUserMessage("Queued prompt")
				setSessionQueuePaused(sess, true)
			},
			messageID:  func(*session.Session) string { return "other-message" },
			body:       `{"content":"Changed"}`,
			wantStatus: stdhttp.StatusConflict,
		},
		{
			name: "content is empty",
			prepare: func(sess *session.Session) {
				sess.AddUserMessage("Queued prompt")
				setSessionQueuePaused(sess, true)
			},
			messageID:  func(sess *session.Session) string { return sess.Messages[0].ID },
			body:       `{"content":"  "}`,
			wantStatus: stdhttp.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := newBruteHTTPProxyTestServer(t)
			sess, err := server.sessionManager.CreateQueued("build")
			if err != nil {
				t.Fatalf("create queued session: %v", err)
			}
			tt.prepare(sess)
			if err := server.sessionManager.Save(sess); err != nil {
				t.Fatalf("save queued session: %v", err)
			}

			req := httptest.NewRequest(stdhttp.MethodPut, "/sessions/"+sess.ID+"/messages/"+tt.messageID(sess), strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			server.router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tt.wantStatus, rec.Code, rec.Body.String())
			}
			fresh, err := server.sessionManager.Get(sess.ID)
			if err != nil {
				t.Fatalf("reload session: %v", err)
			}
			if fresh.Messages[0].Content != "Queued prompt" {
				t.Fatalf("unsafe edit changed message to %q", fresh.Messages[0].Content)
			}
		})
	}
}
