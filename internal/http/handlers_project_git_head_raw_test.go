package http

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"testing"
)

func TestHandleProjectGitHeadFileRawServesCommittedGLB(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	server, projectID, repoDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, repoDir)
	writeGitTestFile(t, repoDir, "model.glb", "old-model")
	runGitForMindTest(t, repoDir, "add", "model.glb")
	runGitForMindTest(t, repoDir, "-c", "commit.gpgsign=false", "commit", "-m", "add model")
	writeGitTestFile(t, repoDir, "model.glb", "new-model")

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/projects/git/head-raw?projectID="+url.QueryEscape(projectID)+"&path="+url.QueryEscape(path), nil)
		rec := httptest.NewRecorder()
		server.router.ServeHTTP(rec, req)
		return rec
	}

	rec := get("model.glb")
	if rec.Code != http.StatusOK || rec.Body.String() != "old-model" {
		t.Fatalf("expected HEAD content, got %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "model/gltf-binary" {
		t.Fatalf("unexpected content type %q", ct)
	}
	if rec := get("missing.glb"); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for file not in HEAD, got %d", rec.Code)
	}
	if rec := get("notes.txt"); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-previewable file, got %d", rec.Code)
	}
}
