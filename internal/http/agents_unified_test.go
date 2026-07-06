package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/go-chi/chi/v5"
)

func newUnifiedAgentsTestServer(t *testing.T) (*Server, *storage.SQLiteStore) {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)
	return server, store
}

func TestExportSubAgentYAML(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)

	now := time.Now()
	sa := &storage.SubAgent{
		ID:                "sa-export",
		Name:              "Exporter",
		Provider:          "openai",
		Model:             "gpt-5.5",
		EnabledTools:      []string{"read", "grep"},
		InstructionBlocks: `[{"type":"text","value":"Be concise."}]`,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := store.SaveSubAgent(sa); err != nil {
		t.Fatalf("failed to save sub-agent: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/sub-agents/sa-export/yaml", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("subAgentID", "sa-export")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rec := httptest.NewRecorder()
	server.handleExportSubAgentYAML(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID      string `json:"id"`
		Runtime string `json:"runtime"`
		YAML    string `json:"yaml"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Runtime != agentdef.RuntimeDocker {
		t.Fatalf("expected docker runtime, got %q", resp.Runtime)
	}

	def, err := agentdef.ParseYAML([]byte(resp.YAML))
	if err != nil {
		t.Fatalf("exported YAML does not parse: %v\n%s", err, resp.YAML)
	}
	if def.Runtime.Type != agentdef.RuntimeDocker {
		t.Fatalf("exported definition should use docker runtime: %+v", def.Runtime)
	}
	if def.Agent.ID != "sa-export" || def.LLM.Model != "gpt-5.5" {
		t.Fatalf("exported definition lost fields: %+v", def)
	}
	if len(def.Tools.Enabled) != 2 {
		t.Fatalf("exported definition lost tools: %+v", def.Tools)
	}
}

func TestProbeLocalDockerAgentHealthCapturesOfflineUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{
			"status":"offline",
			"reason":"anthropic_usage_limit_reached",
			"message":"Claude usage limit reached; this container stays offline until usage resets.",
			"provider_usage":{"provider":"anthropic","status":"available","usage_left_text":"5h 0% left","refreshable":true}
		}`))
	}))
	defer upstream.Close()

	health := probeLocalDockerAgentHealth(context.Background(), upstream.Client(), upstream.URL)
	if health.Healthy {
		t.Fatalf("expected unhealthy health result: %+v", health)
	}
	if health.Status != "offline" || health.Reason != "anthropic_usage_limit_reached" || health.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("unexpected health result: %+v", health)
	}
	if health.ProviderUsage == nil || health.ProviderUsage.Provider != string(config.ProviderAnthropic) {
		t.Fatalf("expected provider usage in health result: %+v", health)
	}
}

func TestConfiguredAgentPromptSkipsUnhealthyRunningContainer(t *testing.T) {
	binding := dockerWorkspaceBinding{}
	containers := map[string][]LocalDockerAgent{
		"reviewer": {{
			Running: true,
			Health: &LocalDockerAgentHealth{
				Status:  "offline",
				Healthy: false,
				Reason:  "anthropic_usage_limit_reached",
			},
		}},
	}
	if configuredAgentPromptHasRunningContainer([]string{"reviewer"}, binding, containers) {
		t.Fatal("unhealthy running container should not be listed as available")
	}

	containers["reviewer"][0].Health = &LocalDockerAgentHealth{Status: "ok", Healthy: true}
	if !configuredAgentPromptHasRunningContainer([]string{"reviewer"}, binding, containers) {
		t.Fatal("healthy running container should be listed as available")
	}
}

func TestApplyUnifiedAgentContainerStatusMarksUnhealthyRunningContainer(t *testing.T) {
	entry := UnifiedAgentResponse{}
	applyUnifiedAgentContainerStatus(&entry, []LocalDockerAgent{{
		Running: true,
		APIURL:  "http://127.0.0.1:18080",
		Health:  &LocalDockerAgentHealth{Status: "offline", Healthy: false},
	}})
	if entry.Status != agentDefinitionStatusUnhealthy {
		t.Fatalf("status = %q, want %q", entry.Status, agentDefinitionStatusUnhealthy)
	}
	if entry.APIURL != "http://127.0.0.1:18080" {
		t.Fatalf("expected APIURL to remain available for diagnostics, got %q", entry.APIURL)
	}
}

func TestLocalDockerCreateRequestBaseFromDefinitionCarriesPublishMetadata(t *testing.T) {
	def := &agentdef.Definition{
		Agent: agentdef.AgentMeta{
			ID:          "dev-code-reviewer",
			Name:        "Code Reviewer",
			Emoji:       "🔍",
			Description: "Reviews code changes for correctness and regressions.",
			IconURL:     "https://example.com/agent-icon.png",
			Kind:        "reviewer",
		},
		Runtime: agentdef.Runtime{Type: agentdef.RuntimeDocker},
		Publish: agentdef.Publish{Square: agentdef.PublishSquare{
			Category:  "engineering",
			AvatarURL: "https://example.com/avatar.png",
		}},
	}

	req := localDockerCreateRequestBaseFromDefinition(def)
	labels := req.Labels
	if labels["a2gent.agent_name"] != "Code Reviewer" || labels["a2gent.agent_description"] != "Reviews code changes for correctness and regressions." {
		t.Fatalf("definition identity metadata was not converted to labels: %#v", labels)
	}
	if labels["a2gent.agent_category"] != "engineering" || labels["a2gent.agent_avatar_url"] != "https://example.com/avatar.png" {
		t.Fatalf("publish metadata was not converted to labels: %#v", labels)
	}
	if labels["a2gent.agent_icon_url"] != "https://example.com/agent-icon.png" {
		t.Fatalf("agent icon metadata was not converted to labels: %#v", labels)
	}
}

func TestImportHostAgentYAMLMigratesToDockerDefinition(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)

	yamlDef := `
version: "1"
agent:
  id: imported-reviewer
  name: Imported Reviewer
runtime:
  type: host
llm:
  provider: openai
  model: gpt-5.5
tools:
  mode: allow
  enabled:
    - read
instructions:
  blocks:
    - type: text
      value: Review carefully.
`
	body, _ := json.Marshal(importAgentYAMLRequest{ConfigYAML: yamlDef})
	req := httptest.NewRequest(http.MethodPost, "/unified-agents/import-yaml", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	server.handleImportAgentYAML(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	record, err := store.GetAgentDefinition("imported-reviewer")
	if err != nil {
		t.Fatalf("imported docker definition not found: %v", err)
	}
	if record.Runtime != agentdef.RuntimeDocker || record.Name != "Imported Reviewer" {
		t.Fatalf("legacy host import should store a docker definition: %+v", record)
	}
	def, err := agentdef.ParseYAML([]byte(record.DefinitionYAML))
	if err != nil {
		t.Fatalf("stored definition YAML invalid: %v", err)
	}
	if def.Runtime.Type != agentdef.RuntimeDocker {
		t.Fatalf("legacy host import should be coerced to docker: %+v", def.Runtime)
	}
	if def.Workspace.Scope != agentdef.WorkspaceScopeCurrentProject || def.Workspace.Mount != agentdef.WorkspaceMountRW {
		t.Fatalf("legacy host import should preserve current-project rw semantics: %+v", def.Workspace)
	}
	if def.LLM.Provider != "openai" || def.LLM.Model != "gpt-5.5" {
		t.Fatalf("imported definition lost LLM fields: %+v", def.LLM)
	}
	if len(def.Tools.Enabled) != 1 || def.Tools.Enabled[0] != "read" {
		t.Fatalf("imported definition lost tools: %+v", def.Tools)
	}
	if !strings.Contains(record.DefinitionYAML, "Review carefully.") {
		t.Fatalf("imported definition lost instruction blocks: %s", record.DefinitionYAML)
	}

	// Re-importing the same definition updates instead of duplicating.
	req = httptest.NewRequest(http.MethodPost, "/unified-agents/import-yaml", strings.NewReader(string(body)))
	rec = httptest.NewRecorder()
	server.handleImportAgentYAML(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on re-import, got %d: %s", rec.Code, rec.Body.String())
	}
	definitions, err := store.ListAgentDefinitions()
	if err != nil {
		t.Fatalf("failed to list agent definitions: %v", err)
	}
	count := 0
	for _, a := range definitions {
		if a.ID == "imported-reviewer" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one imported definition, got %d", count)
	}
}

func TestComposeDockerAgentSystemPromptIncludesInstructionBlocks(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)

	def := &agentdef.Definition{
		Agent:   agentdef.AgentMeta{ID: "docker-instructions", Name: "Docker Instructions"},
		Runtime: agentdef.Runtime{Type: agentdef.RuntimeDocker},
		Instructions: agentdef.Instructions{Blocks: []agentdef.InstructionBlock{
			{Type: "text", Value: "Review carefully."},
		}},
	}

	prompt := server.composeDockerAgentSystemPrompt(def, "")
	if !strings.Contains(prompt, `You are a sub-agent named "Docker Instructions"`) {
		t.Fatalf("prompt missing delegated-agent identity:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Review carefully.") {
		t.Fatalf("prompt missing instruction block:\n%s", prompt)
	}
}

func TestImportAgentYAMLFromFolderStoresDefinitionDir(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	definitionDir := t.TempDir()
	configPath := filepath.Join(definitionDir, "agent.yaml")
	yamlDef := `
version: "1"
agent:
  id: folder-reviewer
  name: Folder Reviewer
runtime:
  type: docker
`
	if err := os.WriteFile(configPath, []byte(yamlDef), 0o644); err != nil {
		t.Fatalf("failed to write agent.yaml: %v", err)
	}

	body, _ := json.Marshal(importAgentYAMLRequest{ConfigPath: definitionDir})
	req := httptest.NewRequest(http.MethodPost, "/unified-agents/import-yaml", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	server.handleImportAgentYAML(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	record, err := store.GetAgentDefinition("folder-reviewer")
	if err != nil {
		t.Fatalf("imported definition not found: %v", err)
	}
	def, err := agentdef.ParseYAML([]byte(record.DefinitionYAML))
	if err != nil {
		t.Fatalf("stored YAML did not parse: %v", err)
	}
	if def.Local.DefinitionDir != definitionDir {
		t.Fatalf("expected definition_dir %q, got %q", definitionDir, def.Local.DefinitionDir)
	}
}

func TestComposeDockerAgentSystemPromptIncludesDefinitionFolderSkills(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	definitionDir := t.TempDir()
	skillsDir := filepath.Join(definitionDir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("failed to create skills dir: %v", err)
	}
	skill := []byte("---\nname: Deep Review\nstrategy: always\n---\nAlways check folder-local invariants.")
	if err := os.WriteFile(filepath.Join(skillsDir, "deep-review.md"), skill, 0o644); err != nil {
		t.Fatalf("failed to write skill: %v", err)
	}
	def := &agentdef.Definition{
		Agent:   agentdef.AgentMeta{ID: "folder-skills", Name: "Folder Skills"},
		Runtime: agentdef.Runtime{Type: agentdef.RuntimeDocker},
		Local:   agentdef.Local{DefinitionDir: definitionDir},
	}

	prompt := server.composeDockerAgentSystemPrompt(def, "")
	if !strings.Contains(prompt, "Connected skills folder: "+skillsDir) || !strings.Contains(prompt, "- Deep Review [deep-review.md]") {
		t.Fatalf("prompt missing definition folder skills listing:\n%s", prompt)
	}
}

func TestComposeDockerAgentSystemPromptUsesContainerWorkspace(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)

	projectRoot := t.TempDir()
	project := &storage.Project{
		ID:        "project-1",
		Name:      "Project One",
		Folder:    &projectRoot,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}
	def := &agentdef.Definition{
		Agent:   agentdef.AgentMeta{ID: "workspace-agent", Name: "Workspace Agent"},
		Runtime: agentdef.Runtime{Type: agentdef.RuntimeDocker},
	}

	prompt := server.composeDockerAgentSystemPrompt(def, project.ID)
	if strings.Contains(prompt, projectRoot) {
		t.Fatalf("prompt should not expose host project path to Docker child:\n%s", prompt)
	}
	if !strings.Contains(prompt, "/workspace") {
		t.Fatalf("prompt should describe the container workspace:\n%s", prompt)
	}
}

func TestDefinitionForUnifiedAgentUsesStoredDefinitionMetadata(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)

	now := time.Now()
	record := &storage.AgentDefinitionRecord{
		ID:      "youtube-transcriber",
		Name:    "YouTube Transcriber",
		Runtime: agentdef.RuntimeDocker,
		DefinitionYAML: `
version: "1"
agent:
  id: youtube-transcriber
  name: YouTube Transcriber
  kind: transcriber
runtime:
  type: docker
llm:
  provider: openai
  model: gpt-5.5
tools:
  mode: allow
  enabled:
    - browser_open
`,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveAgentDefinition(record); err != nil {
		t.Fatalf("failed to save agent definition: %v", err)
	}

	def, err := server.definitionForUnifiedAgent("youtube-transcriber")
	if err != nil {
		t.Fatalf("definitionForUnifiedAgent failed: %v", err)
	}
	if def.Agent.Kind != "transcriber" || def.LLM.Provider != "openai" || def.LLM.Model != "gpt-5.5" {
		t.Fatalf("definition metadata was not preserved: %+v", def)
	}
	if len(def.Tools.Enabled) != 1 || def.Tools.Enabled[0] != "browser_open" {
		t.Fatalf("definition tools were not preserved: %+v", def.Tools)
	}
}

func TestDefinitionForUnifiedAgentUsesSavedSubAgent(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)

	now := time.Now()
	projectID := "project-1"
	sa := &storage.SubAgent{
		ID:                "saved-agent",
		Name:              "Saved Agent",
		ProjectID:         &projectID,
		Provider:          "openai",
		Model:             "gpt-5.5",
		EnabledTools:      []string{"read"},
		InstructionBlocks: "[]",
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := store.SaveSubAgent(sa); err != nil {
		t.Fatalf("failed to save sub-agent: %v", err)
	}

	def, err := server.definitionForUnifiedAgent("saved-agent")
	if err != nil {
		t.Fatalf("definitionForUnifiedAgent failed: %v", err)
	}
	if def.Runtime.Type != agentdef.RuntimeDocker {
		t.Fatalf("saved agents should resolve as docker definitions: %+v", def.Runtime)
	}
	if def.Local.ProjectBindings[agentdef.WorkspaceScopeConfiguredProject] != projectID {
		t.Fatalf("saved agent project binding missing: %+v", def.Local.ProjectBindings)
	}
}

func TestImportDockerAgentYAMLStoresDefinition(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)

	yamlDef := `
version: "1"
agent:
  id: docker-reviewer
  name: Docker Reviewer
runtime:
  type: docker
  image: a2gent-brute:latest
workspace:
  scope: current_project
  mount: ro
llm:
  provider: openai
`
	body, _ := json.Marshal(importAgentYAMLRequest{ConfigYAML: yamlDef})
	req := httptest.NewRequest(http.MethodPost, "/unified-agents/import-yaml", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	server.handleImportAgentYAML(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	record, err := store.GetAgentDefinition("docker-reviewer")
	if err != nil {
		t.Fatalf("stored definition not found: %v", err)
	}
	if record.Runtime != agentdef.RuntimeDocker || record.Name != "Docker Reviewer" {
		t.Fatalf("stored definition lost fields: %+v", record)
	}
	def, err := agentdef.ParseYAML([]byte(record.DefinitionYAML))
	if err != nil {
		t.Fatalf("stored definition YAML invalid: %v", err)
	}
	if def.Runtime.Image != "a2gent-brute:latest" || def.Workspace.Scope != agentdef.WorkspaceScopeCurrentProject {
		t.Fatalf("stored definition content wrong: %+v", def)
	}

	// Re-import updates rather than duplicating.
	req = httptest.NewRequest(http.MethodPost, "/unified-agents/import-yaml", strings.NewReader(string(body)))
	rec = httptest.NewRecorder()
	server.handleImportAgentYAML(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on re-import, got %d: %s", rec.Code, rec.Body.String())
	}

	// Export returns the stored YAML.
	exportReq := httptest.NewRequest(http.MethodGet, "/unified-agents/docker-reviewer/yaml", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("agentDefID", "docker-reviewer")
	exportReq = exportReq.WithContext(context.WithValue(exportReq.Context(), chi.RouteCtxKey, routeCtx))
	exportRec := httptest.NewRecorder()
	server.handleExportAgentDefinitionYAML(exportRec, exportReq)
	if exportRec.Code != http.StatusOK {
		t.Fatalf("expected 200 export, got %d: %s", exportRec.Code, exportRec.Body.String())
	}
	if !strings.Contains(exportRec.Body.String(), "docker-reviewer") {
		t.Fatalf("export missing definition: %s", exportRec.Body.String())
	}

	// Delete removes the stored definition.
	deleteReq := httptest.NewRequest(http.MethodDelete, "/unified-agents/docker-reviewer", nil)
	deleteCtx := chi.NewRouteContext()
	deleteCtx.URLParams.Add("agentDefID", "docker-reviewer")
	deleteReq = deleteReq.WithContext(context.WithValue(deleteReq.Context(), chi.RouteCtxKey, deleteCtx))
	deleteRec := httptest.NewRecorder()
	server.handleDeleteAgentDefinition(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected 200 delete, got %d: %s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, err := store.GetAgentDefinition("docker-reviewer"); err == nil {
		t.Fatal("definition should be deleted")
	}
}

func TestImportDockerAgentYAMLAcceptsAllProjectsScope(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	projectRoot := t.TempDir()
	project := &storage.Project{ID: "proj-app", Name: "App", Folder: &projectRoot, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	yamlDef := `
agent:
  id: broad-agent
runtime:
  type: docker
workspace:
  scope: all_projects
  mount: ro
`
	body, _ := json.Marshal(importAgentYAMLRequest{ConfigYAML: yamlDef})
	req := httptest.NewRequest(http.MethodPost, "/unified-agents/import-yaml", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	server.handleImportAgentYAML(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for all_projects scope, got %d: %s", rec.Code, rec.Body.String())
	}
	record, err := store.GetAgentDefinition("broad-agent")
	if err != nil {
		t.Fatalf("all_projects definition was not stored: %v", err)
	}
	def, err := agentdef.ParseYAML([]byte(record.DefinitionYAML))
	if err != nil {
		t.Fatalf("stored all_projects definition is invalid: %v", err)
	}
	if def.Workspace.Scope != agentdef.WorkspaceScopeAllProjects || def.Workspace.Mount != agentdef.WorkspaceMountRO {
		t.Fatalf("stored workspace scope/mount wrong: %+v", def.Workspace)
	}
}

func TestImportDockerAgentYAMLValidatesSelectedProjectsBinding(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	appRoot := t.TempDir()
	apiRoot := t.TempDir()
	for _, project := range []*storage.Project{
		{ID: "proj-app", Name: "App", Folder: &appRoot, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "proj-api", Name: "API", Folder: &apiRoot, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	} {
		if err := store.SaveProject(project); err != nil {
			t.Fatalf("failed to save project %s: %v", project.ID, err)
		}
	}

	yamlDef := `
agent:
  id: selected-agent
runtime:
  type: docker
workspace:
  scope: selected_projects
  mount: ro
local:
  project_bindings:
    selected_projects: proj-app, proj-api
`
	body, _ := json.Marshal(importAgentYAMLRequest{ConfigYAML: yamlDef})
	req := httptest.NewRequest(http.MethodPost, "/unified-agents/import-yaml", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	server.handleImportAgentYAML(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for selected_projects scope, got %d: %s", rec.Code, rec.Body.String())
	}

	missingBinding := strings.Replace(yamlDef, "    selected_projects: proj-app, proj-api\n", "", 1)
	body, _ = json.Marshal(importAgentYAMLRequest{ConfigYAML: missingBinding})
	req = httptest.NewRequest(http.MethodPost, "/unified-agents/import-yaml", strings.NewReader(string(body)))
	rec = httptest.NewRecorder()
	server.handleImportAgentYAML(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "selected_projects") {
		t.Fatalf("expected selected_projects binding validation error, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestImportDockerAgentDefinitionUpdateRemovesStaleManagedContainers(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	yamlDef := `
agent:
  id: stale-agent
runtime:
  type: docker
workspace:
  scope: current_project
`
	body, _ := json.Marshal(importAgentYAMLRequest{ConfigYAML: yamlDef})
	req := httptest.NewRequest(http.MethodPost, "/unified-agents/import-yaml", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	server.handleImportAgentYAML(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected initial import 201, got %d: %s", rec.Code, rec.Body.String())
	}

	oldRunCommand := runCommand
	var removed []string
	runCommand = func(ctx context.Context, command string, args ...string) (string, error) {
		if command != "docker" {
			return "", nil
		}
		if len(args) > 0 && args[0] == "ps" {
			row := dockerPSRow{
				ID:     "old-container-id",
				Image:  "a2gent-brute:latest",
				State:  "running",
				Status: "Up",
				Names:  "agent-stale-agent__project-old",
				Ports:  "0.0.0.0:18080->8080/tcp",
				Labels: localAgentManagerLabelKey + "=" + localAgentManagerLabelValue + "," + dockerRuntimeAgentDefLabelKey + "=stale-agent",
			}
			encoded, _ := json.Marshal(row)
			return string(encoded), nil
		}
		if len(args) >= 3 && args[0] == "rm" && args[1] == "-f" {
			removed = append(removed, args[2])
			return "", nil
		}
		return "", nil
	}
	t.Cleanup(func() { runCommand = oldRunCommand })

	req = httptest.NewRequest(http.MethodPost, "/unified-agents/import-yaml", strings.NewReader(string(body)))
	rec = httptest.NewRecorder()
	server.handleImportAgentYAML(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected update import 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(removed) != 1 || removed[0] != "old-container-id" {
		t.Fatalf("expected stale container to be removed, got %#v", removed)
	}
	var result importAgentYAMLResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to decode import result: %v", err)
	}
	if len(result.RemovedContainers) != 1 || result.RemovedContainers[0] != "agent-stale-agent__project-old" {
		t.Fatalf("expected removed container name in response, got %#v", result.RemovedContainers)
	}
}

func TestImportRemoteAgentYAMLRejected(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)

	body, _ := json.Marshal(importAgentYAMLRequest{ConfigYAML: "agent:\n  id: ext\nruntime:\n  type: remote\n"})
	req := httptest.NewRequest(http.MethodPost, "/unified-agents/import-yaml", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	server.handleImportAgentYAML(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501 for remote runtime import, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestImportHostAgentYAMLRejectsUnknownProjectBinding(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)

	yamlDef := `
agent:
  id: bound-agent
runtime:
  type: host
workspace:
  scope: configured_project
local:
  project_bindings:
    configured_project: does-not-exist
`
	body, _ := json.Marshal(importAgentYAMLRequest{ConfigYAML: yamlDef})
	req := httptest.NewRequest(http.MethodPost, "/unified-agents/import-yaml", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	server.handleImportAgentYAML(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown project binding, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDelegateToAgentToolRegistration(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)

	if _, ok := server.toolManager.Get("delegate_to_agent"); !ok {
		t.Fatal("delegate_to_agent should be registered on the main tool manager")
	}
	if _, ok := server.toolManager.Get("delegate_to_subagent"); !ok {
		t.Fatal("delegate_to_subagent alias should remain registered")
	}

	now := time.Now()
	sa := &storage.SubAgent{ID: "sa-recursion", Name: "Helper", InstructionBlocks: "[]", CreatedAt: now, UpdatedAt: now}
	if err := store.SaveSubAgent(sa); err != nil {
		t.Fatalf("failed to save sub-agent: %v", err)
	}
	sess, err := server.sessionManager.Create("subagent")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	subMgr := server.buildSubAgentToolManager(sess, nil)
	if _, ok := subMgr.Get("delegate_to_agent"); ok {
		t.Fatal("delegate_to_agent must be removed from sub-agent tool managers to prevent recursion")
	}
	if _, ok := subMgr.Get("delegate_to_subagent"); ok {
		t.Fatal("delegate_to_subagent must be removed from sub-agent tool managers to prevent recursion")
	}
}

func TestDelegateToAgentToolUnknownAgent(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)

	tool := newDelegateToAgentTool(server)
	params, _ := json.Marshal(delegateToAgentParams{AgentID: "definitely-missing-agent", Task: "do something"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for unknown agent")
	}
	if !strings.Contains(result.Error, "not found") {
		t.Fatalf("unexpected error message: %s", result.Error)
	}
}
