package http

import (
	"context"
	"encoding/json"
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

	systemPrompt, err := server.buildSystemPromptForSession(subSess)
	if err != nil {
		t.Fatal(err)
	}
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
	if !strings.Contains(systemPrompt, "Operating system:") || !strings.Contains(systemPrompt, "Session created at:") {
		t.Fatalf("expected sub-agent prompt to include OS and session creation time, got: %q", systemPrompt)
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

	systemPrompt, err := server.buildSystemPromptForSession(sess)
	if err != nil {
		t.Fatal(err)
	}
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
metrics:
  cost: 30
  speed: 90
  intelligence: 75
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

	systemPrompt, err := server.buildSystemPromptForSession(sess)
	if err != nil {
		t.Fatal(err)
	}
	subAgentsSectionStart := strings.Index(systemPrompt, availableConfiguredAgentsPromptHeader)
	if subAgentsSectionStart == -1 {
		t.Fatalf("expected configured agent listing, got: %q", systemPrompt)
	}
	subAgentsSection := systemPrompt[subAgentsSectionStart:]
	if !strings.Contains(subAgentsSection, "- youtube-transcriber-gemini - YouTube Transcriber (Gemini) (cost=30, speed=90, intelligence=75)") {
		t.Fatalf("expected running stored YAML agent with metrics in compact prompt, got: %q", systemPrompt)
	}
	if !strings.Contains(subAgentsSection, "stopped-agent") || !strings.Contains(subAgentsSection, "Uncreated Agent") {
		t.Fatalf("expected stopped and uncreated agents available on demand, got: %q", systemPrompt)
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
			ProjectID:         &otherProjectID,
			Provider:          "openai",
			Model:             "gpt-5.5",
			EnabledTools:      []string{"read"},
			InstructionBlocks: "[]",
			CreatedAt:         now,
			UpdatedAt:         now,
		},
		{
			ID:                "current-project-reviewer",
			Name:              "Current Project Reviewer",
			ProjectID:         &currentProjectID,
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
	currentProjectReviewerHealthPort := healthyDockerAgentPortForTest(t)
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
			{
				ID:     "current-project-reviewer-id",
				Image:  "a2gent-brute:latest",
				State:  "running",
				Status: "Up",
				Names:  "agent-current-project-reviewer__project-project-current",
				Ports:  "0.0.0.0:" + strconv.Itoa(currentProjectReviewerHealthPort) + "->8080/tcp",
				Labels: localAgentManagerLabelKey + "=" + localAgentManagerLabelValue + "," + dockerRuntimeManagedLabelKey + "=true," + dockerRuntimeAgentDefLabelKey + "=current-project-reviewer,a2gent.project_id=project-current",
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
	systemPrompt, err := server.buildSystemPromptForSession(sess)
	if err != nil {
		t.Fatal(err)
	}
	subAgentsSectionStart := strings.Index(systemPrompt, availableConfiguredAgentsPromptHeader)
	if subAgentsSectionStart == -1 {
		t.Fatalf("expected configured agent listing, got: %q", systemPrompt)
	}
	subAgentsSection := systemPrompt[subAgentsSectionStart:]
	if !strings.Contains(subAgentsSection, "- running-reviewer - Running Reviewer") {
		t.Fatalf("expected running saved sub-agent in compact prompt, got: %q", systemPrompt)
	}
	if !strings.Contains(subAgentsSection, "- current-project-reviewer - Current Project Reviewer") {
		t.Fatalf("expected current-project saved sub-agent in compact prompt, got: %q", systemPrompt)
	}
	if !strings.Contains(subAgentsSection, "- stopped-reviewer - Stopped Reviewer") {
		t.Fatalf("expected stopped saved sub-agent available on demand, got: %q", systemPrompt)
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

	systemPrompt, err := server.buildSystemPromptForSession(sess)
	if err != nil {
		t.Fatal(err)
	}
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

	systemPrompt, err := server.buildSystemPromptForSession(sess)
	if err != nil {
		t.Fatal(err)
	}
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

	systemPrompt, err := server.buildSystemPromptForSession(sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(systemPrompt, "Branch-specific project instructions.") {
		t.Fatalf("expected branch task documentation from project settings in prompt, got: %q", systemPrompt)
	}
	if strings.Contains(systemPrompt, "Branch task documentation directory is not configured") {
		t.Fatalf("did not expect legacy global settings error, got: %q", systemPrompt)
	}

	htmlPath := filepath.Join(docsDir, "PROJECT-123-custom-prompt.html")
	if err := os.WriteFile(htmlPath, []byte("<h1>Interactive task instructions</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"content", "path"} {
		project.Settings[projectBranchTaskDocModeSettingKey] = mode
		if err := store.SaveProject(project); err != nil {
			t.Fatal(err)
		}
		rendered, path, _, message := server.resolveBranchTaskDocSection(sess, "", 1)
		if message != "" || path != htmlPath || !strings.Contains(rendered, htmlPath) {
			t.Fatalf("HTML reference not selected in %s mode: %q %q %q", mode, path, rendered, message)
		}
		if mode == "content" && !strings.Contains(rendered, "<h1>Interactive task instructions</h1>") {
			t.Fatalf("HTML content missing: %q", rendered)
		}
		if strings.Contains(rendered, "Branch-specific project instructions.") {
			t.Fatalf("stale Markdown selected in %s mode: %q", mode, rendered)
		}
	}
}
