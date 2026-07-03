package storage

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMigrateProjectPromptSettingsFromAppSettingsDoesNotDeadlock(t *testing.T) {
	dataPath := t.TempDir()
	store, err := NewSQLiteStore(dataPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	now := time.Now().UTC()
	projectID := "project-legacy-prompt-settings"
	if err := store.SaveProject(&Project{
		ID:        projectID,
		Name:      "Legacy Prompt Settings",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	enabled := true
	legacyBlocks, err := json.Marshal([]storedInstructionBlock{
		{Type: "project_agents_md", Value: "AGENTS.md", Enabled: &enabled},
	})
	if err != nil {
		t.Fatalf("marshal legacy blocks: %v", err)
	}
	if err := store.SaveSettings(map[string]string{
		legacyAgentInstructionBlocksSettingKey:                string(legacyBlocks),
		legacyBranchTaskDocDirectorySettingPrefix + projectID: "docs/tasks",
		legacyBranchTaskDocModeSettingPrefix + projectID:      "path",
	}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	type openResult struct {
		store *SQLiteStore
		err   error
	}
	done := make(chan openResult, 1)
	go func() {
		reopened, openErr := NewSQLiteStore(dataPath)
		done <- openResult{store: reopened, err: openErr}
	}()

	var reopened *SQLiteStore
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("reopen NewSQLiteStore: %v", result.err)
		}
		reopened = result.store
	case <-time.After(2 * time.Second):
		t.Fatal("reopen NewSQLiteStore timed out; migration likely deadlocked")
	}
	defer reopened.Close()

	project, err := reopened.GetProject(projectID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got := project.Settings[projectBranchTaskDocDirectorySettingKey]; got != "docs/tasks" {
		t.Fatalf("%s = %q, want %q", projectBranchTaskDocDirectorySettingKey, got, "docs/tasks")
	}
	if got := project.Settings[projectBranchTaskDocModeSettingKey]; got != "path" {
		t.Fatalf("%s = %q, want %q", projectBranchTaskDocModeSettingKey, got, "path")
	}

	blocks := parseStoredInstructionBlocks(project.Settings[projectInstructionBlocksSettingKey])
	if len(blocks) != 1 || blocks[0].Type != "project_agents_md" || blocks[0].Value != "AGENTS.md" {
		t.Fatalf("migrated project instruction blocks = %#v, want project_agents_md AGENTS.md", blocks)
	}
}

func TestMigrateProjectPromptSettingsFromAppSettingsDeletesLegacyBranchTaskDocSettings(t *testing.T) {
	dataPath := t.TempDir()
	store, err := NewSQLiteStore(dataPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	now := time.Now().UTC()
	projectID := "project-legacy-branch-doc-settings"
	if err := store.SaveProject(&Project{
		ID:        projectID,
		Name:      "Legacy Branch Doc Settings",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	if err := store.SaveSettings(map[string]string{
		legacyBranchTaskDocDirectorySettingPrefix + projectID: "docs/tasks",
		legacyBranchTaskDocModeSettingPrefix + projectID:      "path",
		"A2A_REGISTRY_URL": "https://a2gent.net",
	}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := NewSQLiteStore(dataPath)
	if err != nil {
		t.Fatalf("reopen NewSQLiteStore: %v", err)
	}
	defer reopened.Close()

	project, err := reopened.GetProject(projectID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got := project.Settings[projectBranchTaskDocDirectorySettingKey]; got != "docs/tasks" {
		t.Fatalf("migrated branch doc directory = %q, want docs/tasks", got)
	}
	if got := project.Settings[projectBranchTaskDocModeSettingKey]; got != "path" {
		t.Fatalf("migrated branch doc mode = %q, want path", got)
	}

	settings, err := reopened.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	for key := range settings {
		if isProjectBranchTaskDocAppSettingKey(key) {
			t.Fatalf("legacy branch task doc app setting was not deleted: %q", key)
		}
	}
	if got := settings["A2A_REGISTRY_URL"]; got != "https://a2gent.net" {
		t.Fatalf("unrelated app setting = %q, want https://a2gent.net", got)
	}
}
