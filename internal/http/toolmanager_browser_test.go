package http

import (
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/A2gent/brute/internal/tools/integrationtools"
)

func browserChromeFromManager(t *testing.T, manager *tools.Manager) *integrationtools.BrowserChromeTool {
	t.Helper()
	registered, ok := manager.Get("browser_chrome")
	if !ok {
		t.Fatalf("expected browser_chrome to be registered")
	}
	chrome, ok := registered.(*integrationtools.BrowserChromeTool)
	if !ok {
		t.Fatalf("expected *BrowserChromeTool, got %T", registered)
	}
	return chrome
}

// A project session gets a fresh manager per run and per MCP bridge call; the
// browser tool must survive that or every call starts on a new about:blank page.
func TestToolManagerForSession_ProjectSessionReusesBrowserChromeTool(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	projectRoot := t.TempDir()
	now := time.Now()
	if err := store.SaveProject(&storage.Project{ID: "project", Name: "Project", Folder: &projectRoot, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	sessionManager := session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)

	newProjectSession := func() *session.Session {
		sess, err := sessionManager.Create("build")
		if err != nil {
			t.Fatalf("failed to create session: %v", err)
		}
		projectID := "project"
		sess.ProjectID = &projectID
		return sess
	}

	sess := newProjectSession()
	firstManager := server.toolManagerForSession(sess)
	secondManager := server.toolManagerForSession(sess)
	if firstManager == secondManager {
		t.Fatalf("expected project session to get a fresh manager per call")
	}
	first := browserChromeFromManager(t, firstManager)
	if first.WorkDir() != projectRoot {
		t.Fatalf("browser_chrome workdir = %q, want %q", first.WorkDir(), projectRoot)
	}
	if second := browserChromeFromManager(t, secondManager); second != first {
		t.Fatalf("expected the same browser_chrome instance across calls for one session")
	}

	if other := browserChromeFromManager(t, server.toolManagerForSession(newProjectSession())); other == first {
		t.Fatalf("expected a different session to get its own browser_chrome instance")
	}

	defaultSess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create default session: %v", err)
	}
	shared := browserChromeFromManager(t, server.toolManager)
	if got := browserChromeFromManager(t, server.toolManagerForSession(defaultSess)); got != shared {
		t.Fatalf("expected default-workdir session to use the shared browser_chrome instance")
	}
}
