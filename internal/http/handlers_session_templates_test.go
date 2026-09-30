package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	return recorder
}

func TestBuiltInSessionTemplateOverrideLifecycle(t *testing.T) {
	server, router := newSessionTemplateTestRouter(t)
	for _, focus := range []struct{ id, name, command string }{
		{"architecture", "Architecture review", "architecture-review"},
		{"quality", "Quality review", "quality-review"},
		{"security-performance", "Security and performance review", "security-performance-review"},
		{"simplicity", "Simplicity review", "simplicity-review"},
		{"documentation", "Documentation review", "documentation-review"},
	} {
		t.Run(focus.id, func(t *testing.T) {
			id := "built-in:branch-review:" + focus.id
			// Match encodeURIComponent in Caesar, including escaped colons.
			path := "/session-templates/" + strings.ReplaceAll(id, ":", "%3A")
			request := SessionTemplateRequest{Name: focus.name, SlashCommand: focus.command, Content: "  Custom review instructions  "}
			response := sendSessionTemplateRequest(t, router, http.MethodPut, path, request)
			if response.Code != http.StatusOK {
				t.Fatalf("first save: %d %s", response.Code, response.Body.String())
			}
			stored, err := server.store.GetSessionTemplate(id)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Content != "Custom review instructions" || stored.SlashCommand != focus.command {
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
			response = sendSessionTemplateRequest(t, router, http.MethodPut, path, request)
			if response.Code != http.StatusOK {
				t.Fatalf("update: %d %s", response.Code, response.Body.String())
			}
			var updated SessionTemplateResponse
			if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil {
				t.Fatal(err)
			}
			if updated.Content != request.Content || !updated.CreatedAt.Equal(created) {
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
		{"reserved create", http.MethodPost, "/session-templates", SessionTemplateRequest{Name: "Custom", SlashCommand: "quality-review", Content: "Text"}, http.StatusBadRequest},
		{"unknown built-in", http.MethodPut, "/session-templates/built-in:branch-review:unknown", SessionTemplateRequest{Name: "Custom", SlashCommand: "quality-review", Content: "Text"}, http.StatusNotFound},
		{"empty content", http.MethodPut, "/session-templates/built-in:branch-review:quality", SessionTemplateRequest{Name: "Quality review", SlashCommand: "quality-review", Content: "  "}, http.StatusBadRequest},
		{"changed command", http.MethodPut, "/session-templates/built-in:branch-review:quality", SessionTemplateRequest{Name: "Quality review", SlashCommand: "architecture-review", Content: "Text"}, http.StatusBadRequest},
		{"changed name", http.MethodPut, "/session-templates/built-in:branch-review:quality", SessionTemplateRequest{Name: "Renamed", SlashCommand: "quality-review", Content: "Text"}, http.StatusBadRequest},
		{"custom create", http.MethodPost, "/session-templates", SessionTemplateRequest{Name: "Custom", SlashCommand: "custom-review", Content: "Text"}, http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := sendSessionTemplateRequest(t, router, test.method, test.path, test.request)
			if response.Code != test.status {
				t.Fatalf("got %d want %d: %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}
