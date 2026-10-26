package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/storage"
)

func listUnifiedForTest(t *testing.T, server *Server, query string) []UnifiedAgentResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	server.handleListUnifiedAgents(rec, httptest.NewRequest(http.MethodGet, "/unified-agents/"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list failed: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Agents []UnifiedAgentResponse `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Agents
}

func saveLegacyForTest(t *testing.T, store *storage.SQLiteStore, id string, projectID *string) {
	t.Helper()
	now := time.Now()
	if err := store.SaveSubAgent(&storage.SubAgent{ID: id, Name: "Legacy " + id, Provider: "openai", Model: "gpt-5.5", ProjectID: projectID, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("save sub-agent: %v", err)
	}
}

func saveStoredForTest(t *testing.T, store *storage.SQLiteStore, id, yaml string, projectID *string) {
	t.Helper()
	now := time.Now()
	if err := store.SaveAgentDefinition(&storage.AgentDefinitionRecord{ID: id, Name: "Stored " + id, Runtime: agentdef.RuntimeHost, ProjectID: projectID, DefinitionYAML: yaml, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("save definition: %v", err)
	}
}

func TestListUnifiedAgentsStoredBeatsLegacyLikeResolver(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	saveLegacyForTest(t, store, "dup", nil)
	saveStoredForTest(t, store, "dup", "version: \"1\"\nagent:\n  id: dup\n  name: Stored dup\nruntime:\n  type: host\n", nil)

	agents := listUnifiedForTest(t, server, "")
	var got []UnifiedAgentResponse
	for _, a := range agents {
		if a.ID == "dup" {
			got = append(got, a)
		}
	}
	if len(got) != 1 {
		t.Fatalf("want one dup entry, got %+v", got)
	}
	def, _, err := server.definitionForUnifiedAgent("dup", "")
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	if got[0].Runtime != agentdef.RuntimeHost || got[0].Definition == nil || got[0].Definition.Runtime.Type != def.Runtime.Type || got[0].Definition.Agent.Name != def.Agent.Name {
		t.Fatalf("list entry does not match resolver: %+v vs %+v", got[0], def)
	}
}

func TestListUnifiedAgentsHiddenStoredWinnerDoesNotLeakLegacy(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	// Legacy is global (visible in the global list); stored winner is bound to a project.
	saveLegacyForTest(t, store, "dup", nil)
	projectID := "proj-x"
	saveStoredForTest(t, store, "dup", "version: \"1\"\nagent:\n  id: dup\n  name: Stored dup\nruntime:\n  type: host\nworkspace:\n  scope: configured_project\nlocal:\n  project_bindings:\n    configured_project: proj-x\n", &projectID)

	for _, a := range listUnifiedForTest(t, server, "") {
		if a.ID == "dup" {
			t.Fatalf("hidden stored winner leaked legacy entry: %+v", a)
		}
	}
}

func TestListUnifiedAgentsStoredBeatsOnDiskAndHiddenDoesNotLeak(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	now := time.Now()
	root := t.TempDir()
	if err := store.SaveProject(&storage.Project{ID: "proj-app", Name: "App", Folder: &root, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("save project: %v", err)
	}
	dir := filepath.Join(root, "agents", "dup")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("version: \"1\"\nagent:\n  id: dup\n  name: Disk dup\nruntime:\n  type: docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stored host copy bound to the project wins over the on-disk docker one.
	pid := "proj-app"
	saveStoredForTest(t, store, "dup", "version: \"1\"\nagent:\n  id: dup\n  name: Stored dup\nruntime:\n  type: host\nworkspace:\n  scope: configured_project\nlocal:\n  project_bindings:\n    configured_project: proj-app\n", &pid)
	agents := listUnifiedForTest(t, server, "?project_id=proj-app")
	if len(agents) != 1 || agents[0].Runtime != agentdef.RuntimeHost || agents[0].Name != "Stored dup" {
		t.Fatalf("stored should beat on-disk, got %+v", agents)
	}

	// Now the stored winner is global-scoped (hidden in the project view): the
	// on-disk project entry must not leak.
	saveStoredForTest(t, store, "dup", "version: \"1\"\nagent:\n  id: dup\n  name: Stored dup\nruntime:\n  type: host\nworkspace:\n  scope: current_project\n", nil)
	if agents := listUnifiedForTest(t, server, "?project_id=proj-app"); len(agents) != 0 {
		t.Fatalf("hidden stored winner leaked on-disk entry: %+v", agents)
	}
}
