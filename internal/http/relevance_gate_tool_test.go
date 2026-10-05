package http

import (
	"reflect"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestRelevanceGateRegistration(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv(disableToolsByDefaultSettingKey, "false")
	t.Setenv(syncDisabledToolsFromEnvSettingKey, "false")
	server, store := newClassifyRegistrationTestServer(t, config.Provider{APIKey: "key"}, nil)
	if _, ok := server.toolManager.Get("relevance_gate"); !ok {
		t.Fatal("relevance_gate not registered")
	}
	projectDir := t.TempDir()
	project := &storage.Project{ID: "relevance-project", Name: "Relevance", Folder: &projectDir}
	if err := store.SaveProject(project); err != nil {
		t.Fatal(err)
	}
	manager := server.toolManagerForSession(&session.Session{ID: "gate-session", ProjectID: &project.ID})
	tool, ok := manager.Get("relevance_gate")
	if !ok || reflect.ValueOf(tool).Elem().FieldByName("workDir").String() != projectDir {
		t.Fatal("gate must use project workspace")
	}
	server.config.Tools.RelevanceGateDisabled = true
	server.registerRelevanceGateTool(server.toolManager)
	if _, ok := server.toolManager.Get("relevance_gate"); ok {
		t.Fatal("disable switch must unregister gate")
	}
	if _, ok := server.toolManager.Get("classify"); !ok {
		t.Fatal("gate switch must not affect classify")
	}
	other := tools.NewManager(projectDir)
	server.registerRelevanceGateTool(other)
	if _, ok := other.Get("relevance_gate"); ok {
		t.Fatal("disabled gate must not register")
	}
	server.config.Tools.RelevanceGateDisabled = false
	server.config.Providers["jev"] = config.Provider{}
	server.registerRelevanceGateTool(server.toolManager)
	if _, ok := server.toolManager.Get("relevance_gate"); !ok {
		t.Fatal("no credentials must retain lossless fallback")
	}
}
