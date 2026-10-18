package http

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAddSessionWorktreeMatchesBaseRefAndKeepsIgnoredCaches(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}
	root := t.TempDir()
	initMindGitTestRepo(t, root)
	writeGitTestFile(t, root, ".gitignore", ".cache/\n.worktrees/\n")
	writeGitTestFile(t, root, "changed.txt", "base\n")
	writeGitTestFile(t, root, "deleted.txt", "base\n")
	runGitForMindTest(t, root, "add", "-A")
	runGitForMindTest(t, root, "-c", "commit.gpgsign=false", "commit", "-m", "base")

	// The main folder drifts from the base: another branch, local edits, junk and caches.
	runGitForMindTest(t, root, "checkout", "-b", "other")
	writeGitTestFile(t, root, "other-only.txt", "other\n")
	runGitForMindTest(t, root, "add", "other-only.txt")
	runGitForMindTest(t, root, "-c", "commit.gpgsign=false", "commit", "-m", "other")
	writeGitTestFile(t, root, "changed.txt", "local edit\n")
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	writeGitTestFile(t, root, "untracked.txt", "junk\n")
	if err := os.MkdirAll(filepath.Join(root, ".cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeGitTestFile(t, root, ".cache/import", "cache\n")

	// The worktrees folder lives inside the project to cover the recursion guard.
	path := filepath.Join(root, ".worktrees", "session")
	if err := addSessionWorktree(root, path, "a2gent/session", "master"); err != nil {
		t.Fatalf("addSessionWorktree: %v", err)
	}

	for name, want := range map[string]string{"README.md": "initial\n", "changed.txt": "base\n", "deleted.txt": "base\n"} {
		if got, err := os.ReadFile(filepath.Join(path, name)); err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	for _, name := range []string{"other-only.txt", "untracked.txt", ".worktrees"} {
		if _, err := os.Stat(filepath.Join(path, name)); !os.IsNotExist(err) {
			t.Fatalf("%s should not be in the worktree: %v", name, err)
		}
	}
	if status, err := runGitCommand(path, "status", "--porcelain"); err != nil || status != "" {
		t.Fatalf("worktree status = %q, %v; want clean", status, err)
	}
	if branch, _ := currentGitBranch(path); branch != "a2gent/session" {
		t.Fatalf("worktree branch = %q", branch)
	}
	if runtime.GOOS == "darwin" {
		if got, err := os.ReadFile(filepath.Join(path, ".cache", "import")); err != nil || string(got) != "cache\n" {
			t.Fatalf("ignored cache was not cloned: %q, %v", got, err)
		}
	}
}
