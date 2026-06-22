package http

import (
	"bytes"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestProjectURLPatternsRoundTripThroughProjectAPI(t *testing.T) {
	server, store := newProjectsAPITestServer(t)
	defer store.Close()

	createPayload := CreateProjectRequest{
		Name: "URL Project",
		URLPatterns: []string{
			" https://example.com/* ",
			"https://*.example.test/app/*",
			"https://example.com/*",
		},
	}
	createRec := requestProjectJSON(t, server, stdhttp.MethodPost, "/projects", createPayload)
	if createRec.Code != stdhttp.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", createRec.Code, createRec.Body.String())
	}

	var created ProjectResponse
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	wantPatterns := []string{"https://example.com/*", "https://*.example.test/app/*"}
	if !reflect.DeepEqual(created.URLPatterns, wantPatterns) {
		t.Fatalf("created URLPatterns = %#v, want %#v", created.URLPatterns, wantPatterns)
	}

	renameRec := requestProjectJSON(t, server, stdhttp.MethodPut, "/projects/"+created.ID, UpdateProjectRequest{
		Name: stringPtr("Renamed URL Project"),
	})
	if renameRec.Code != stdhttp.StatusOK {
		t.Fatalf("rename project status = %d, body = %s", renameRec.Code, renameRec.Body.String())
	}

	var renamed ProjectResponse
	if err := json.NewDecoder(renameRec.Body).Decode(&renamed); err != nil {
		t.Fatalf("decode rename response: %v", err)
	}
	if renamed.Name != "Renamed URL Project" {
		t.Fatalf("renamed project name = %q, want %q", renamed.Name, "Renamed URL Project")
	}
	if !reflect.DeepEqual(renamed.URLPatterns, wantPatterns) {
		t.Fatalf("renamed URLPatterns = %#v, want %#v", renamed.URLPatterns, wantPatterns)
	}

	updatePatterns := []string{"https://docs.example.com/*"}
	updateRec := requestProjectJSON(t, server, stdhttp.MethodPut, "/projects/"+created.ID, UpdateProjectRequest{
		URLPatterns: &updatePatterns,
	})
	if updateRec.Code != stdhttp.StatusOK {
		t.Fatalf("update patterns status = %d, body = %s", updateRec.Code, updateRec.Body.String())
	}

	getReq := httptest.NewRequest(stdhttp.MethodGet, "/projects/"+created.ID, nil)
	getRec := httptest.NewRecorder()
	server.router.ServeHTTP(getRec, getReq)
	if getRec.Code != stdhttp.StatusOK {
		t.Fatalf("get project status = %d, body = %s", getRec.Code, getRec.Body.String())
	}

	var got ProjectResponse
	if err := json.NewDecoder(getRec.Body).Decode(&got); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if !reflect.DeepEqual(got.URLPatterns, updatePatterns) {
		t.Fatalf("got URLPatterns = %#v, want %#v", got.URLPatterns, updatePatterns)
	}
}

func newProjectsAPITestServer(t *testing.T) (*Server, *storage.SQLiteStore) {
	t.Helper()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()
	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)
	return server, store
}

func requestProjectJSON(t *testing.T, server *Server, method string, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	return rec
}

func stringPtr(value string) *string {
	return &value
}
