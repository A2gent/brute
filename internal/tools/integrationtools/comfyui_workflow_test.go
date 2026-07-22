package integrationtools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestComfyUIRunWorkflowToolNameAndSchema(t *testing.T) {
	t.Parallel()
	tool := NewComfyUIRunWorkflowTool(nil, "", "")
	if tool.Name() != "comfyui_run_workflow" {
		t.Fatalf("unexpected name: %s", tool.Name())
	}
	props, ok := tool.Schema()["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("expected schema properties map")
	}
	for _, name := range []string{"workflow", "workflow_path", "input_overrides", "integration_id", "integration_name"} {
		if _, ok := props[name]; !ok {
			t.Fatalf("expected %s property", name)
		}
	}
}

func TestComfyUIRunWorkflowDownloadsMixedArtifactsAndAppliesOverrides(t *testing.T) {
	t.Parallel()

	var historyGets atomic.Int32
	files := map[string]struct {
		contentType string
		body        string
	}{
		"preview.png": {"image/png", "png-data"},
		"sound.flac":  {"audio/flac", "flac-data"},
		"clip.mp4":    {"video/mp4", "mp4-data"},
		"mesh.glb":    {"model/gltf-binary", "glb-data"},
		"notes.txt":   {"text/plain", "text-data"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/prompt":
			body, _ := io.ReadAll(r.Body)
			var payload map[string]interface{}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("invalid prompt payload: %v", err)
			}
			graph := payload["prompt"].(map[string]interface{})
			inputs := graph["6"].(map[string]interface{})["inputs"].(map[string]interface{})
			if inputs["text"] != "overridden prompt" {
				t.Fatalf("input override was not applied: %#v", inputs)
			}
			if inputs["clip"] == nil {
				t.Fatal("input override should merge without dropping existing inputs")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"prompt_id":"mixed-1","node_errors":{}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/history/mixed-1":
			if historyGets.Add(1) == 1 {
				_, _ = w.Write([]byte(`{}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"mixed-1": {
					"outputs": {
						"10": {"images":[{"filename":"preview.png","subfolder":"images","type":"output"}]},
						"11": {"audio":[{"filename":"sound.flac","subfolder":"audio","type":"output"}]},
						"12": {"video":[{"filename":"clip.mp4","subfolder":"video","type":"output"}]},
						"13": {"3d":[{"filename":"mesh.glb","subfolder":"3d","type":"output"}]},
						"14": {"text":["hello"],"files":[{"filename":"notes.txt","subfolder":"text","type":"output"}]}
					},
					"status": {"status_str":"success","completed":true}
				}
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/view":
			file, ok := files[r.URL.Query().Get("filename")]
			if !ok {
				t.Fatalf("unexpected artifact download: %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", file.contentType)
			_, _ = w.Write([]byte(file.body))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	outDir := t.TempDir()
	tool := NewComfyUIRunWorkflowTool(newComfyUITestStore(t, server.URL, nil), t.TempDir(), outDir)
	tool.runner.pollInterval = 5 * time.Millisecond
	tool.runner.client.Timeout = 5 * time.Second

	params, _ := json.Marshal(map[string]interface{}{
		"workflow": map[string]interface{}{
			"6": map[string]interface{}{
				"class_type": "CLIPTextEncode",
				"inputs": map[string]interface{}{
					"text": "original prompt",
					"clip": []interface{}{"4", 1},
				},
			},
		},
		"input_overrides": map[string]interface{}{
			"6": map[string]interface{}{"text": "overridden prompt"},
		},
	})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %q", result.Error)
	}
	if historyGets.Load() < 2 {
		t.Fatalf("expected history polling, got %d", historyGets.Load())
	}

	artifacts, ok := result.Metadata["artifacts"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected artifacts metadata, got %#v", result.Metadata["artifacts"])
	}
	if len(artifacts) != 5 {
		t.Fatalf("expected 5 artifacts, got %d: %#v", len(artifacts), artifacts)
	}
	wantKinds := []string{"image", "audio", "video", "3d", "text"}
	for i, wantKind := range wantKinds {
		if artifacts[i]["kind"] != wantKind {
			t.Fatalf("artifact %d: expected kind %q, got %#v", i, wantKind, artifacts[i])
		}
		path, _ := artifacts[i]["path"].(string)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("artifact %d was not saved: %v", i, err)
		}
	}
	imageFile, ok := result.Metadata["image_file"].(map[string]interface{})
	if !ok || imageFile["source_tool"] != "comfyui_run_workflow" {
		t.Fatalf("expected backward-compatible image_file metadata, got %#v", result.Metadata["image_file"])
	}
	if !strings.Contains(result.Output, "5 artifact(s)") {
		t.Fatalf("unexpected result output: %q", result.Output)
	}
}

func TestComfyUIRunWorkflowLoadsAPIWorkflowFile(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/prompt":
			var payload map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			graph := payload["prompt"].(map[string]interface{})
			if graph["1"].(map[string]interface{})["class_type"] != "PrimitiveString" {
				t.Fatalf("unexpected workflow: %#v", graph)
			}
			_, _ = w.Write([]byte(`{"prompt_id":"file-1"}`))
		case "/history/file-1":
			_, _ = w.Write([]byte(`{"file-1":{"outputs":{"2":{"text":["done"]}},"status":{"status_str":"success","completed":true}}}`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	workDir := t.TempDir()
	workflowPath := filepath.Join(workDir, "workflow.json")
	if err := os.WriteFile(workflowPath, []byte(`{"prompt":{"1":{"class_type":"PrimitiveString","inputs":{"value":"hello"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewComfyUIRunWorkflowTool(newComfyUITestStore(t, server.URL, nil), workDir, t.TempDir())
	tool.runner.pollInterval = time.Millisecond
	params, _ := json.Marshal(map[string]string{"workflow_path": "workflow.json"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %q", result.Error)
	}
	if !strings.Contains(result.Output, "no downloadable artifacts") {
		t.Fatalf("expected no-artifact success detail, got %q", result.Output)
	}
}

func TestComfyUIRunWorkflowRejectsUIFormat(t *testing.T) {
	t.Parallel()
	tool := NewComfyUIRunWorkflowTool(newComfyUITestStore(t, "http://127.0.0.1:8188", nil), t.TempDir(), t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"workflow": map[string]interface{}{"nodes": []interface{}{}, "links": []interface{}{}},
	})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || !strings.Contains(strings.ToLower(result.Error), "api format") {
		t.Fatalf("expected API format guidance, got %#v", result)
	}
}

func TestComfyUIRunWorkflowReturnsExecutionErrorDetail(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/prompt":
			_, _ = w.Write([]byte(`{"prompt_id":"failed-1"}`))
		case "/history/failed-1":
			_, _ = w.Write([]byte(`{
				"failed-1": {
					"outputs": {},
					"status": {
						"status_str":"error",
						"completed":false,
						"messages":[["execution_error",{"node_id":"7","node_type":"SaveGLB","exception_message":"mesh is empty"}]]
					}
				}
			}`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	tool := NewComfyUIRunWorkflowTool(newComfyUITestStore(t, server.URL, nil), t.TempDir(), t.TempDir())
	tool.runner.pollInterval = time.Millisecond
	params, _ := json.Marshal(map[string]interface{}{
		"workflow": map[string]interface{}{"7": map[string]interface{}{"class_type": "SaveGLB", "inputs": map[string]interface{}{}}},
	})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "mesh is empty") || !strings.Contains(result.Error, "SaveGLB") {
		t.Fatalf("expected execution detail, got %#v", result)
	}
}

func TestComfyUIRunWorkflowLocal(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("COMFYUI_BASE_URL"))
	if baseURL == "" {
		t.Skip("set COMFYUI_BASE_URL to run against a local ComfyUI")
	}
	tool := NewComfyUIRunWorkflowTool(newComfyUITestStore(t, baseURL, nil), t.TempDir(), t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{"workflow": map[string]interface{}{
		"1": map[string]interface{}{"class_type": "PrimitiveString", "inputs": map[string]interface{}{"value": "a2gent local workflow test"}},
		"2": map[string]interface{}{"class_type": "SaveText", "inputs": map[string]interface{}{"text": []interface{}{"1", 0}, "filename_prefix": "a2gent-test/artifact", "format": "txt"}},
	}})
	result, err := tool.Execute(context.Background(), params)
	if err != nil || !result.Success {
		t.Fatalf("local ComfyUI run failed: result=%#v err=%v", result, err)
	}
	artifacts, _ := result.Metadata["artifacts"].([]map[string]interface{})
	if len(artifacts) != 1 || artifacts[0]["kind"] != "text" {
		t.Fatalf("unexpected local artifacts: %#v", artifacts)
	}
}
