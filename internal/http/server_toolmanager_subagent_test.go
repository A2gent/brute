package http

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func healthyDockerAgentPortForTest(t *testing.T) int {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(server.Close)

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("failed to parse test health server URL: %v", err)
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("failed to parse test health server host %q: %v", parsed.Host, err)
	}
	return mustAtoiForTest(t, port)
}

func mustAtoiForTest(t *testing.T, raw string) int {
	t.Helper()
	value, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("failed to parse integer %q: %v", raw, err)
	}
	return value
}

func TestToolManagerForSession_SubAgentIgnoresGlobalDisabledTools(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	if err := store.SaveSettings(map[string]string{
		disabledToolsSettingKey: `["glob","find_files","read"]`,
	}); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	now := time.Now()
	subAgent := &storage.SubAgent{
		ID:                "sa-file-editor",
		Name:              "File editor",
		Provider:          "",
		Model:             "",
		EnabledTools:      []string{}, // empty means all tools
		InstructionBlocks: "[]",
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := store.SaveSubAgent(subAgent); err != nil {
		t.Fatalf("failed to save sub-agent: %v", err)
	}

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	normalSess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create normal session: %v", err)
	}
	normalMgr := server.toolManagerForSession(normalSess)
	if _, ok := normalMgr.Get("glob"); ok {
		t.Fatalf("expected glob to be disabled for normal session")
	}

	subSess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create sub-agent session: %v", err)
	}
	if subSess.Metadata == nil {
		subSess.Metadata = map[string]interface{}{}
	}
	subSess.Metadata["sub_agent_id"] = subAgent.ID
	if err := sessionManager.Save(subSess); err != nil {
		t.Fatalf("failed to save sub-agent session: %v", err)
	}

	subMgr := server.toolManagerForSession(subSess)
	if _, ok := subMgr.Get("glob"); !ok {
		t.Fatalf("expected glob to be available for sub-agent session")
	}
	if _, ok := subMgr.Get("find_files"); !ok {
		t.Fatalf("expected find_files to be available for sub-agent session")
	}
	if _, ok := subMgr.Get("read"); !ok {
		t.Fatalf("expected read to be available for sub-agent session")
	}
}

func TestServerToolManager_RegistersTavilyAndPerplexitySearchTools(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	if _, ok := server.toolManager.Get("tavily_search"); !ok {
		t.Fatalf("expected tavily_search to be registered in the server tool manager")
	}
	if _, ok := server.toolManager.Get("perplexity_search"); !ok {
		t.Fatalf("expected perplexity_search to be registered in the server tool manager")
	}
}

func TestBuildSubAgentToolManager_IncludesIntegrationToolsForProjectWorkDir(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	projectRoot := t.TempDir()
	project := &storage.Project{
		ID:        "project-youtube-subagent",
		Name:      "YouTube Subagent Project",
		Folder:    &projectRoot,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	subSess, err := sessionManager.Create("subagent")
	if err != nil {
		t.Fatalf("failed to create sub-agent session: %v", err)
	}
	subSess.ProjectID = &project.ID

	mgr := server.buildSubAgentToolManager(subSess, []string{"youtube_transcript", "tavily_search", "perplexity_search"})
	if _, ok := mgr.Get("youtube_transcript"); !ok {
		t.Fatalf("expected youtube_transcript to be available for project-scoped sub-agent")
	}
	if _, ok := mgr.Get("tavily_search"); !ok {
		t.Fatalf("expected tavily_search to be available when explicitly enabled")
	}
	if _, ok := mgr.Get("perplexity_search"); !ok {
		t.Fatalf("expected perplexity_search to be available when explicitly enabled")
	}
	if _, ok := mgr.Get("exa_search"); ok {
		t.Fatalf("did not expect unrelated integration tools to bypass sub-agent allow list")
	}

	// WHY: the failure reported in the UI was a missing tool, not YouTube network
	// behavior. Execute with an invalid URL to prove the registered integration
	// tool itself receives the call instead of Manager returning "tool not found".
	result, err := mgr.Execute(t.Context(), "youtube_transcript", json.RawMessage(`{"url":"not a youtube url"}`))
	if err != nil {
		t.Fatalf("expected registered youtube_transcript tool, got manager error: %v", err)
	}
	if result == nil || result.Success || result.Error != "invalid youtube url" {
		t.Fatalf("expected youtube_transcript validation error, got %#v", result)
	}
}

func TestBuildSystemPromptForSession_UsesSubAgentInstructions(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	now := time.Now()
	subAgent := &storage.SubAgent{
		ID:       "sa-planner",
		Name:     "Planner",
		Provider: "",
		Model:    "",
		EnabledTools: []string{
			"read",
		},
		InstructionBlocks: `[{"type":"text","value":"You are planner only. Never edit files.","enabled":true}]`,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := store.SaveSubAgent(subAgent); err != nil {
		t.Fatalf("failed to save sub-agent: %v", err)
	}

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	projectRoot := t.TempDir()
	project := &storage.Project{
		ID:        "project-app",
		Name:      "App Project",
		Folder:    &projectRoot,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	subSess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create sub-agent session: %v", err)
	}
	subSess.ProjectID = &project.ID
	if subSess.Metadata == nil {
		subSess.Metadata = map[string]interface{}{}
	}
	subSess.Metadata["sub_agent_id"] = subAgent.ID
	if err := sessionManager.Save(subSess); err != nil {
		t.Fatalf("failed to save sub-agent session: %v", err)
	}

	systemPrompt := server.buildSystemPromptForSession(subSess)
	if !strings.Contains(systemPrompt, `You are a sub-agent named "Planner".`) {
		t.Fatalf("expected sub-agent identity prompt, got: %q", systemPrompt)
	}
	if !strings.Contains(systemPrompt, "You are planner only. Never edit files.") {
		t.Fatalf("expected sub-agent instruction block in prompt, got: %q", systemPrompt)
	}
	if !strings.Contains(systemPrompt, "Environment context:") {
		t.Fatalf("expected sub-agent prompt to include environment context, got: %q", systemPrompt)
	}
	if !strings.Contains(systemPrompt, "Project root: "+projectRoot) {
		t.Fatalf("expected sub-agent prompt to include project root %q, got: %q", projectRoot, systemPrompt)
	}
	if !strings.Contains(systemPrompt, "Operating system:") || !strings.Contains(systemPrompt, "Current time:") {
		t.Fatalf("expected sub-agent prompt to include OS and current time, got: %q", systemPrompt)
	}
	if strings.Contains(systemPrompt, availableConfiguredAgentsPromptHeader) {
		t.Fatalf("expected sub-agent prompt to omit main-agent configured-agent listing")
	}
}

func TestBuildSystemPromptForSession_IncludesEnvironmentContextForMainAgent(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	now := time.Now()
	projectRoot := t.TempDir()
	project := &storage.Project{
		ID:        "project-main",
		Name:      "Main Project",
		Folder:    &projectRoot,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	sess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.ProjectID = &project.ID
	if err := sessionManager.Save(sess); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	systemPrompt := server.buildSystemPromptForSession(sess)
	if !strings.Contains(systemPrompt, "Environment context:") {
		t.Fatalf("expected main prompt to include environment context, got: %q", systemPrompt)
	}
	if !strings.Contains(systemPrompt, "Project root: "+projectRoot) {
		t.Fatalf("expected main prompt to include project root %q, got: %q", projectRoot, systemPrompt)
	}
}

func TestBuildSystemPromptForSession_IncludesStoredAgentDefinitions(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	now := time.Now()
	definitionYAML := `
version: "1"
agent:
  id: youtube-transcriber-gemini
  name: YouTube Transcriber (Gemini)
runtime:
  type: docker
llm:
  provider: google
  model: models/gemini-3.1-pro-preview
tools:
  mode: allow
  enabled:
    - youtube_transcript
workspace:
  scope: none
`
	if err := store.SaveAgentDefinition(&storage.AgentDefinitionRecord{
		ID:             "youtube-transcriber-gemini",
		Name:           "YouTube Transcriber (Gemini)",
		Runtime:        agentdef.RuntimeDocker,
		DefinitionYAML: definitionYAML,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("failed to save agent definition: %v", err)
	}
	stoppedDefinitionYAML := strings.Replace(definitionYAML, "youtube-transcriber-gemini", "stopped-agent", 1)
	stoppedDefinitionYAML = strings.Replace(stoppedDefinitionYAML, "YouTube Transcriber (Gemini)", "Stopped Agent", 1)
	if err := store.SaveAgentDefinition(&storage.AgentDefinitionRecord{
		ID:             "stopped-agent",
		Name:           "Stopped Agent",
		Runtime:        agentdef.RuntimeDocker,
		DefinitionYAML: stoppedDefinitionYAML,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("failed to save stopped agent definition: %v", err)
	}
	uncreatedDefinitionYAML := strings.Replace(definitionYAML, "youtube-transcriber-gemini", "uncreated-agent", 1)
	uncreatedDefinitionYAML = strings.Replace(uncreatedDefinitionYAML, "YouTube Transcriber (Gemini)", "Uncreated Agent", 1)
	if err := store.SaveAgentDefinition(&storage.AgentDefinitionRecord{
		ID:             "uncreated-agent",
		Name:           "Uncreated Agent",
		Runtime:        agentdef.RuntimeDocker,
		DefinitionYAML: uncreatedDefinitionYAML,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("failed to save uncreated agent definition: %v", err)
	}

	runningAgentHealthPort := healthyDockerAgentPortForTest(t)
	oldRunCommand := runCommand
	runCommand = func(ctx context.Context, command string, args ...string) (string, error) {
		if command != "docker" || len(args) == 0 || args[0] != "ps" {
			return "", nil
		}
		rows := []dockerPSRow{
			{
				ID:     "running-agent-id",
				Image:  "a2gent-brute:latest",
				State:  "running",
				Status: "Up",
				Names:  "agent-youtube-transcriber-gemini",
				Ports:  "0.0.0.0:" + strconv.Itoa(runningAgentHealthPort) + "->8080/tcp",
				Labels: localAgentManagerLabelKey + "=" + localAgentManagerLabelValue + "," + dockerRuntimeManagedLabelKey + "=true," + dockerRuntimeAgentDefLabelKey + "=youtube-transcriber-gemini",
			},
			{
				ID:     "stopped-agent-id",
				Image:  "a2gent-brute:latest",
				State:  "exited",
				Status: "Exited",
				Names:  "agent-stopped-agent",
				Ports:  "0.0.0.0:18081->8080/tcp",
				Labels: localAgentManagerLabelKey + "=" + localAgentManagerLabelValue + "," + dockerRuntimeManagedLabelKey + "=true," + dockerRuntimeAgentDefLabelKey + "=stopped-agent",
			},
		}
		encoded := make([]string, 0, len(rows))
		for _, row := range rows {
			raw, _ := json.Marshal(row)
			encoded = append(encoded, string(raw))
		}
		return strings.Join(encoded, "\n"), nil
	}
	t.Cleanup(func() { runCommand = oldRunCommand })

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)
	sess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	systemPrompt := server.buildSystemPromptForSession(sess)
	subAgentsSectionStart := strings.Index(systemPrompt, availableConfiguredAgentsPromptHeader)
	if subAgentsSectionStart == -1 {
		t.Fatalf("expected configured agent listing, got: %q", systemPrompt)
	}
	subAgentsSection := systemPrompt[subAgentsSectionStart:]
	if !strings.Contains(subAgentsSection, "- youtube-transcriber-gemini — YouTube Transcriber (Gemini)") {
		t.Fatalf("expected running stored YAML agent in compact prompt, got: %q", systemPrompt)
	}
	if strings.Contains(subAgentsSection, "stopped-agent") || strings.Contains(subAgentsSection, "Uncreated Agent") {
		t.Fatalf("expected only running agents in prompt, got: %q", systemPrompt)
	}
	if strings.Contains(subAgentsSection, "Status:") || strings.Contains(subAgentsSection, "Provider:") || strings.Contains(subAgentsSection, "Model:") || strings.Contains(subAgentsSection, "Tools:") {
		t.Fatalf("expected compact ID/name-only agent metadata in prompt, got: %q", systemPrompt)
	}
}

func TestBuildSystemPromptForSession_IncludesAvailableSavedSubAgents(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	now := time.Now()
	currentProjectID := "project-current"
	otherProjectID := "project-other"
	subAgents := []*storage.SubAgent{
		{
			ID:                "running-reviewer",
			Name:              "Running Reviewer",
			Provider:          "openai",
			Model:             "gpt-5.5",
			EnabledTools:      []string{"read", "grep"},
			InstructionBlocks: "[]",
			CreatedAt:         now,
			UpdatedAt:         now,
		},
		{
			ID:                "stopped-reviewer",
			Name:              "Stopped Reviewer",
			Provider:          "openai",
			Model:             "gpt-5.5",
			EnabledTools:      []string{"read"},
			InstructionBlocks: "[]",
			CreatedAt:         now,
			UpdatedAt:         now,
		},
		{
			ID:                "wrong-project-reviewer",
			Name:              "Wrong Project Reviewer",
			Provider:          "openai",
			Model:             "gpt-5.5",
			EnabledTools:      []string{"read"},
			InstructionBlocks: "[]",
			CreatedAt:         now,
			UpdatedAt:         now,
		},
	}
	for _, subAgent := range subAgents {
		if err := store.SaveSubAgent(subAgent); err != nil {
			t.Fatalf("failed to save sub-agent %s: %v", subAgent.ID, err)
		}
	}
	currentProjectRoot := t.TempDir()
	currentProject := &storage.Project{ID: currentProjectID, Name: "Current Project", Folder: &currentProjectRoot, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveProject(currentProject); err != nil {
		t.Fatalf("failed to save current project: %v", err)
	}
	otherProjectRoot := t.TempDir()
	otherProject := &storage.Project{ID: otherProjectID, Name: "Other Project", Folder: &otherProjectRoot, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveProject(otherProject); err != nil {
		t.Fatalf("failed to save other project: %v", err)
	}

	runningReviewerHealthPort := healthyDockerAgentPortForTest(t)
	wrongProjectReviewerHealthPort := healthyDockerAgentPortForTest(t)
	oldRunCommand := runCommand
	runCommand = func(ctx context.Context, command string, args ...string) (string, error) {
		if command != "docker" || len(args) == 0 || args[0] != "ps" {
			return "", nil
		}
		rows := []dockerPSRow{
			{
				ID:     "running-reviewer-id",
				Image:  "a2gent-brute:latest",
				State:  "running",
				Status: "Up",
				Names:  "agent-running-reviewer__project-project-current",
				Ports:  "0.0.0.0:" + strconv.Itoa(runningReviewerHealthPort) + "->8080/tcp",
				Labels: localAgentManagerLabelKey + "=" + localAgentManagerLabelValue + "," + dockerRuntimeManagedLabelKey + "=true," + dockerRuntimeAgentDefLabelKey + "=running-reviewer,a2gent.project_id=project-current",
			},
			{
				ID:     "stopped-reviewer-id",
				Image:  "a2gent-brute:latest",
				State:  "exited",
				Status: "Exited",
				Names:  "agent-stopped-reviewer__project-project-current",
				Ports:  "0.0.0.0:18081->8080/tcp",
				Labels: localAgentManagerLabelKey + "=" + localAgentManagerLabelValue + "," + dockerRuntimeManagedLabelKey + "=true," + dockerRuntimeAgentDefLabelKey + "=stopped-reviewer,a2gent.project_id=project-current",
			},
			{
				ID:     "wrong-project-reviewer-id",
				Image:  "a2gent-brute:latest",
				State:  "running",
				Status: "Up",
				Names:  "agent-wrong-project-reviewer__project-project-other",
				Ports:  "0.0.0.0:" + strconv.Itoa(wrongProjectReviewerHealthPort) + "->8080/tcp",
				Labels: localAgentManagerLabelKey + "=" + localAgentManagerLabelValue + "," + dockerRuntimeManagedLabelKey + "=true," + dockerRuntimeAgentDefLabelKey + "=wrong-project-reviewer,a2gent.project_id=project-other",
			},
		}
		encoded := make([]string, 0, len(rows))
		for _, row := range rows {
			raw, _ := json.Marshal(row)
			encoded = append(encoded, string(raw))
		}
		return strings.Join(encoded, "\n"), nil
	}
	t.Cleanup(func() { runCommand = oldRunCommand })

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)
	sess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.ProjectID = &currentProjectID
	systemPrompt := server.buildSystemPromptForSession(sess)
	subAgentsSectionStart := strings.Index(systemPrompt, availableConfiguredAgentsPromptHeader)
	if subAgentsSectionStart == -1 {
		t.Fatalf("expected configured agent listing, got: %q", systemPrompt)
	}
	subAgentsSection := systemPrompt[subAgentsSectionStart:]
	if !strings.Contains(subAgentsSection, "- running-reviewer — Running Reviewer") {
		t.Fatalf("expected running saved sub-agent in compact prompt, got: %q", systemPrompt)
	}
	if strings.Contains(subAgentsSection, "stopped-reviewer") || strings.Contains(subAgentsSection, "Stopped Reviewer") {
		t.Fatalf("expected stopped saved sub-agent to be omitted from prompt, got: %q", systemPrompt)
	}
	if strings.Contains(subAgentsSection, "wrong-project-reviewer") || strings.Contains(subAgentsSection, "Wrong Project Reviewer") {
		t.Fatalf("expected other-project saved sub-agent to be omitted from prompt, got: %q", systemPrompt)
	}
	if strings.Contains(subAgentsSection, "Status:") || strings.Contains(subAgentsSection, "Provider:") || strings.Contains(subAgentsSection, "Model:") || strings.Contains(subAgentsSection, "Tools:") {
		t.Fatalf("expected compact ID/name-only agent metadata in prompt, got: %q", systemPrompt)
	}
}

func TestBuildSystemPromptForSession_RebuildsOutdatedConfiguredAgentSnapshot(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	oldRunCommand := runCommand
	runCommand = func(ctx context.Context, command string, args ...string) (string, error) {
		if command == "docker" && len(args) > 0 && args[0] == "ps" {
			return "", nil
		}
		return "", nil
	}
	t.Cleanup(func() { runCommand = oldRunCommand })

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)
	sess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	sess.Metadata[sessionSystemPromptSnapshotMetadataKey] = systemPromptSnapshot{
		BasePrompt:     "base",
		CombinedPrompt: "Environment context:\n- old\n\n" + legacySavedConfiguredAgentsPromptHeader + "\n- ID: stopped-reviewer | Name: Stopped Reviewer | Status: stopped | Provider: openai | Model: gpt-5.5 | Tools: 1 tools",
		Blocks: []systemPromptBlockSnapshot{
			{Type: "environment_context", Enabled: true, ResolvedContent: "Environment context:\n- old"},
			{Type: "sub_agents", Enabled: true, ResolvedContent: legacySavedConfiguredAgentsPromptHeader + "\n- ID: stopped-reviewer | Name: Stopped Reviewer | Status: stopped | Provider: openai | Model: gpt-5.5 | Tools: 1 tools"},
		},
	}

	systemPrompt := server.buildSystemPromptForSession(sess)
	if strings.Contains(systemPrompt, legacySavedConfiguredAgentsPromptHeader) || strings.Contains(systemPrompt, "stopped-reviewer") || strings.Contains(systemPrompt, "Provider:") {
		t.Fatalf("outdated configured-agent snapshot should have been rebuilt, got: %q", systemPrompt)
	}
}

func TestBuildSystemPromptForSession_IncludesProjectInstructionBlocks(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	now := time.Now()
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "AGENTS.md"), []byte("Project agent rules."), 0o644); err != nil {
		t.Fatalf("failed to write AGENTS.md: %v", err)
	}
	projectBlocks := `[{"type":"project_agents_md","value":"","enabled":true},{"type":"text","value":"Project-only text rule.","enabled":true}]`
	project := &storage.Project{
		ID:     "project-instructions",
		Name:   "Project Instructions",
		Folder: &projectRoot,
		Settings: map[string]string{
			projectInstructionBlocksSettingKey: projectBlocks,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	sess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.ProjectID = &project.ID
	if err := sessionManager.Save(sess); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	systemPrompt := server.buildSystemPromptForSession(sess)
	if !strings.Contains(systemPrompt, "Project agent rules.") {
		t.Fatalf("expected project AGENTS.md content in prompt, got: %q", systemPrompt)
	}
	if !strings.Contains(systemPrompt, "Project-only text rule.") {
		t.Fatalf("expected project text block in prompt, got: %q", systemPrompt)
	}
}

func TestBuildSystemPromptForSession_UsesProjectBranchTaskDocSettings(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	now := time.Now()
	projectRoot := t.TempDir()
	docsDir := filepath.Join(projectRoot, "docs", "tasks")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatalf("failed to create docs dir: %v", err)
	}
	if _, err := runGitCommand(projectRoot, "init"); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}
	if _, err := runGitCommand(projectRoot, "checkout", "-b", "kurapov/PROJECT-123-custom-prompt"); err != nil {
		t.Fatalf("failed to create branch: %v", err)
	}
	branchDocPath := filepath.Join(docsDir, "PROJECT-123-custom-prompt.md")
	if err := os.WriteFile(branchDocPath, []byte("Branch-specific project instructions."), 0o644); err != nil {
		t.Fatalf("failed to write branch doc: %v", err)
	}

	projectBlocks := `[{"type":"branch_task_doc","value":"","enabled":true}]`
	project := &storage.Project{
		ID:     "project-branch-doc-settings",
		Name:   "Project Branch Docs",
		Folder: &projectRoot,
		Settings: map[string]string{
			projectInstructionBlocksSettingKey:      projectBlocks,
			projectBranchTaskDocDirectorySettingKey: docsDir,
			projectBranchTaskDocModeSettingKey:      "content",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	sess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.ProjectID = &project.ID
	if err := sessionManager.Save(sess); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	systemPrompt := server.buildSystemPromptForSession(sess)
	if !strings.Contains(systemPrompt, "Branch-specific project instructions.") {
		t.Fatalf("expected branch task documentation from project settings in prompt, got: %q", systemPrompt)
	}
	if strings.Contains(systemPrompt, "Branch task documentation directory is not configured") {
		t.Fatalf("did not expect legacy global settings error, got: %q", systemPrompt)
	}
}

func TestServerRegistersCreateLocalDockerAgentsTools(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	if _, ok := server.toolManager.Get("create_local_docker_agents_bulk"); !ok {
		t.Fatalf("expected create_local_docker_agents_bulk to be registered")
	}
	if _, ok := server.toolManager.Get("create_local_docker_agents_from_yaml"); !ok {
		t.Fatalf("expected create_local_docker_agents_from_yaml to be registered")
	}
	if _, ok := server.toolManager.Get("import_agent_definition_yaml"); !ok {
		t.Fatalf("expected import_agent_definition_yaml to be registered")
	}
}

func TestBootstrapDisabledToolsByDefault(t *testing.T) {
	t.Setenv(disableToolsByDefaultSettingKey, "true")
	t.Setenv(disabledToolsSettingKey, "")

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	settings, err := store.GetSettings()
	if err != nil {
		t.Fatalf("failed to load settings: %v", err)
	}

	if strings.TrimSpace(settings[disableToolsByDefaultAppliedSettingKey]) == "" {
		t.Fatalf("expected bootstrap applied marker to be set")
	}

	rawDisabled := settings[disabledToolsSettingKey]
	if strings.TrimSpace(rawDisabled) == "" {
		t.Fatalf("expected disabled tools setting to be initialized")
	}

	var disabled []string
	if err := json.Unmarshal([]byte(rawDisabled), &disabled); err != nil {
		t.Fatalf("failed to parse disabled tools: %v", err)
	}

	if len(disabled) != len(server.toolManager.GetDefinitions()) {
		t.Fatalf("expected %d disabled tools, got %d", len(server.toolManager.GetDefinitions()), len(disabled))
	}
}

func TestBootstrapDisabledToolsByDefault_UsesEnvDisabledToolsPolicy(t *testing.T) {
	t.Setenv(disableToolsByDefaultSettingKey, "true")
	t.Setenv(disabledToolsSettingKey, `["delegate_to_agent","suggest_session"]`)

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	_ = NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	settings, err := store.GetSettings()
	if err != nil {
		t.Fatalf("failed to load settings: %v", err)
	}
	if got := strings.TrimSpace(settings[disabledToolsSettingKey]); got != `["delegate_to_agent","suggest_session"]` {
		t.Fatalf("expected disabled tools policy from env, got %q", got)
	}
}

func TestBootstrapDisabledToolsByDefault_RepairsStaleAllDisabledPolicyFromEnv(t *testing.T) {
	t.Setenv(disableToolsByDefaultSettingKey, "true")
	t.Setenv(disabledToolsSettingKey, "")

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	first := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)
	settings, err := store.GetSettings()
	if err != nil {
		t.Fatalf("failed to load settings: %v", err)
	}
	if !first.disabledToolsSettingDisablesAllTools(settings[disabledToolsSettingKey]) {
		t.Fatalf("test setup expected stale all-tools disabled policy, got %q", settings[disabledToolsSettingKey])
	}

	t.Setenv(disabledToolsSettingKey, `["delegate_to_agent","suggest_session"]`)
	_ = NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	after, err := store.GetSettings()
	if err != nil {
		t.Fatalf("failed to load repaired settings: %v", err)
	}
	if got := strings.TrimSpace(after[disabledToolsSettingKey]); got != `["delegate_to_agent","suggest_session"]` {
		t.Fatalf("expected stale all-disabled policy to be repaired from env, got %q", got)
	}
}

func TestBootstrapDisabledToolsByDefault_SyncsExplicitEnvPolicyAfterMarker(t *testing.T) {
	t.Setenv(syncDisabledToolsFromEnvSettingKey, "true")
	t.Setenv(disableToolsByDefaultSettingKey, "true")
	t.Setenv(disabledToolsSettingKey, `["delegate_to_agent","suggest_session"]`)

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	if err := store.SaveSettings(map[string]string{
		disabledToolsSettingKey:                `["read","grep","find_files","bash"]`,
		disableToolsByDefaultAppliedSettingKey: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("failed to seed stale settings: %v", err)
	}

	sessionManager := session.NewManager(store)
	_ = NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	after, err := store.GetSettings()
	if err != nil {
		t.Fatalf("failed to load synced settings: %v", err)
	}
	if got := strings.TrimSpace(after[disabledToolsSettingKey]); got != `["delegate_to_agent","suggest_session"]` {
		t.Fatalf("expected explicit env disabled-tools policy to replace stale policy, got %q", got)
	}
}

func TestBootstrapDisabledToolsByDefault_SyncsExplicitEmptyEnvPolicyAfterMarker(t *testing.T) {
	t.Setenv(syncDisabledToolsFromEnvSettingKey, "true")
	t.Setenv(disableToolsByDefaultSettingKey, "false")
	t.Setenv(disabledToolsSettingKey, "")

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	if err := store.SaveSettings(map[string]string{
		disabledToolsSettingKey:                `["read","grep","find_files","bash"]`,
		disableToolsByDefaultAppliedSettingKey: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("failed to seed stale settings: %v", err)
	}

	sessionManager := session.NewManager(store)
	_ = NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	after, err := store.GetSettings()
	if err != nil {
		t.Fatalf("failed to load synced settings: %v", err)
	}
	if got := strings.TrimSpace(after[disabledToolsSettingKey]); got != "" {
		t.Fatalf("expected explicit empty env policy to clear stale disabled tools, got %q", got)
	}
}

func TestBootstrapDisabledToolsByDefault_DoesNotReapplyAfterMarker(t *testing.T) {
	t.Setenv(disableToolsByDefaultSettingKey, "true")
	t.Setenv(disabledToolsSettingKey, "")

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	_ = NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	settings, err := store.GetSettings()
	if err != nil {
		t.Fatalf("failed to load settings: %v", err)
	}

	delete(settings, disabledToolsSettingKey)
	if err := store.SaveSettings(settings); err != nil {
		t.Fatalf("failed to persist settings without disabled tools: %v", err)
	}

	_ = NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	after, err := store.GetSettings()
	if err != nil {
		t.Fatalf("failed to load settings after restart: %v", err)
	}

	if strings.TrimSpace(after[disabledToolsSettingKey]) != "" {
		t.Fatalf("expected disabled tools not to be reapplied once bootstrap marker exists")
	}
}
