package http

import (
	"encoding/json"
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

func TestServerRegistersCreateLocalDockerAgentsBulkTool(t *testing.T) {
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
