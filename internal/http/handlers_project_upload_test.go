package http

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func buildProjectUploadRequest(t *testing.T, projectID, folderPath string, files map[string]string) *http.Request {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for name, content := range files {
		part, err := writer.CreateFormFile("files", name)
		if err != nil {
			t.Fatalf("failed to create form file: %v", err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatalf("failed to write form file: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	url := "/projects/file/upload?projectID=" + projectID
	if folderPath != "" {
		url += "&path=" + folderPath
	}
	req := httptest.NewRequest(http.MethodPost, url, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestHandleUploadProjectFilesWritesIntoFolder(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	projectRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(projectRoot, "docs"), 0o755); err != nil {
		t.Fatalf("failed to create docs dir: %v", err)
	}
	project := createTestProject(t, store, "project-1", "Project", projectRoot)

	req := buildProjectUploadRequest(t, project.ID, "docs", map[string]string{"notes.md": "# Notes\n"})
	rec := httptest.NewRecorder()
	server.handleUploadProjectFiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp UploadProjectFilesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.UploadedFiles) != 1 || resp.UploadedFiles[0] != "docs/notes.md" {
		t.Fatalf("uploaded_files = %v, want [docs/notes.md]", resp.UploadedFiles)
	}
	written, err := os.ReadFile(filepath.Join(projectRoot, "docs", "notes.md"))
	if err != nil {
		t.Fatalf("failed to read uploaded file: %v", err)
	}
	if string(written) != "# Notes\n" {
		t.Fatalf("uploaded content = %q", string(written))
	}
}

func TestHandleUploadProjectFilesRejectsExistingName(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	projectRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("failed to create project root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "notes.md"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("failed to write existing file: %v", err)
	}
	project := createTestProject(t, store, "project-1", "Project", projectRoot)

	rec := httptest.NewRecorder()
	server.handleUploadProjectFiles(rec, buildProjectUploadRequest(t, project.ID, "", map[string]string{"notes.md": "overwrite"}))

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	existing, err := os.ReadFile(filepath.Join(projectRoot, "notes.md"))
	if err != nil {
		t.Fatalf("failed to read existing file: %v", err)
	}
	if string(existing) != "keep" {
		t.Fatalf("existing file was overwritten: %q", string(existing))
	}
}

func TestHandleUploadProjectFilesStripsClientDirectories(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	projectRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("failed to create project root: %v", err)
	}
	project := createTestProject(t, store, "project-1", "Project", projectRoot)

	rec := httptest.NewRecorder()
	server.handleUploadProjectFiles(rec, buildProjectUploadRequest(t, project.ID, "", map[string]string{"../../escape.txt": "x"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "escape.txt")); err != nil {
		t.Fatalf("expected escape.txt inside project root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(projectRoot), "escape.txt")); err == nil {
		t.Fatal("upload escaped the project root")
	}
}

func TestHandleUploadProjectFilesRequiresFiles(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	projectRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("failed to create project root: %v", err)
	}
	project := createTestProject(t, store, "project-1", "Project", projectRoot)

	rec := httptest.NewRecorder()
	server.handleUploadProjectFiles(rec, buildProjectUploadRequest(t, project.ID, "", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
