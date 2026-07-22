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

	"github.com/A2gent/brute/internal/storage"
)

func newComfyUITestStore(t *testing.T, baseURL string, extras map[string]string) storage.Store {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := map[string]string{
		"base_url": baseURL,
	}
	for k, v := range extras {
		cfg[k] = v
	}
	now := time.Now().UTC()
	if err := store.SaveIntegration(&storage.Integration{
		ID:        "comfy-1",
		Provider:  "comfyui",
		Name:      "Local ComfyUI",
		Mode:      "notify_only",
		Enabled:   true,
		Config:    cfg,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("failed to save comfyui integration: %v", err)
	}
	return store
}

func TestComfyUIGenerateImageToolNameAndSchema(t *testing.T) {
	t.Parallel()
	tool := NewComfyUIGenerateImageTool(nil, "")
	if tool.Name() != "comfyui_generate_image" {
		t.Fatalf("unexpected name: %s", tool.Name())
	}
	schema := tool.Schema()
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("expected schema properties map")
	}
	if _, ok := props["prompt"]; !ok {
		t.Fatal("expected prompt property")
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("expected required []string")
	}
	found := false
	for _, item := range required {
		if item == "prompt" {
			found = true
		}
	}
	if !found {
		t.Fatal("prompt should be required")
	}
}

func TestComfyUIGenerateImageEmptyPrompt(t *testing.T) {
	t.Parallel()
	tool := NewComfyUIGenerateImageTool(newComfyUITestStore(t, "http://127.0.0.1:8188", nil), t.TempDir())
	params, _ := json.Marshal(map[string]string{"prompt": "  "})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for empty prompt")
	}
	if !strings.Contains(result.Error, "prompt") {
		t.Fatalf("expected prompt error, got %q", result.Error)
	}
}

func TestComfyUIGenerateImageMissingIntegration(t *testing.T) {
	t.Parallel()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tool := NewComfyUIGenerateImageTool(store, t.TempDir())
	params, _ := json.Marshal(map[string]string{"prompt": "a cat"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure without comfyui integration")
	}
	if !strings.Contains(strings.ToLower(result.Error), "comfyui") {
		t.Fatalf("expected comfyui config error, got %q", result.Error)
	}
}

func TestComfyUIGenerateImageSuccess(t *testing.T) {
	t.Parallel()

	var promptPosts atomic.Int32
	var historyGets atomic.Int32
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/models/checkpoints":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`["demo.safetensors","other.ckpt"]`))
		case r.Method == http.MethodPost && r.URL.Path == "/prompt":
			promptPosts.Add(1)
			body, _ := io.ReadAll(r.Body)
			var payload map[string]interface{}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("invalid prompt payload: %v", err)
			}
			promptGraph, ok := payload["prompt"].(map[string]interface{})
			if !ok || len(promptGraph) == 0 {
				t.Fatal("expected prompt workflow graph")
			}
			loader, ok := promptGraph["4"].(map[string]interface{})
			if !ok {
				t.Fatal("expected checkpoint loader node")
			}
			inputs, _ := loader["inputs"].(map[string]interface{})
			if inputs["ckpt_name"] != "demo.safetensors" {
				t.Fatalf("unexpected checkpoint: %v", inputs["ckpt_name"])
			}
			textNode, ok := promptGraph["6"].(map[string]interface{})
			if !ok {
				t.Fatal("expected positive prompt node")
			}
			textInputs, _ := textNode["inputs"].(map[string]interface{})
			if textInputs["text"] != "a red fox" {
				t.Fatalf("unexpected prompt text: %v", textInputs["text"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"prompt_id":"pid-1","number":1}`))
		case r.Method == http.MethodGet && r.URL.Path == "/history/pid-1":
			n := historyGets.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if n == 1 {
				_, _ = w.Write([]byte(`{}`))
				return
			}
			_, _ = w.Write([]byte(`{
				"pid-1": {
					"outputs": {
						"9": {
							"images": [
								{"filename":"a2gent_00001_.png","subfolder":"","type":"output"}
							]
						}
					},
					"status": {"status_str":"success","completed":true}
				}
			}`))
		case r.Method == http.MethodGet && r.URL.Path == "/view":
			if r.URL.Query().Get("filename") != "a2gent_00001_.png" {
				t.Fatalf("unexpected filename: %s", r.URL.Query().Get("filename"))
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	outDir := t.TempDir()
	tool := NewComfyUIGenerateImageTool(newComfyUITestStore(t, server.URL, nil), outDir)
	tool.pollInterval = 5 * time.Millisecond
	tool.client.Timeout = 5 * time.Second

	params, _ := json.Marshal(map[string]string{"prompt": "a red fox"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if promptPosts.Load() != 1 {
		t.Fatalf("expected 1 prompt post, got %d", promptPosts.Load())
	}
	if historyGets.Load() < 2 {
		t.Fatalf("expected history polling, got %d gets", historyGets.Load())
	}

	imageFile, ok := result.Metadata["image_file"].(map[string]interface{})
	if !ok {
		t.Fatal("expected image_file metadata")
	}
	path, _ := imageFile["path"].(string)
	if path == "" {
		t.Fatal("expected saved image path")
	}
	if source, _ := imageFile["source_tool"].(string); source != "comfyui_generate_image" {
		t.Fatalf("unexpected source_tool: %v", source)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read saved image: %v", err)
	}
	if string(data) != string(pngBytes) {
		t.Fatal("saved image bytes mismatch")
	}
	if !strings.HasPrefix(filepath.Base(path), "pid-1") && !strings.Contains(path, "pid-1") {
		// generation uses prompt id prefix when available
		if !strings.Contains(result.Output, path) {
			t.Fatalf("expected output to mention saved path, got %q", result.Output)
		}
	}
}

func TestComfyUIGenerateImageUsesConfiguredCheckpoint(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/prompt":
			body, _ := io.ReadAll(r.Body)
			var payload map[string]interface{}
			_ = json.Unmarshal(body, &payload)
			promptGraph := payload["prompt"].(map[string]interface{})
			loader := promptGraph["4"].(map[string]interface{})
			inputs := loader["inputs"].(map[string]interface{})
			if inputs["ckpt_name"] != "sdxl.safetensors" {
				t.Fatalf("expected configured checkpoint, got %v", inputs["ckpt_name"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"prompt_id":"pid-2"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/history/pid-2":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"pid-2":{"outputs":{"9":{"images":[{"filename":"out.png","subfolder":"","type":"output"}]}},"status":{"completed":true}}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/view":
			_, _ = w.Write([]byte("png"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tool := NewComfyUIGenerateImageTool(newComfyUITestStore(t, server.URL, map[string]string{
		"checkpoint": "sdxl.safetensors",
	}), t.TempDir())
	tool.pollInterval = 5 * time.Millisecond

	params, _ := json.Marshal(map[string]string{"prompt": "sunset"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %q", result.Error)
	}
}

func TestComfyUIGenerateImagePromptAPIError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models/checkpoints" {
			_, _ = w.Write([]byte(`["demo.safetensors"]`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"node missing","type":"prompt_outputs_failed"}}`))
	}))
	defer server.Close()

	tool := NewComfyUIGenerateImageTool(newComfyUITestStore(t, server.URL, nil), t.TempDir())
	params, _ := json.Marshal(map[string]string{"prompt": "broken"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure")
	}
	if !strings.Contains(result.Error, "node missing") && !strings.Contains(result.Error, "400") {
		t.Fatalf("expected API error detail, got %q", result.Error)
	}
}

func TestBuildDefaultComfyUIWorkflow(t *testing.T) {
	t.Parallel()
	workflow := buildDefaultComfyUIWorkflow(comfyUIWorkflowOptions{
		Prompt:         "hello",
		NegativePrompt: "blurry",
		Checkpoint:     "model.safetensors",
		Width:          768,
		Height:         512,
		Steps:          25,
		CFG:            6.5,
		Seed:           42,
		SamplerName:    "euler",
		Scheduler:      "normal",
	})
	loader := workflow["4"].(map[string]interface{})
	inputs := loader["inputs"].(map[string]interface{})
	if inputs["ckpt_name"] != "model.safetensors" {
		t.Fatalf("unexpected ckpt: %v", inputs["ckpt_name"])
	}
	pos := workflow["6"].(map[string]interface{})["inputs"].(map[string]interface{})
	if pos["text"] != "hello" {
		t.Fatalf("unexpected positive prompt: %v", pos["text"])
	}
	neg := workflow["7"].(map[string]interface{})["inputs"].(map[string]interface{})
	if neg["text"] != "blurry" {
		t.Fatalf("unexpected negative prompt: %v", neg["text"])
	}
	latent := workflow["5"].(map[string]interface{})["inputs"].(map[string]interface{})
	if latent["width"] != 768 || latent["height"] != 512 {
		t.Fatalf("unexpected latent size: %#v", latent)
	}
}
