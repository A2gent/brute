package http

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBuildProjectGitBranchesReadsForEachRefFields(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	repoRoot := t.TempDir()
	runGitForMindTest(t, repoRoot, "init")
	runGitForMindTest(t, repoRoot, "config", "user.email", "test@example.com")
	runGitForMindTest(t, repoRoot, "config", "user.name", "Test User")

	writeGitTestFile(t, repoRoot, "README.md", "initial\n")
	runGitForMindTest(t, repoRoot, "add", "README.md")
	runGitForMindTest(t, repoRoot, "-c", "commit.gpgsign=false", "commit", "-m", "initial")
	runGitForMindTest(t, repoRoot, "branch", "alpha")
	runGitForMindTest(t, repoRoot, "checkout", "-b", "feature/recent")

	writeGitTestFile(t, repoRoot, "README.md", "changed\n")
	runGitForMindTest(t, repoRoot, "add", "README.md")
	runGitForMindTest(t, repoRoot, "-c", "commit.gpgsign=false", "commit", "-m", "recent")

	branches, err := buildProjectGitBranches(repoRoot, "feature/recent")
	if err != nil {
		t.Fatalf("expected branches, got error: %v", err)
	}
	if len(branches) < 2 {
		t.Fatalf("expected at least two branches, got %#v", branches)
	}

	var current *ProjectGitBranch
	var alpha *ProjectGitBranch
	for i := range branches {
		switch branches[i].Name {
		case "feature/recent":
			current = &branches[i]
		case "alpha":
			alpha = &branches[i]
		}
	}

	if current == nil {
		t.Fatalf("expected current branch in %#v", branches)
	}
	if !current.Current {
		t.Fatalf("expected feature/recent to be current, got %#v", current)
	}
	if current.Remote {
		t.Fatalf("expected feature/recent to be local, got %#v", current)
	}
	if current.UpdatedAt == "" {
		t.Fatalf("expected feature/recent updated_at to be populated, got %#v", current)
	}
	if alpha == nil {
		t.Fatalf("expected alpha branch in %#v", branches)
	}
	if alpha.Current {
		t.Fatalf("expected alpha not to be current, got %#v", alpha)
	}
}

func TestProjectGitBranchChangesTargetUsesLocalMaster(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	repoRoot := t.TempDir()
	initMindGitTestRepo(t, repoRoot)

	target := projectGitBranchChangesTarget(repoRoot)
	if target.Available {
		t.Fatalf("expected master branch not to expose branch changes, got %#v", target)
	}
	if target.CurrentBranch != "master" {
		t.Fatalf("expected current branch master, got %#v", target)
	}

	runGitForMindTest(t, repoRoot, "checkout", "-b", "feature/local-base")
	target = projectGitBranchChangesTarget(repoRoot)
	if !target.Available {
		t.Fatalf("expected feature branch changes to be available, got %#v", target)
	}
	if target.BaseBranch != "master" || target.BaseRef != "refs/heads/master" {
		t.Fatalf("expected local master base, got %#v", target)
	}
}

func TestProjectGitBranchChangesTargetFallsBackToOriginMaster(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	repoRoot := t.TempDir()
	initMindGitTestRepo(t, repoRoot)
	runGitForMindTest(t, repoRoot, "update-ref", "refs/remotes/origin/master", "HEAD")
	runGitForMindTest(t, repoRoot, "checkout", "-b", "feature/remote-base")
	runGitForMindTest(t, repoRoot, "branch", "-D", "master")

	target := projectGitBranchChangesTarget(repoRoot)
	if !target.Available {
		t.Fatalf("expected remote master branch changes to be available, got %#v", target)
	}
	if target.BaseBranch != "origin/master" || target.BaseRef != "refs/remotes/origin/master" {
		t.Fatalf("expected origin/master base, got %#v", target)
	}
}

func TestProjectGitBranchChangesTargetInfersDetachedLocalBranch(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	repoRoot := t.TempDir()
	initMindGitTestRepo(t, repoRoot)
	runGitForMindTest(t, repoRoot, "checkout", "-b", "feature/detached")
	writeGitTestFile(t, repoRoot, "README.md", "feature\n")
	runGitForMindTest(t, repoRoot, "add", "README.md")
	runGitForMindTest(t, repoRoot, "-c", "commit.gpgsign=false", "commit", "-m", "feature")
	runGitForMindTest(t, repoRoot, "checkout", "--detach", "HEAD")

	target := projectGitBranchChangesTarget(repoRoot)
	if !target.Available {
		t.Fatalf("expected detached feature branch changes to be available, got %#v", target)
	}
	if target.CurrentBranch != "feature/detached" {
		t.Fatalf("expected detached HEAD to infer feature branch, got %#v", target)
	}
	if target.BaseBranch != "master" || target.BaseRef != "refs/heads/master" {
		t.Fatalf("expected local master base, got %#v", target)
	}
}

func initMindGitTestRepo(t *testing.T, repoRoot string) {
	t.Helper()

	runGitForMindTest(t, repoRoot, "init")
	runGitForMindTest(t, repoRoot, "branch", "-M", "master")
	runGitForMindTest(t, repoRoot, "config", "user.email", "test@example.com")
	runGitForMindTest(t, repoRoot, "config", "user.name", "Test User")
	writeGitTestFile(t, repoRoot, "README.md", "initial\n")
	runGitForMindTest(t, repoRoot, "add", "README.md")
	runGitForMindTest(t, repoRoot, "-c", "commit.gpgsign=false", "commit", "-m", "initial")
}

func runGitForMindTest(t *testing.T, repoRoot string, args ...string) {
	t.Helper()

	commandArgs := append([]string{"-C", repoRoot}, args...)
	cmd := exec.Command("git", commandArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func writeGitTestFile(t *testing.T, repoRoot string, name string, content string) {
	t.Helper()

	path := filepath.Join(repoRoot, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write %s: %v", name, err)
	}
}
