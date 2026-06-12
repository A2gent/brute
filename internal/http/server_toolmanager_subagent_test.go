package http

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

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

	mgr := server.buildSubAgentToolManager(subSess, []string{"youtube_transcript"})
	if _, ok := mgr.Get("youtube_transcript"); !ok {
		t.Fatalf("expected youtube_transcript to be available for project-scoped sub-agent")
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
	if strings.Contains(systemPrompt, "Available sub-agents for delegation:") {
		t.Fatalf("expected sub-agent prompt to omit main-agent sub-agent listing")
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
}

func TestBootstrapDisabledToolsByDefault(t *testing.T) {
	t.Setenv(disableToolsByDefaultSettingKey, "true")

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

func TestBootstrapDisabledToolsByDefault_DoesNotReapplyAfterMarker(t *testing.T) {
	t.Setenv(disableToolsByDefaultSettingKey, "true")

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
