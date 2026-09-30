package http

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListAgentsDiscoversGlobalCatalogWithoutDockerOrRegistry(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "reviewer.yaml"), []byte("version: \"1\"\nagent:\n  id: reviewer\n  name: Code Reviewer\nruntime:\n  type: docker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	settings, _ := s.store.GetSettings()
	settings[agentDefinitionsFolderSettingKey] = root
	if err := s.store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	sess, _ := s.sessionManager.Create("build")
	tool, ok := s.toolManagerForSession(sess).Get("list_agents")
	if !ok {
		t.Fatal("missing local agent discovery tool")
	}
	ctx := context.WithValue(context.Background(), "session_id", sess.ID)
	result, err := tool.Execute(ctx, json.RawMessage(`{"query":"Code Reviewer"}`))
	if err != nil || result == nil || !result.Success {
		t.Fatalf("list failed: %+v %v", result, err)
	}
	if !strings.Contains(result.Output, `"id": "reviewer"`) || strings.Contains(result.Output, "instructions") {
		t.Fatalf("expected compact catalog: %s", result.Output)
	}
	defs := s.mcpBridgeToolList(sess)
	found := false
	for _, def := range defs {
		if def["name"] == "list_agents" {
			found = true
		}
	}
	if !found {
		t.Fatal("local discovery absent from bridge")
	}
}
