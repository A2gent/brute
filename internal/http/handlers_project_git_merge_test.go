package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
)

func TestHandleProjectGitMergeMergesSelectedBranchIntoCurrent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	server, projectID, repoDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, repoDir)
	runGitForMindTest(t, repoDir, "checkout", "-b", "feature/notes")
	writeGitTestFile(t, repoDir, "notes.txt", "from feature\n")
	runGitForMindTest(t, repoDir, "add", "notes.txt")
	runGitForMindTest(t, repoDir, "-c", "commit.gpgsign=false", "commit", "-m", "add notes")
	runGitForMindTest(t, repoDir, "checkout", "master")

	target := "/projects/git/merge?projectID=" + url.QueryEscape(projectID)
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"branch":"feature/notes"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected merge to succeed, got %d: %s", rec.Code, rec.Body.String())
	}

	var response ProjectGitPullResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode merge response: %v", err)
	}
	if !strings.Contains(strings.ToLower(response.Output), "feature/notes") && !strings.Contains(strings.ToLower(response.Output), "fast-forward") {
		t.Fatalf("expected merge output to mention the source branch or fast-forward, got %q", response.Output)
	}

	current, err := runGitCommand(repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("failed to read current branch: %v", err)
	}
	if strings.TrimSpace(current) != "master" {
		t.Fatalf("expected to stay on master after merge, got %q", current)
	}
	if _, err := runGitCommand(repoDir, "cat-file", "-e", "HEAD:notes.txt"); err != nil {
		t.Fatalf("expected notes.txt to exist on master after merge: %v", err)
	}
}

func TestHandleProjectGitMergeRejectsCurrentBranchAndConflicts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	server, projectID, repoDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, repoDir)

	target := "/projects/git/merge?projectID=" + url.QueryEscape(projectID)
	sameBranch := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"branch":"master"}`))
	sameBranch.Header.Set("Content-Type", "application/json")
	sameRec := httptest.NewRecorder()
	server.router.ServeHTTP(sameRec, sameBranch)
	if sameRec.Code != http.StatusBadRequest {
		t.Fatalf("expected self-merge to fail, got %d: %s", sameRec.Code, sameRec.Body.String())
	}

	emptyBranch := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"branch":"  "}`))
	emptyBranch.Header.Set("Content-Type", "application/json")
	emptyRec := httptest.NewRecorder()
	server.router.ServeHTTP(emptyRec, emptyBranch)
	if emptyRec.Code != http.StatusBadRequest {
		t.Fatalf("expected empty branch to fail, got %d: %s", emptyRec.Code, emptyRec.Body.String())
	}

	runGitForMindTest(t, repoDir, "checkout", "-b", "feature/conflict")
	writeGitTestFile(t, repoDir, "README.md", "feature change\n")
	runGitForMindTest(t, repoDir, "add", "README.md")
	runGitForMindTest(t, repoDir, "-c", "commit.gpgsign=false", "commit", "-m", "feature edit")
	runGitForMindTest(t, repoDir, "checkout", "master")
	writeGitTestFile(t, repoDir, "README.md", "master change\n")
	runGitForMindTest(t, repoDir, "add", "README.md")
	runGitForMindTest(t, repoDir, "-c", "commit.gpgsign=false", "commit", "-m", "master edit")

	conflictReq := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"branch":"feature/conflict"}`))
	conflictReq.Header.Set("Content-Type", "application/json")
	conflictRec := httptest.NewRecorder()
	server.router.ServeHTTP(conflictRec, conflictReq)
	if conflictRec.Code != http.StatusBadRequest {
		t.Fatalf("expected conflicting merge to fail, got %d: %s", conflictRec.Code, conflictRec.Body.String())
	}
	if !strings.Contains(strings.ToLower(conflictRec.Body.String()), "conflict") && !strings.Contains(strings.ToLower(conflictRec.Body.String()), "merge") {
		t.Fatalf("expected conflict error to mention merge/conflict, got %s", conflictRec.Body.String())
	}
}
