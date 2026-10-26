package storage

import (
	"strings"
	"testing"
)

func TestBuiltInSpecificationSubAgentMaintainsBilingualFiles(t *testing.T) {
	for _, expected := range []string{
		"English source of truth",
		".ru.md",
		".ru.html",
		"Prefer .html over .md",
		"Keep both language versions synchronized",
	} {
		if !strings.Contains(builtInSpecificationSubAgentPrompt, expected) {
			t.Fatalf("builtInSpecificationSubAgentPrompt must contain %q", expected)
		}
	}
	if strings.Contains(builtInSpecificationSubAgentPrompt, "single markdown specification file") {
		t.Fatal("builtInSpecificationSubAgentPrompt still restricts output to one markdown file")
	}
}

func TestSeedRepinsDockerBodyFolder(t *testing.T) {
	root := t.TempDir()
	store, err := NewSQLiteStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE projects SET folder = '/data/body' WHERE id = ?`, SystemProjectAgentID); err != nil {
		t.Fatal(err)
	}
	if err := store.seedSystemProjects(); err != nil {
		t.Fatalf("seed must not fail on stale docker path: %v", err)
	}
	p, err := store.GetProject(SystemProjectAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Folder == nil || !strings.HasPrefix(*p.Folder, root) {
		t.Fatalf("body folder not repinned: %v", p.Folder)
	}
}
