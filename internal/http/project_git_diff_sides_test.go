package http

import (
	"os/exec"
	"strings"
	"testing"
)

func TestParseGitDiffPaths(t *testing.T) {
	t.Parallel()

	oldPath, newPath := parseGitDiffPaths("diff --git a/src/app.ts b/src/app.ts\n@@ -1 +1 @@\n")
	if oldPath != "src/app.ts" || newPath != "src/app.ts" {
		t.Fatalf("plain paths = %q %q", oldPath, newPath)
	}

	oldPath, newPath = parseGitDiffPaths(`diff --git "a/docs/file with spaces.md" "b/docs/file with spaces.md"`)
	if oldPath != "docs/file with spaces.md" || newPath != "docs/file with spaces.md" {
		t.Fatalf("quoted paths = %q %q", oldPath, newPath)
	}

	oldPath, newPath = parseGitDiffPaths("diff --git a/old.ts b/new.ts\nrename from old.ts\n")
	if oldPath != "old.ts" || newPath != "new.ts" {
		t.Fatalf("rename paths = %q %q", oldPath, newPath)
	}
}

func TestGitBlobAtRefRejectsBinaryAndOversizedSides(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	repoRoot := t.TempDir()
	initMindGitTestRepo(t, repoRoot)
	writeGitTestFile(t, repoRoot, "app.bin", "ok\x00nope")
	runGitForMindTest(t, repoRoot, "add", "--", "app.bin")
	runGitForMindTest(t, repoRoot, "-c", "commit.gpgsign=false", "commit", "-m", "binary")

	if _, ok := gitBlobAtRef(repoRoot, "HEAD", "app.bin"); ok {
		t.Fatal("expected binary blob to be skipped")
	}
}

func TestLoadGitDiffFileSidesUsesFallbackPath(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	repoRoot := t.TempDir()
	initMindGitTestRepo(t, repoRoot)
	content := "alpha\nbeta\n"
	writeGitTestFile(t, repoRoot, "plain.ts", content)
	runGitForMindTest(t, repoRoot, "add", "--", "plain.ts")
	runGitForMindTest(t, repoRoot, "-c", "commit.gpgsign=false", "commit", "-m", "plain")

	oldContent, newContent := loadGitDiffFileSides(repoRoot, "HEAD", "not a diff", "plain.ts")
	if oldContent == nil || newContent == nil {
		t.Fatal("expected fallback path sides")
	}
	if *oldContent != content || *newContent != content {
		t.Fatalf("sides = %#v %#v", oldContent, newContent)
	}
	if !strings.HasSuffix(*newContent, "\n") {
		t.Fatal("expected exact blob including trailing newline")
	}
}

func TestLoadGitDiffFileSidesSkipsAddedFiles(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}

	repoRoot := t.TempDir()
	initMindGitTestRepo(t, repoRoot)
	runGitForMindTest(t, repoRoot, "checkout", "-b", "add-file")
	writeGitTestFile(t, repoRoot, "added.ts", "only on the branch\n")
	runGitForMindTest(t, repoRoot, "add", "--", "added.ts")
	runGitForMindTest(t, repoRoot, "-c", "commit.gpgsign=false", "commit", "-m", "add file")

	oldContent, newContent := loadGitDiffFileSides(repoRoot, "master", "diff --git a/added.ts b/added.ts\nnew file mode 100644\n", "added.ts")
	if oldContent != nil || newContent != nil {
		t.Fatalf("added file sides = %#v %#v", oldContent, newContent)
	}
}
