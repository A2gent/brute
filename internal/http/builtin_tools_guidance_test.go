package http

import (
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestResolveBuiltInToolsGuidanceListsNamesAndManHint(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	guidance, usingDefault := server.resolveBuiltInToolsGuidance(map[string]string{})
	if !usingDefault {
		t.Fatalf("expected built-in default guidance flag")
	}
	if !strings.Contains(guidance, "Available built-in tools:") {
		t.Fatalf("expected compact tool list header, got: %q", guidance)
	}
	if !strings.Contains(guidance, "Use `man` with a tool name") {
		t.Fatalf("expected lazy man hint, got: %q", guidance)
	}
	if !strings.Contains(guidance, "- question") || !strings.Contains(guidance, "- man") {
		t.Fatalf("expected tool names in guidance, got: %q", guidance)
	}
	if strings.Contains(guidance, "Ask the user a question when") {
		t.Fatalf("expected compact guidance without verbose tool descriptions, got: %q", guidance)
	}
}

func TestResolveBuiltInToolsGuidanceRespectsDisabledTools(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	guidance, _ := server.resolveBuiltInToolsGuidance(map[string]string{
		disabledToolsSettingKey: `["question"]`,
	})
	if strings.Contains(guidance, "- question") {
		t.Fatalf("did not expect disabled question tool in guidance, got: %q", guidance)
	}
	if !strings.Contains(guidance, "- man") {
		t.Fatalf("expected man tool to remain visible, got: %q", guidance)
	}
}
