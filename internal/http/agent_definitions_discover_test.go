package http

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestDiscoverAgentDefinitionsInDirectory(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "reviewer")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("failed to create agent dir: %v", err)
	}
	yaml := `version: "1"
agent:
  id: reviewer
  name: Reviewer
runtime:
  type: docker
instructions:
  system: Review code.
`
	if err := os.WriteFile(filepath.Join(agentDir, "agent.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("failed to write agent yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "flat-agent.yaml"), []byte(`
version: "1"
agent:
  id: flat-agent
  name: Flat Agent
runtime:
  type: docker
`), 0o644); err != nil {
		t.Fatalf("failed to write flat yaml: %v", err)
	}

	discovered, warnings := discoverAgentDefinitionsInDirectory(root)
	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
	if len(discovered) != 2 {
		t.Fatalf("discovered = %d, want 2: %+v", len(discovered), discovered)
	}

	byID := make(map[string]discoveredAgentDefinition, len(discovered))
	for _, item := range discovered {
		byID[item.ID] = item
	}
	if byID["reviewer"].DefinitionDir != agentDir {
		t.Fatalf("reviewer definition dir = %q, want %q", byID["reviewer"].DefinitionDir, agentDir)
	}
	if byID["flat-agent"].Name != "Flat Agent" {
		t.Fatalf("flat-agent name = %q", byID["flat-agent"].Name)
	}
}

func TestDiscoverAgentDefinitionsInDirectoryMissingRoot(t *testing.T) {
	discovered, warnings := discoverAgentDefinitionsInDirectory(filepath.Join(t.TempDir(), "missing"))
	if len(discovered) != 0 {
		t.Fatalf("expected no discovered definitions, got %#v", discovered)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want one missing-directory warning", warnings)
	}
}

func TestResolveProjectAgentDefinitionsDirectory(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	folder := filepath.Join(t.TempDir(), "repo")
	project := createTestProject(t, store, "project-1", "Project", folder)
	project.Settings = map[string]string{
		projectAgentDefinitionsDirectorySettingKey: "custom-agents",
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	got := server.resolveProjectAgentDefinitionsDirectory(project)
	want := filepath.Join(folder, "custom-agents")
	if got != want {
		t.Fatalf("resolveProjectAgentDefinitionsDirectory() = %q, want %q", got, want)
	}
}

func createTestProject(t *testing.T, store *storage.SQLiteStore, id, name, folder string) *storage.Project {
	t.Helper()
	project := &storage.Project{
		ID:     id,
		Name:   name,
		Folder: &folder,
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}
	return project
}
