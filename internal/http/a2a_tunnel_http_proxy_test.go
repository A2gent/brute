package http

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func newBruteHTTPProxyTestServer(t *testing.T) (*Server, storage.Store) {
	t.Helper()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)
	return server, store
}

func decodeBruteHTTPProxyResponse(t *testing.T, payload []byte) bruteHTTPProxyResponseEnvelope {
	t.Helper()

	var envelope bruteHTTPProxyResponseEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("decode response envelope: %v", err)
	}
	return envelope
}

func decodeBruteHTTPBodyBase64(t *testing.T, body string) string {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("decode body base64: %v", err)
	}
	return string(decoded)
}

func TestHandleBruteHTTPInternalEvent_ProxiesHealth(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	payload, err := json.Marshal(bruteHTTPProxyEnvelope{
		Metadata: map[string]interface{}{"internal_event": bruteHTTPInternalEvent},
		HTTP:     bruteHTTPProxyRequest{Method: http.MethodGet, Path: "/health"},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	responsePayload, conversationID, err := server.handleBruteHTTPInternalEvent(t.Context(), payload)
	if err != nil {
		t.Fatalf("handleBruteHTTPInternalEvent: %v", err)
	}
	if conversationID != "" {
		t.Fatalf("expected empty conversation id, got %q", conversationID)
	}

	envelope := decodeBruteHTTPProxyResponse(t, responsePayload)
	if envelope.HTTP.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", envelope.HTTP.StatusCode)
	}
	if !strings.HasPrefix(envelope.HTTP.ContentType, "application/json") {
		t.Fatalf("expected json content type, got %q", envelope.HTTP.ContentType)
	}
	body := decodeBruteHTTPBodyBase64(t, envelope.HTTP.BodyBase64)
	if !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("expected health json body, got %s", body)
	}
}

func TestHandleBruteHTTPInternalEvent_ProxiesSessions(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	createReq := httptest.NewRequest(http.MethodPost, "/sessions/", strings.NewReader(`{"agent_id":"build","task":"hello","queued":true}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.router.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected session create 201, got %d: %s", createRec.Code, createRec.Body.String())
	}

	payload, err := json.Marshal(bruteHTTPProxyEnvelope{
		Metadata: map[string]interface{}{"internal_event": bruteHTTPInternalEvent},
		HTTP:     bruteHTTPProxyRequest{Method: http.MethodGet, Path: "/sessions/"},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	responsePayload, _, err := server.handleBruteHTTPInternalEvent(t.Context(), payload)
	if err != nil {
		t.Fatalf("handleBruteHTTPInternalEvent: %v", err)
	}
	envelope := decodeBruteHTTPProxyResponse(t, responsePayload)
	if envelope.HTTP.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", envelope.HTTP.StatusCode)
	}
	body := decodeBruteHTTPBodyBase64(t, envelope.HTTP.BodyBase64)
	if !strings.Contains(body, `"agent_id":"build"`) {
		t.Fatalf("expected sessions list body, got %s", body)
	}
}

func TestHandleBruteHTTPInternalEvent_RejectsUnsafePathAndHeaders(t *testing.T) {
	t.Parallel()

	server, _ := newBruteHTTPProxyTestServer(t)
	badPathPayload, err := json.Marshal(bruteHTTPProxyEnvelope{
		Metadata: map[string]interface{}{"internal_event": bruteHTTPInternalEvent},
		HTTP:     bruteHTTPProxyRequest{Method: http.MethodGet, Path: "../../sessions"},
	})
	if err != nil {
		t.Fatalf("marshal bad path payload: %v", err)
	}
	if _, _, err := server.handleBruteHTTPInternalEvent(t.Context(), badPathPayload); err == nil {
		t.Fatal("expected invalid brute path to fail")
	}

	headersPayload, err := json.Marshal(bruteHTTPProxyEnvelope{
		Metadata: map[string]interface{}{"internal_event": bruteHTTPInternalEvent},
		HTTP: bruteHTTPProxyRequest{
			Method: http.MethodGet,
			Path:   "/health",
			Headers: map[string][]string{
				"Authorization":  {"Bearer secret"},
				"Cookie":         {"session=secret"},
				"X-API-Key":      {"square-key"},
				"Sec-Fetch-Site": {"same-origin"},
				"X-Ok":           {" value "},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal header payload: %v", err)
	}

	responsePayload, _, err := server.handleBruteHTTPInternalEvent(t.Context(), headersPayload)
	if err != nil {
		t.Fatalf("handleBruteHTTPInternalEvent with headers: %v", err)
	}
	envelope := decodeBruteHTTPProxyResponse(t, responsePayload)
	if envelope.HTTP.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", envelope.HTTP.StatusCode)
	}
	if got := sanitizeBruteHTTPRequestHeaders(map[string][]string{
		"Authorization":  {"Bearer secret"},
		"Cookie":         {"session=secret"},
		"X-API-Key":      {"square-key"},
		"Sec-Fetch-Site": {"same-origin"},
		"X-Ok":           {" value "},
	}); len(got) != 1 || got["X-Ok"][0] != "value" {
		t.Fatalf("expected only safe headers to survive, got %#v", got)
	}
}

func TestHandleBruteHTTPInternalEvent_PreservesBlobResponse(t *testing.T) {
	t.Parallel()

	server, store := newBruteHTTPProxyTestServer(t)
	projectDir := t.TempDir()
	pdfPath := filepath.Join(projectDir, "sample.pdf")
	pdfBody := []byte("%PDF-1.4\nhello")
	if err := os.WriteFile(pdfPath, pdfBody, 0o644); err != nil {
		t.Fatalf("write pdf file: %v", err)
	}
	projectFolder := projectDir
	project := &storage.Project{ID: "proj-1", Name: "Test Project", Folder: &projectFolder}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("save project: %v", err)
	}

	payload, err := json.Marshal(bruteHTTPProxyEnvelope{
		Metadata: map[string]interface{}{"internal_event": bruteHTTPInternalEvent},
		HTTP: bruteHTTPProxyRequest{
			Method:   http.MethodGet,
			Path:     "/projects/file/raw",
			RawQuery: "projectID=proj-1&path=sample.pdf",
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	responsePayload, _, err := server.handleBruteHTTPInternalEvent(t.Context(), payload)
	if err != nil {
		t.Fatalf("handleBruteHTTPInternalEvent: %v", err)
	}
	envelope := decodeBruteHTTPProxyResponse(t, responsePayload)
	if envelope.HTTP.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", envelope.HTTP.StatusCode)
	}
	if got := envelope.HTTP.ContentType; got != "application/pdf" {
		t.Fatalf("expected application/pdf, got %q", got)
	}
	decoded, err := base64.StdEncoding.DecodeString(envelope.HTTP.BodyBase64)
	if err != nil {
		t.Fatalf("decode base64 body: %v", err)
	}
	if string(decoded) != string(pdfBody) {
		t.Fatalf("expected raw pdf body %q, got %q", string(pdfBody), string(decoded))
	}
}
