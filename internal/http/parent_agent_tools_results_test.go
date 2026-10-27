package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestParentAgentToolsPreservesAssetResultsOnlyInScopedCalls(t *testing.T) {
	parent, parentSess, child, childSess, ctx, _ := parentAgentToolsTestPair(t)
	for _, success := range []bool{true, false} {
		t.Run(map[bool]string{true: "success", false: "failure"}[success], func(t *testing.T) {
			want := &tools.Result{
				Success: success, Output: "rendered asset",
				Metadata: map[string]interface{}{
					"image_path": "/tmp/preview.png", "model_path": "/tmp/model.glb",
					"image_base64": "cG5n", "mime_type": "image/png",
					"artifacts": []interface{}{map[string]interface{}{"kind": "3d", "path": "/tmp/model.glb"}},
				},
			}
			if !success {
				want.Error = "render partially failed"
			}
			parent.toolManager.Register(&bridgeDelegationProbe{name: "blender_run_script", execute: func(context.Context, json.RawMessage) (*tools.Result, error) {
				return want, nil
			}})
			got, err := child.toolManagerForSession(childSess).Execute(ctx, "blender_run_script", json.RawMessage(`{}`))
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("native asset result lost: got %#v, want %#v, err %v", got, want, err)
			}
			// The same tool via an ordinary CLI token must not expose the native
			// metadata envelope, even if the caller asks for it in arguments.
			token, revoke := parent.mustMintMCPBridgeToken(t, parentSess.ID)
			defer revoke()
			code, rpc := serveMCPBridge(t, parent, newMCPBridgeRequest(t, parentSess.ID, token, mcpBridgeRPC(t, 1, "tools/call", map[string]interface{}{
				"name": "blender_run_script", "arguments": map[string]interface{}{"structuredContent": true},
			})))
			text, failed := mcpBridgeCallResultText(t, rpc)
			wantText := want.Output
			if !success {
				wantText = "Error: " + want.Error
			}
			if code != http.StatusOK || text != wantText || failed == success || len(rpc.Result) != 2 {
				t.Fatalf("ordinary MCP contract changed: %d %#v", code, rpc.Result)
			}
		})
	}
}

func TestParentAgentToolsBindsConfiguredChildWorkspaceAtMint(t *testing.T) {
	parent, parentSess, child, childSess, ctx, remove := parentAgentToolsTestPair(t)
	parentDir, childDir, laterDir := t.TempDir(), t.TempDir(), t.TempDir()
	now := time.Now()
	for id, dir := range map[string]string{"parent-project": parentDir, "child-project": childDir} {
		folder := dir
		if err := parent.store.SaveProject(&storage.Project{ID: id, Name: id, Folder: &folder, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	projectID := "parent-project"
	parentSess.ProjectID = &projectID
	parentSess.Metadata = map[string]interface{}{"unified_agent_id": "workspace-host", "docker_workspace": map[string]interface{}{"host_path": laterDir}}
	if err := parent.sessionManager.Save(parentSess); err != nil {
		t.Fatal(err)
	}
	definition := "version: \"1\"\nagent:\n  id: workspace-host\n  name: Workspace host\nruntime:\n  type: host\nworkspace:\n  scope: configured_project\nlocal:\n  project_bindings:\n    configured_project: child-project\n"
	if err := parent.store.SaveAgentDefinition(&storage.AgentDefinitionRecord{ID: "workspace-host", Name: "Workspace host", ProjectID: &projectID, Runtime: "host", DefinitionYAML: definition, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for dir, value := range map[string]string{parentDir: "wrong-parent", childDir: "bound-child", laterDir: "wrong-later"} {
		workflow := `{"1":{"class_type":"PrimitiveString","inputs":{"value":"` + value + `"}}}`
		if err := os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(workflow), 0600); err != nil {
			t.Fatal(err)
		}
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/prompt":
			var body struct {
				Prompt map[string]struct {
					Inputs map[string]string `json:"inputs"`
				} `json:"prompt"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if value := body.Prompt["1"].Inputs["value"]; value != "bound-child" {
				t.Errorf("workflow resolved outside bound child workspace: %q", value)
			}
			_, _ = w.Write([]byte(`{"prompt_id":"workspace"}`))
		case "/history/workspace":
			_, _ = w.Write([]byte(`{"workspace":{"outputs":{},"status":{"status_str":"success","completed":true}}}`))
		default:
			t.Errorf("unexpected upstream request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	if err := parent.store.SaveIntegration(&storage.Integration{ID: "workspace-comfy", Provider: "comfyui", Name: "Parent comfy", Enabled: true, Config: map[string]string{"base_url": upstream.URL}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	headers, revoke, err := parent.directAgentToolsHeaders(t.Context(), parentSess, agentdef.RuntimeHost)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	// Configuration changes after mint must not redirect an active capability.
	if err := parent.store.SaveProject(&storage.Project{ID: "child-project", Name: "Child", Folder: &laterDir, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	remove()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header = headers
	cleanup, err := child.acquireParentAgentTools(t.Context(), req, childSess)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	manager := child.toolManagerForSession(childSess)
	args, _ := json.Marshal(map[string]string{"workflow_path": "workflow.json", "workdir": laterDir, "workspace": parentDir})
	result, err := manager.Execute(ctx, "comfyui_run_workflow", args)
	if err != nil || !result.Success {
		t.Fatalf("relative workflow failed: %+v %v", result, err)
	}
	settings, _ := parent.store.GetSettings()
	settings[disabledToolsSettingKey] = `["comfyui_run_workflow"]`
	if err := parent.store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	result, err = manager.Execute(ctx, "comfyui_run_workflow", args)
	if err != nil || result.Success || !strings.Contains(result.Error, "tool not found") {
		t.Fatalf("workspace override bypassed parent filtering: %+v %v", result, err)
	}
}

func TestParentAgentToolsRejectsInvalidConfiguredWorkspace(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	sess, _ := s.sessionManager.Create("build")
	sess.Metadata = map[string]interface{}{"unified_agent_id": "invalid-workspace"}
	now := time.Now()
	definition := "version: \"1\"\nagent:\n  id: invalid-workspace\n  name: Invalid workspace\nruntime:\n  type: host\nworkspace:\n  scope: configured_project\nlocal:\n  project_bindings:\n    configured_project: nonexistent-project\n"
	if err := s.store.SaveAgentDefinition(&storage.AgentDefinitionRecord{ID: "invalid-workspace", Name: "Invalid workspace", Runtime: "host", DefinitionYAML: definition, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, revoke, err := s.directAgentToolsHeaders(t.Context(), sess, agentdef.RuntimeHost); err == nil {
		revoke()
		t.Fatal("minted capability with invalid workspace")
	}
	s.mcpBridge.mu.Lock()
	defer s.mcpBridge.mu.Unlock()
	if len(s.mcpBridge.tokens) != 0 {
		t.Fatal("invalid workspace leaked token")
	}
}
