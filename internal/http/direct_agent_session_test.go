package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func setupDiscoveredRebelResearcherAgent(t *testing.T, server *Server, store *storage.SQLiteStore) (*storage.Project, *session.Session) {
	t.Helper()

	now := time.Now()
	projectRoot := t.TempDir()
	project := &storage.Project{ID: "proj-rebel", Name: "Rebel", Folder: &projectRoot, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	agentsDir := filepath.Join(projectRoot, "agents", "rebel-researcher")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("failed to create project agents dir: %v", err)
	}
	yamlDef := `
version: "1"
agent:
  id: rebel-researcher
  name: Rebel Researcher
runtime:
  type: docker
workspace:
  scope: current_project
  mount: rw
`
	if err := os.WriteFile(filepath.Join(agentsDir, "agent.yaml"), []byte(yamlDef), 0o644); err != nil {
		t.Fatalf("failed to write project agent yaml: %v", err)
	}

	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.ProjectID = &project.ID
	sess.Metadata = map[string]interface{}{"unified_agent_id": "rebel-researcher"}
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}
	return project, sess
}

func TestUnifiedAgentDefinitionForSessionUsesDiscoveredProjectAgent(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	_, sess := setupDiscoveredRebelResearcherAgent(t, server, store)

	def, err := server.unifiedAgentDefinitionForSession(sess)
	if err != nil {
		t.Fatalf("unifiedAgentDefinitionForSession failed: %v", err)
	}
	if def.Agent.ID != "rebel-researcher" {
		t.Fatalf("unexpected agent id: %q", def.Agent.ID)
	}
}

func TestCreateSessionAcceptsDiscoveredProjectUnifiedAgent(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	project, _ := setupDiscoveredRebelResearcherAgent(t, server, store)

	body, err := json.Marshal(CreateSessionRequest{
		AgentID:        "build",
		ProjectID:      project.ID,
		UnifiedAgentID: "rebel-researcher",
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.handleCreateSession(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create session status = %d, want %d, body: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
}
