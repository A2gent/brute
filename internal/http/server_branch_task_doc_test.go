package http

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBranchTaskDocRelativePathUsesLastBranchSegment(t *testing.T) {
	t.Parallel()

	got, err := branchTaskDocRelativePath("kurapov/AR-1324-some-task")
	if err != nil {
		t.Fatalf("expected branch documentation path, got error: %v", err)
	}
	if got != "AR-1324-some-task.md" {
		t.Fatalf("expected final branch segment as markdown filename, got %q", got)
	}
}

func TestBranchTaskDocRelativePathRejectsUnsafeFinalSegment(t *testing.T) {
	t.Parallel()

	for _, branch := range []string{"", "/", "feature/..", "feature/."} {
		if got, err := branchTaskDocRelativePath(branch); err == nil {
			t.Fatalf("expected error for branch %q, got path %q", branch, got)
		}
	}
}

func TestReadBranchTaskDocumentationPrefersHTML(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		files       map[string]string
		wantPath    string
		wantContent string
		wantMissing bool
	}{
		{"both", map[string]string{"task.md": "markdown", "task.html": "<h1>html</h1>"}, "task.html", "<h1>html</h1>", false},
		{"html only", map[string]string{"task.html": "html"}, "task.html", "html", false},
		{"markdown only", map[string]string{"task.md": "markdown"}, "task.md", "markdown", false},
		{"empty HTML still preferred", map[string]string{"task.md": "markdown", "task.html": ""}, "task.html", "", false},
		{"missing", nil, "task.md", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			path, data, err := readBranchTaskDocumentation(dir, "task.md")
			if tc.wantMissing {
				if !os.IsNotExist(err) {
					t.Fatalf("expected missing file error, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if path != filepath.Join(dir, tc.wantPath) || string(data) != tc.wantContent {
				t.Fatalf("got path %q content %q; want %q %q", path, data, tc.wantPath, tc.wantContent)
			}
		})
	}
}

func TestReadBranchTaskDocumentationDoesNotFallbackOnUnreadableHTML(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "task.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "task.md"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, _, err := readBranchTaskDocumentation(dir, "task.md")
	if err == nil || path != filepath.Join(dir, "task.html") {
		t.Fatalf("expected HTML read error without fallback, got %q %v", path, err)
	}
}

func TestProjectFileMissingReturnsNotFound(t *testing.T) {
	server, projectID, _ := newProjectFileTestServer(t)
	rec := requestProjectFile(t, server, "GET", projectID, "missing.html", nil)
	if rec.Code != 404 {
		t.Fatalf("expected 404 for absent file, got %d: %s", rec.Code, rec.Body.String())
	}
}
