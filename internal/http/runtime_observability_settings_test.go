package http

import (
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestRuntimeReasoningPersistenceEnabled_DefaultsFalse(t *testing.T) {
	t.Setenv(runtimeReasoningPersistenceSettingKey, "")

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), session.NewManager(store), store, speechcache.New(0), 0)
	if server.runtimeReasoningPersistenceEnabled() {
		t.Fatal("expected runtime reasoning persistence to default to disabled")
	}
}

func TestRuntimeReasoningPersistenceEnabled_EnvWins(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()
	if err := store.SaveSettings(map[string]string{runtimeReasoningPersistenceSettingKey: "false"}); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), session.NewManager(store), store, speechcache.New(0), 0)

	t.Setenv(runtimeReasoningPersistenceSettingKey, "true")
	if !server.runtimeReasoningPersistenceEnabled() {
		t.Fatal("expected explicit env true to enable runtime reasoning persistence")
	}

	t.Setenv(runtimeReasoningPersistenceSettingKey, "false")
	if server.runtimeReasoningPersistenceEnabled() {
		t.Fatal("expected explicit env false to disable runtime reasoning persistence")
	}
}

func TestRuntimeReasoningPersistenceEnabled_SettingEnablesWhenEnvUnset(t *testing.T) {
	t.Setenv(runtimeReasoningPersistenceSettingKey, "")

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()
	if err := store.SaveSettings(map[string]string{runtimeReasoningPersistenceSettingKey: "true"}); err != nil {
		t.Fatalf("failed to save settings: %v", err)
	}

	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), session.NewManager(store), store, speechcache.New(0), 0)
	if !server.runtimeReasoningPersistenceEnabled() {
		t.Fatal("expected sqlite setting true to enable runtime reasoning persistence")
	}
}

func TestAgentConfigFromTargetSetsPersistRuntimeReasoningOnlyForAnthropic(t *testing.T) {
	t.Setenv(runtimeReasoningPersistenceSettingKey, "true")

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), session.NewManager(store), store, speechcache.New(0), 0)
	sess := session.New("build")

	anthropicCfg := server.agentConfigFromTarget(sess, &executionTarget{ProviderType: config.ProviderAnthropic, Model: "claude"}, "prompt", 10, 0)
	if !anthropicCfg.PersistRuntimeReasoning {
		t.Fatal("expected anthropic provider to inherit runtime reasoning persistence setting")
	}

	openaiCfg := server.agentConfigFromTarget(sess, &executionTarget{ProviderType: config.ProviderOpenAI, Model: "gpt"}, "prompt", 10, 0)
	if openaiCfg.PersistRuntimeReasoning {
		t.Fatal("expected non-anthropic provider to leave PersistRuntimeReasoning false")
	}
}
