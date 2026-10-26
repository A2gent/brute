package http

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/storage"
)

func TestConfiguredAgentsAvailableOnDemandWithoutDocker(t *testing.T) {
	for _, runtime := range []string{agentdef.RuntimeHost, agentdef.RuntimeDocker} {
		for _, source := range []string{"global-folder", "project-folder", "global-stored", "project-stored"} {
			t.Run(runtime+"/"+source, func(t *testing.T) {
				server, store := newUnifiedAgentsTestServer(t)
				now := time.Now()
				root := t.TempDir()
				projectID := "current-project"
				if err := store.SaveProject(&storage.Project{ID: projectID, Name: "Current", Folder: &root, CreatedAt: now, UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
				yaml := fmt.Sprintf("version: \"1\"\nagent:\n  id: reviewer\n  name: On-demand Reviewer\nruntime:\n  type: %s\nmetrics:\n  speed: 90\n", runtime)
				scoped := strings.HasPrefix(source, "project-")
				if strings.HasSuffix(source, "folder") {
					dir := t.TempDir()
					if scoped {
						dir = filepath.Join(root, "agents")
						if err := os.MkdirAll(dir, 0700); err != nil {
							t.Fatal(err)
						}
					} else {
						settings, _ := store.GetSettings()
						settings[agentDefinitionsFolderSettingKey] = dir
						if err := store.SaveSettings(settings); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.WriteFile(filepath.Join(dir, "reviewer.yaml"), []byte(yaml), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					record := &storage.AgentDefinitionRecord{ID: "reviewer", Name: "On-demand Reviewer", Runtime: runtime, DefinitionYAML: yaml, CreatedAt: now, UpdatedAt: now}
					if scoped {
						record.ProjectID = &projectID
					}
					if err := store.SaveAgentDefinition(record); err != nil {
						t.Fatal(err)
					}
				}
				calls := 0
				oldRunCommand := runCommand
				runCommand = func(context.Context, string, ...string) (string, error) {
					calls++
					return "", fmt.Errorf("Docker is unavailable")
				}
				t.Cleanup(func() { runCommand = oldRunCommand })

				for _, sessionProject := range []string{projectID, ""} {
					sess, err := server.sessionManager.Create("build")
					if err != nil {
						t.Fatal(err)
					}
					if sessionProject != "" {
						sess.ProjectID = &sessionProject
					}
					if err := server.sessionManager.Save(sess); err != nil {
						t.Fatal(err)
					}
					ctx := context.WithValue(t.Context(), "session_id", sess.ID)
					result, err := (&listAgentsTool{server: server}).Execute(ctx, json.RawMessage(`{}`))
					if err != nil || result == nil || !result.Success {
						t.Fatalf("list_agents: %+v %v", result, err)
					}
					section, tokens := server.resolveSubAgentsSection(sess)
					wantVisible := !scoped || sessionProject == projectID
					if got := strings.Contains(result.Output, `"id": "reviewer"`); got != wantVisible {
						t.Fatalf("list visibility for %q = %v, want %v: %s", sessionProject, got, wantVisible, result.Output)
					}
					if got := strings.Contains(section, "- reviewer - On-demand Reviewer"); got != wantVisible {
						t.Fatalf("prompt visibility for %q = %v, want %v: %s", sessionProject, got, wantVisible, section)
					}
					if wantVisible && (tokens == 0 || !strings.Contains(section, "on demand") || !strings.Contains(section, "speed=90")) {
						t.Fatalf("expected on-demand instructions and compact metrics: %s", section)
					}
				}
				if calls != 0 {
					t.Fatalf("discovery must not probe or start runtimes, got %d commands", calls)
				}
			})
		}
	}
}

func TestConfiguredAgentsRefreshRunningOnlySnapshots(t *testing.T) {
	for _, header := range []string{
		"Available Docker-backed configured agents for delegation:",
		"Available Docker-backed configured agents for delegation (saved locally):",
		"Currently running Docker-backed configured agents for delegation:",
		"Running agents for delegate_to_agent:",
	} {
		if !configuredAgentsPromptBlockNeedsRefresh(header + "\n- reviewer - Reviewer") {
			t.Errorf("old catalog must be refreshed: %s", header)
		}
	}
	if configuredAgentsPromptBlockNeedsRefresh(availableConfiguredAgentsPromptHeader) {
		t.Fatal("current catalog must remain cacheable")
	}
}

func TestConfiguredAgentsMigrateEmptyPersistedCatalog(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	old := systemPromptSnapshot{
		BasePrompt: "legacy", CombinedPrompt: "Environment context:\n- old session without running agents",
		Blocks: []systemPromptBlockSnapshot{{Type: "environment_context", Enabled: true, ResolvedContent: "Environment context:\n- old session without running agents"}},
	}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSystemPromptSnapshot(sess.ID, raw); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.SaveAgentDefinition(&storage.AgentDefinitionRecord{
		ID: "sleeping-host", Name: "Sleeping Host", Runtime: agentdef.RuntimeHost,
		DefinitionYAML: "version: \"1\"\nagent:\n  id: sleeping-host\n  name: Sleeping Host\nruntime:\n  type: host\n",
		CreatedAt:      now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	prompt, err := server.buildSystemPromptForSession(sess)
	if err != nil || !strings.Contains(prompt, "- sleeping-host - Sleeping Host") {
		t.Fatalf("old empty catalog must migrate: %s %v", prompt, err)
	}
	// Freeze the new catalog, including empty catalogs, rather than rebuilding
	// the provider's cached system prefix on every subsequent turn.
	if next, err := server.buildSystemPromptForSession(sess); err != nil || next != prompt {
		t.Fatalf("migrated snapshot must remain stable: %s %v", next, err)
	}
}
