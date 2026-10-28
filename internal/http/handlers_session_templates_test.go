package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
)

func newSessionTemplateTestRouter(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := &Server{store: store}
	router := chi.NewRouter()
	router.Get("/session-templates", server.handleListSessionTemplates)
	router.Post("/session-templates", server.handleCreateSessionTemplate)
	router.Get("/session-templates/{templateID}", server.handleGetSessionTemplate)
	router.Put("/session-templates/{templateID}", server.handleUpdateSessionTemplate)
	router.Delete("/session-templates/{templateID}", server.handleDeleteSessionTemplate)
	return server, router
}

func sendSessionTemplateRequest(t *testing.T, router http.Handler, method, path string, request SessionTemplateRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(method, path, bytes.NewReader(body)))
	if strings.Contains(recorder.Body.String(), `"slash_command"`) {
		t.Fatalf("legacy slash command exposed: %s", recorder.Body.String())
	}
	return recorder
}

func TestBuiltInSessionTemplateOverrideLifecycle(t *testing.T) {
	server, router := newSessionTemplateTestRouter(t)
	for _, focus := range []struct{ id, name string }{
		{"built-in:branch-review:architecture", "Architecture review"},
		{"built-in:branch-review:quality", "Quality review"},
		{"built-in:branch-review:security-performance", "Security and performance review"},
		{"built-in:branch-review:simplicity", "Simplicity review"},
		{"built-in:branch-review:documentation", "Documentation review"},
		{"built-in:project-audit:dependencies", "Dependency security audit"},
		{"built-in:project-audit:secrets", "Secret exposure audit"},
		{"built-in:project-audit:access-control", "Access control audit"},
		{"built-in:project-audit:test-coverage", "Test coverage audit"},
		{"built-in:project-audit:ci", "CI health audit"},
	} {
		t.Run(focus.id, func(t *testing.T) {
			id := focus.id
			// Match encodeURIComponent in Caesar, including escaped colons.
			path := "/session-templates/" + strings.ReplaceAll(id, ":", "%3A")
			request := SessionTemplateRequest{Name: focus.name, AgentID: "  unavailable-agent  ", Content: "  Custom review instructions  "}
			response := sendSessionTemplateRequest(t, router, http.MethodPut, path, request)
			if response.Code != http.StatusOK {
				t.Fatalf("first save: %d %s", response.Code, response.Body.String())
			}
			stored, err := server.store.GetSessionTemplate(id)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Content != "Custom review instructions" || stored.AgentID != "unavailable-agent" {
				t.Fatalf("unexpected stored override: %+v", stored)
			}
			response = sendSessionTemplateRequest(t, router, http.MethodGet, path, SessionTemplateRequest{})
			var fetched SessionTemplateResponse
			if response.Code != http.StatusOK {
				t.Fatalf("get by encoded ID: %d %s", response.Code, response.Body.String())
			}
			if err := json.Unmarshal(response.Body.Bytes(), &fetched); err != nil {
				t.Fatal(err)
			}
			if fetched.ID != id || fetched.Content != stored.Content {
				t.Fatalf("unexpected fetched override: %+v", fetched)
			}
			created := stored.CreatedAt
			request.Content = "Updated instructions"
			request.AgentID = "another-agent"
			response = sendSessionTemplateRequest(t, router, http.MethodPut, path, request)
			if response.Code != http.StatusOK {
				t.Fatalf("update: %d %s", response.Code, response.Body.String())
			}
			var updated SessionTemplateResponse
			if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
				t.Fatal(err)
			}
			if updated.Content != request.Content || updated.AgentID != request.AgentID || !updated.CreatedAt.Equal(created) || updated.UpdatedAt.Before(stored.UpdatedAt) {
				t.Fatalf("unexpected updated response: %+v", updated)
			}
			response = sendSessionTemplateRequest(t, router, http.MethodGet, "/session-templates", SessionTemplateRequest{})
			var list []SessionTemplateResponse
			if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
			if len(list) != 1 || list[0].ID != id || list[0].Content != request.Content {
				t.Fatalf("unexpected list: %+v", list)
			}
			response = sendSessionTemplateRequest(t, router, http.MethodDelete, path, SessionTemplateRequest{})
			if response.Code != http.StatusNoContent {
				t.Fatalf("reset: %d %s", response.Code, response.Body.String())
			}
			listAfterReset, err := server.store.ListSessionTemplates()
			if err != nil || len(listAfterReset) != 0 {
				t.Fatalf("reset left overrides: %+v, %v", listAfterReset, err)
			}
		})
	}
}

func TestSessionTemplateOverrideValidation(t *testing.T) {
	_, router := newSessionTemplateTestRouter(t)
	for _, test := range []struct {
		name, method, path string
		request            SessionTemplateRequest
		status             int
	}{
		{"unknown built-in", http.MethodPut, "/session-templates/built-in:branch-review:unknown", SessionTemplateRequest{Name: "Custom", AgentID: "agent", Content: "Text"}, http.StatusNotFound},
		{"empty content", http.MethodPut, "/session-templates/built-in:branch-review:quality", SessionTemplateRequest{Name: "Quality review", AgentID: "agent", Content: "  "}, http.StatusBadRequest},
		{"empty agent", http.MethodPut, "/session-templates/built-in:branch-review:quality", SessionTemplateRequest{Name: "Quality review", AgentID: "  ", Content: "Text"}, http.StatusBadRequest},
		{"changed name", http.MethodPut, "/session-templates/built-in:branch-review:quality", SessionTemplateRequest{Name: "Renamed", AgentID: "agent", Content: "Text"}, http.StatusBadRequest},
		{"custom create", http.MethodPost, "/session-templates", SessionTemplateRequest{Name: "Custom", AgentID: "agent", Content: "Text"}, http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := sendSessionTemplateRequest(t, router, test.method, test.path, test.request)
			if response.Code != test.status {
				t.Fatalf("got %d want %d: %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}

func TestSessionTemplateCRUD(t *testing.T) {
	server, router := newSessionTemplateTestRouter(t)
	request := SessionTemplateRequest{Name: "  Custom  ", Content: "  Prompt text  ", AgentID: "  missing-Agent:portable  "}
	response := sendSessionTemplateRequest(t, router, http.MethodPost, "/session-templates", request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	var created SessionTemplateResponse
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Name != "Custom" || created.Content != "Prompt text" || created.AgentID != "missing-Agent:portable" || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("unexpected created template: %+v", created)
	}
	path := "/session-templates/" + created.ID
	request = SessionTemplateRequest{Name: "  Updated  ", Content: "  Updated prompt  ", AgentID: "  different-agent  "}
	response = sendSessionTemplateRequest(t, router, http.MethodPut, path, request)
	if response.Code != http.StatusOK {
		t.Fatalf("update: %d %s", response.Code, response.Body.String())
	}
	response = sendSessionTemplateRequest(t, router, http.MethodGet, path, SessionTemplateRequest{})
	var fetched SessionTemplateResponse
	if response.Code != http.StatusOK {
		t.Fatalf("get: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &fetched); err != nil {
		t.Fatal(err)
	}
	if fetched.ID != created.ID || fetched.Name != "Updated" || fetched.Content != "Updated prompt" || fetched.AgentID != "different-agent" || !fetched.CreatedAt.Equal(created.CreatedAt) || fetched.UpdatedAt.Before(created.UpdatedAt) {
		t.Fatalf("unexpected updated template: %+v", fetched)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		for _, invalid := range []SessionTemplateRequest{
			{Name: "Custom", Content: "Text", AgentID: "  "},
			{Name: "Custom", Content: "  ", AgentID: "agent"},
			{Name: "  ", Content: "Text", AgentID: "agent"},
		} {
			requestPath := "/session-templates"
			if method == http.MethodPut {
				requestPath = path
			}
			response = sendSessionTemplateRequest(t, router, method, requestPath, invalid)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid %s %+v: %d %s", method, invalid, response.Code, response.Body.String())
			}
		}
	}
	response = sendSessionTemplateRequest(t, router, http.MethodGet, "/session-templates", SessionTemplateRequest{})
	var list []SessionTemplateResponse
	if response.Code != http.StatusOK {
		t.Fatalf("list: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0] != fetched {
		t.Fatalf("unexpected list: %+v", list)
	}
	response = sendSessionTemplateRequest(t, router, http.MethodDelete, path, SessionTemplateRequest{})
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", response.Code, response.Body.String())
	}
	if _, err := server.store.GetSessionTemplate(created.ID); err == nil {
		t.Fatal("deleted template still exists")
	}
}

func TestLegacySessionTemplateAPI(t *testing.T) {
	server, router := newSessionTemplateTestRouter(t)
	created := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := created.Add(time.Hour)
	id := "built-in:branch-review:quality"
	if err := server.store.SaveSessionTemplate(&storage.SessionTemplate{
		ID: id, Name: "Quality review", Content: "Legacy prompt", CreatedAt: created, UpdatedAt: updated,
	}); err != nil {
		t.Fatal(err)
	}
	path := "/session-templates/" + id
	for _, getPath := range []string{path, "/session-templates"} {
		response := sendSessionTemplateRequest(t, router, http.MethodGet, getPath, SessionTemplateRequest{})
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"agent_id":""`) || !strings.Contains(response.Body.String(), `"content":"Legacy prompt"`) || !strings.Contains(response.Body.String(), updated.Format(time.RFC3339)) {
			t.Fatalf("legacy read: %d %s", response.Code, response.Body.String())
		}
	}
	response := sendSessionTemplateRequest(t, router, http.MethodPut, path, SessionTemplateRequest{Name: "Quality review", Content: "Updated prompt", AgentID: "replacement-agent"})
	if response.Code != http.StatusOK {
		t.Fatalf("update legacy override: %d %s", response.Code, response.Body.String())
	}
	var saved SessionTemplateResponse
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.CreatedAt.Equal(created) || saved.AgentID != "replacement-agent" || !saved.UpdatedAt.After(updated) {
		t.Fatalf("unexpected legacy override update: %+v", saved)
	}
}

func TestSessionTemplateJSONContract(t *testing.T) {
	_, router := newSessionTemplateTestRouter(t)
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"missing agent", `{"name":"Custom","content":"Text","slash_command":"custom"}`, http.StatusBadRequest},
		{"multiple agents", `{"name":"Custom","content":"Text","agent_id":["one","two"]}`, http.StatusBadRequest},
		{"non-string agent", `{"name":"Custom","content":"Text","agent_id":42}`, http.StatusBadRequest},
		{"null agent", `{"name":"Custom","content":"Text","agent_id":null}`, http.StatusBadRequest},
		{"obsolete slash command ignored", `{"name":"Custom","content":"Text","agent_id":"unavailable","slash_command":"/help !"}`, http.StatusCreated},
		{"same agent reused", `{"name":"Another","content":"Text","agent_id":"unavailable"}`, http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/session-templates", strings.NewReader(test.body)))
			if response.Code != test.status || strings.Contains(response.Body.String(), `"slash_command"`) {
				t.Fatalf("got %d want %d: %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}
