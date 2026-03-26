package http

import (
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
