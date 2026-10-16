package http

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

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
