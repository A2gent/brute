package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestProjectFileEditorAllowsCodeFiles(t *testing.T) {
	server, projectID, projectDir := newProjectFileTestServer(t)

	srcDir := filepath.Join(projectDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("failed to create source directory: %v", err)
	}
	filePath := filepath.Join(srcDir, "app.ts")
	initialContent := "export const answer = 42;\n"
	if err := os.WriteFile(filePath, []byte(initialContent), 0o644); err != nil {
		t.Fatalf("failed to write code file: %v", err)
	}

	rec := requestProjectFile(t, server, http.MethodGet, projectID, "src/app.ts", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var loaded MindFileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &loaded); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if loaded.Content != initialContent {
		t.Fatalf("expected %q, got %q", initialContent, loaded.Content)
	}

	updatedContent := "export const answer = 43;\n"
	payload, err := json.Marshal(UpdateMindFileRequest{
		Path:    "src/app.ts",
		Content: updatedContent,
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}
	rec = requestProjectFile(t, server, http.MethodPut, projectID, "", bytes.NewReader(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	written, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(written) != updatedContent {
		t.Fatalf("expected saved content %q, got %q", updatedContent, string(written))
	}
}

func TestProjectFileEditorRejectsUnsupportedFiles(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		content     []byte
		wantMessage string
	}{
		{
			name:        "image extension",
			path:        "logo.png",
			content:     []byte("not actually an image\n"),
			wantMessage: "Images and videos cannot be opened",
		},
		{
			name:        "oversized text",
			path:        "large.txt",
			content:     bytes.Repeat([]byte("a"), maxProjectEditableFileBytes+1),
			wantMessage: "File is too large to open",
		},
		{
			name:        "too many lines",
			path:        "many-lines.txt",
			content:     []byte(strings.Repeat("x\n", maxProjectEditableFileLines+1)),
			wantMessage: "File has too many lines to open",
		},
		{
			name:        "binary content",
			path:        "data.bin",
			content:     []byte{'a', 0, 'b'},
			wantMessage: "File must be UTF-8 text to open",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, projectID, projectDir := newProjectFileTestServer(t)
			fullPath := filepath.Join(projectDir, filepath.FromSlash(tt.path))
			if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
				t.Fatalf("failed to create parent directory: %v", err)
			}
			if err := os.WriteFile(fullPath, tt.content, 0o644); err != nil {
				t.Fatalf("failed to write test file: %v", err)
			}

			rec := requestProjectFile(t, server, http.MethodGet, projectID, tt.path, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantMessage) {
				t.Fatalf("expected response to contain %q, got %s", tt.wantMessage, rec.Body.String())
			}
		})
	}
}

func newProjectFileTestServer(t *testing.T) (*Server, string, string) {
	t.Helper()

	dataDir := t.TempDir()
	projectDir := t.TempDir()

	store, err := storage.NewSQLiteStore(dataDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("failed to close store: %v", err)
		}
	})

	cfg := config.DefaultConfig()
	cfg.DataPath = dataDir
	cfg.WorkDir = projectDir
	toolManager := tools.NewManager(projectDir)
	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, toolManager, sessionManager, store, speechcache.New(0), 0)

	projectID := "project-file-test"
	now := time.Now()
	folder := projectDir
	if err := store.SaveProject(&storage.Project{
		ID:        projectID,
		Name:      "Project File Test",
		Folder:    &folder,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	return server, projectID, projectDir
}

func requestProjectFile(t *testing.T, server *Server, method string, projectID string, path string, body *bytes.Reader) *httptest.ResponseRecorder {
	t.Helper()

	target := "/projects/file?projectID=" + url.QueryEscape(projectID)
	if path != "" {
		target += "&path=" + url.QueryEscape(path)
	}
	var requestBody *bytes.Reader
	if body == nil {
		requestBody = bytes.NewReader(nil)
	} else {
		requestBody = body
	}
	req := httptest.NewRequest(method, target, requestBody)
	if method == http.MethodPut || method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	return rec
}
