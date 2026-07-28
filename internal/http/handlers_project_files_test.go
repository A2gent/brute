package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHandleCreateProjectFolderExistingDirectoryIsIdempotent(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	projectRoot := filepath.Join(t.TempDir(), "repo")
	agentDir := filepath.Join(projectRoot, "agents", "spareto-developer")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("failed to create agent dir: %v", err)
	}
	project := createTestProject(t, store, "project-1", "Project", projectRoot)

	body, err := json.Marshal(CreateFolderRequest{Path: "agents/spareto-developer"})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/projects/folder?projectID="+project.ID, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleCreateProjectFolder(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for existing directory, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp CreateFolderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Path != "agents/spareto-developer" {
		t.Fatalf("path = %q, want agents/spareto-developer", resp.Path)
	}
}

func TestHandleCreateProjectFolderExistingFileReturnsConflict(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	projectRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("failed to create project root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, "agents"), 0o755); err != nil {
		t.Fatalf("failed to create agents dir: %v", err)
	}
	filePath := filepath.Join(projectRoot, "agents", "blocked")
	if err := os.WriteFile(filePath, []byte("not a folder"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	project := createTestProject(t, store, "project-1", "Project", projectRoot)

	body, err := json.Marshal(CreateFolderRequest{Path: "agents/blocked"})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/projects/folder?projectID="+project.ID, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleCreateProjectFolder(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for existing file, got %d: %s", rec.Code, rec.Body.String())
	}
}
