package http

import (
	_ "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestZZTmpGitHooksUpload(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	projectRoot := filepath.Join(t.TempDir(), "repo")
	_ = os.MkdirAll(filepath.Join(projectRoot, ".git", "hooks"), 0o755)
	project := createTestProject(t, store, "project-1", "Project", projectRoot)

	req := buildProjectUploadRequest(t, project.ID, ".git/hooks", map[string]string{"post-checkout": "#!/bin/sh\necho pwned\n"})
	rec := httptest.NewRecorder()
	server.handleUploadProjectFiles(rec, req)
	t.Logf("status=%d body=%s", rec.Code, rec.Body.String())
	if info, err := os.Stat(filepath.Join(projectRoot, ".git", "hooks", "post-checkout")); err == nil {
		t.Logf("BUG CONFIRMED: wrote git hook, mode=%v", info.Mode())
	}
}
