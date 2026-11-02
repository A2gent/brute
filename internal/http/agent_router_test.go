package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/storage"
)

type agentRouterTestClient struct {
	content string
	prompt  string
}

func (c *agentRouterTestClient) Chat(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	c.prompt = req.Messages[0].Content
	return &llm.ChatResponse{Content: c.content}, nil
}

func writeAgentRouterTestAgent(t *testing.T, dir string, id string, description string) {
	t.Helper()
	agentDir := filepath.Join(dir, id)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "version: \"1\"\nagent:\n  id: " + id + "\n  name: " + id + "\n  description: " + description + "\nruntime:\n  type: docker\n"
	if err := os.WriteFile(filepath.Join(agentDir, "agent.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newAgentRouterTestServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("AAGENT_CONFIG_PATH", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("A2GENT_PARENT_PROXY_URL", "")
	server, store := newUnifiedAgentsTestServer(t)
	soul, err := store.GetProject(storage.SystemProjectSoulID)
	if err != nil || soul.Folder == nil {
		t.Fatalf("soul project unavailable: %v", err)
	}
	writeAgentRouterTestAgent(t, filepath.Join(*soul.Folder, "agents"), "global-coder", "writes code")
	projectRoot := t.TempDir()
	now := time.Now()
	if err := store.SaveProject(&storage.Project{ID: "proj-app", Name: "App", Folder: &projectRoot, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	writeAgentRouterTestAgent(t, filepath.Join(projectRoot, "agents"), "project-writer", "writes docs")
	return server
}

func routableAgentIDs(agents []routableAgent) map[string]bool {
	ids := map[string]bool{}
	for _, agent := range agents {
		ids[agent.ID] = agent.Enabled
	}
	return ids
}

func TestRoutableAgentsDefaultEnabledAndProjectScoped(t *testing.T) {
	server := newAgentRouterTestServer(t)

	inProject := routableAgentIDs(server.routableAgents("proj-app", false))
	if !inProject["global-coder"] || !inProject["project-writer"] {
		t.Fatalf("expected global and project agents enabled by default, got %v", inProject)
	}
	if _, ok := routableAgentIDs(server.routableAgents("", false))["project-writer"]; ok {
		t.Fatal("project agent must not be routable outside its project")
	}
	if _, ok := routableAgentIDs(server.routableAgents("", true))["project-writer"]; !ok {
		t.Fatal("settings listing must include project agents")
	}
}

func TestAgentRouterPutGetRoundTrip(t *testing.T) {
	server := newAgentRouterTestServer(t)
	body, _ := json.Marshal(map[string]interface{}{
		"agents": []map[string]interface{}{{"id": "global-coder", "enabled": false}},
	})
	rec := httptest.NewRecorder()
	server.handleUpdateAgentRouter(rec, httptest.NewRequest(http.MethodPut, "/agent-router", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT failed: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.handleGetAgentRouter(rec, httptest.NewRequest(http.MethodGet, "/agent-router", nil))
	var resp agentRouterResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	ids := routableAgentIDs(resp.Agents)
	if ids["global-coder"] || !ids["project-writer"] {
		t.Fatalf("unexpected enabled flags: %v", ids)
	}
	saved, err := os.ReadFile(config.GetConfigPath())
	if err != nil || !strings.Contains(string(saved), `"agent_router"`) {
		t.Fatalf("agent router not persisted: %v", err)
	}
}

func TestAgentRouterRejectsUnknownRouterProvider(t *testing.T) {
	server := newAgentRouterTestServer(t)
	rec := httptest.NewRecorder()
	server.handleUpdateAgentRouter(rec, httptest.NewRequest(http.MethodPut, "/agent-router", strings.NewReader(`{"router_provider":"nope"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestSelectAgentViaLLMPicksIndexedAgent(t *testing.T) {
	client := &agentRouterTestClient{content: `{"index":2,"reason":"docs task"}`}
	candidates := []routableAgent{
		{ID: "global-coder", Name: "Coder", Description: "writes code"},
		{ID: "project-writer", Name: "Writer", Description: "writes docs"},
	}
	id, reason, err := selectAgentViaLLM(context.Background(), client, "", "", "write the README", candidates)
	if err != nil || id != "project-writer" || reason != "docs task" {
		t.Fatalf("unexpected choice id=%q reason=%q err=%v", id, reason, err)
	}
	if !strings.Contains(client.prompt, "writes docs") {
		t.Fatalf("router prompt omitted agent descriptions: %s", client.prompt)
	}
	client.content = `{"index":3}`
	if _, _, err := selectAgentViaLLM(context.Background(), client, "", "", "x", candidates); err == nil {
		t.Fatal("expected out-of-range index to fail")
	}
}

func TestApplyAgentAutoRoute(t *testing.T) {
	server := newAgentRouterTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}

	server.config.AgentRouter.Agents = []config.AgentRouterAgent{{ID: "global-coder", Enabled: false}}
	for _, agent := range server.routableAgents("proj-app", false) {
		if agent.ID != "global-coder" && agent.ID != "project-writer" {
			server.config.AgentRouter.Agents = append(server.config.AgentRouter.Agents, config.AgentRouterAgent{ID: agent.ID, Enabled: false})
		}
	}
	req := CreateSessionRequest{AgentAutoRoute: true, Task: "write docs", ProjectID: "proj-app"}
	server.applyAgentAutoRoute(context.Background(), &req, sess)
	if req.UnifiedAgentID != "project-writer" || sess.Metadata["agent_routed"] != true {
		t.Fatalf("expected single enabled agent to be chosen, got %q", req.UnifiedAgentID)
	}

	server.config.AgentRouter.Agents = append(server.config.AgentRouter.Agents, config.AgentRouterAgent{ID: "project-writer", Enabled: false})
	req = CreateSessionRequest{AgentAutoRoute: true, Task: "write docs", ProjectID: "proj-app"}
	server.applyAgentAutoRoute(context.Background(), &req, sess)
	if req.UnifiedAgentID != "" {
		t.Fatalf("expected default fallback when no agent is enabled, got %q", req.UnifiedAgentID)
	}
}
