package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestHandleProjectGitHistory_NoGitMetadataReturnsEmptyHistory(t *testing.T) {
	server, projectID, projectDir := newProjectFileTestServer(t)

	target := "/projects/git/history?projectID=" + url.QueryEscape(projectID) + "&limit=160"
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected OK for non-git project root, got %d: %s", rec.Code, rec.Body.String())
	}

	var response ProjectGitHistoryResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response.RootFolder != projectDir {
		t.Fatalf("expected root folder %q, got %q", projectDir, response.RootFolder)
	}
	if len(response.Branches) != 0 || len(response.Commits) != 0 || response.CurrentBranch != "" {
		t.Fatalf("expected empty history for non-git root, got %#v", response)
	}
}
