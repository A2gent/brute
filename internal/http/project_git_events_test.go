package http

import (
	"bufio"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandleProjectGitEventsEmitsOnCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}
	server, projectID, projectDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, projectDir)

	ts := httptest.NewServer(server.router)
	defer ts.Close()
	resp, err := ts.Client().Get(ts.URL + "/projects/git/events?projectID=" + url.QueryEscape(projectID))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	if line, _ := reader.ReadString('\n'); !strings.HasPrefix(line, ":ok") {
		t.Fatalf("expected stream preamble, got %q", line)
	}

	cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-m", "evt")
	cmd.Dir = projectDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v %s", err, out)
	}

	got := make(chan string, 1)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "event: git_changed") {
				got <- line
				return
			}
		}
	}()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no git_changed event after commit")
	}
}

func TestHandleProjectGitEventsEmitsOnWorktreeAdd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}
	server, projectID, projectDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, projectDir)

	ts := httptest.NewServer(server.router)
	defer ts.Close()
	resp, err := ts.Client().Get(ts.URL + "/projects/git/events?projectID=" + url.QueryEscape(projectID))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	if line, _ := reader.ReadString('\n'); !strings.HasPrefix(line, ":ok") {
		t.Fatalf("expected stream preamble, got %q", line)
	}

	// Detached add only touches .git/worktrees, not refs/, so it proves that dir is watched.
	cmd := exec.Command("git", "worktree", "add", "--detach", filepath.Join(t.TempDir(), "wt"))
	cmd.Dir = projectDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v %s", err, out)
	}

	got := make(chan string, 1)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "event: git_changed") {
				got <- line
				return
			}
		}
	}()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no git_changed event after worktree add")
	}
}
