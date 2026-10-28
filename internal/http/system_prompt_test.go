package http

import (
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestBuildSystemPromptForSessionStableAcrossTime(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), manager, store, speechcache.New(0), 0)
	sess, err := manager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	sess.CreatedAt = sess.CreatedAt.In(time.FixedZone("local", 3*60*60))
	first, err := server.buildSystemPromptForSession(sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "- Session created at: "+sess.CreatedAt.UTC().Format(time.RFC3339)) {
		t.Errorf("expected immutable session creation time in UTC")
	}
	if strings.Contains(first, "- Current time:") {
		t.Errorf("system prompt must not claim to contain the current time")
	}

	// Cross a wall-clock second to catch time.Now() even when a snapshot is rebuilt.
	time.Sleep(1100 * time.Millisecond)
	reloaded, err := manager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := server.buildSystemPromptForSession(reloaded); err != nil || first != second {
		t.Fatal("persisted snapshot changed across turns")
	}
	if rebuilt := server.composeSystemPromptSnapshot(reloaded).CombinedPrompt; first != rebuilt {
		t.Fatalf("recomposed prompt changed across time for the same session\nfirst: %s\nrebuilt: %s", first, rebuilt)
	}
}

func TestEnvironmentContextWithoutSessionOmitsTime(t *testing.T) {
	server := &Server{config: config.DefaultConfig()}
	for _, sess := range []*session.Session{nil, {}} {
		content, _ := server.resolveEnvironmentContextSection(sess)
		if strings.Contains(content, "time:") || strings.Contains(content, "Session created at:") {
			t.Fatal("missing creation time must not fall back to the wall clock")
		}
	}
}

func TestEnvironmentContextPointsToAutoRouterRules(t *testing.T) {
	cfg := config.DefaultConfig()
	server := &Server{config: cfg}
	if content, _ := server.resolveEnvironmentContextSection(nil); strings.Contains(content, "router_rules") {
		t.Fatal("hint must be omitted while the router is not configured")
	}
	cfg.Providers[string(config.ProviderAutoRouter)] = config.Provider{RouterProvider: "lmstudio"}
	content, _ := server.resolveEnvironmentContextSection(nil)
	for _, want := range []string{config.GetConfigPath(), "providers.automatic_router.router_rules", "/providers/automatic_router"} {
		if !strings.Contains(content, want) {
			t.Fatalf("environment context missing %q:\n%s", want, content)
		}
	}
}

func TestPromptFieldsLessDeterministicTies(t *testing.T) {
	cases := []struct{ left, right []string }{
		{[]string{"A", "x"}, []string{"a", "x"}},
		{[]string{"a", "b|c"}, []string{"a|b", "c"}},
		{[]string{"server", "stdio", "global", "1", "10"}, []string{"server", "stdio", "project", "1", "10"}},
	}
	for _, tc := range cases {
		if !promptFieldsLess(tc.left, tc.right) || promptFieldsLess(tc.right, tc.left) {
			t.Errorf("unstable ordering for %q and %q", tc.left, tc.right)
		}
	}
	if promptFieldsLess([]string{"same"}, []string{"same"}) {
		t.Fatal("identical fields must compare equal")
	}
}

func TestBuildSystemPromptPreservesLegacySnapshot(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), manager, store, speechcache.New(0), 0)
	sess, err := manager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	legacy := "Environment context:\n- Current time: 2026-10-14T18:33:24+03:00 (Local)"
	attachSessionSystemPromptSnapshot(sess, &systemPromptSnapshot{BasePrompt: "Legacy", CombinedPrompt: legacy})
	if got, err := server.buildSystemPromptForSession(sess); err != nil || got != legacy {
		t.Fatal("legacy snapshot migration would invalidate an existing cached prefix")
	}
}
