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
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

// 1x1 PNG so image inputs pass content sniffing.
var testPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

func newModel3DTestStore(t *testing.T, provider, baseURL string) storage.Store {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	if err := store.SaveIntegration(&storage.Integration{
		ID: provider + "-1", Provider: provider, Name: provider, Mode: "notify_only", Enabled: true,
		Config:    map[string]string{"api_key": "test-key", "base_url": baseURL},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("failed to save integration: %v", err)
	}
	return store
}

func TestMeshyGenerate3DImageToModel(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var createBody map[string]interface{}
	polls := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" && strings.HasPrefix(r.URL.Path, "/openapi") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/openapi/v1/image-to-3d":
			_ = json.NewDecoder(r.Body).Decode(&createBody)
			_, _ = io.WriteString(w, `{"result":"task-1"}`)
		case r.URL.Path == "/openapi/v1/image-to-3d/task-1":
			polls++
			if polls < 2 {
				_, _ = io.WriteString(w, `{"id":"task-1","status":"IN_PROGRESS","progress":50}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":"task-1","status":"SUCCEEDED","model_urls":{"glb":"`+srv.URL+`/files/model.glb"},"thumbnail_url":"`+srv.URL+`/files/preview.png"}`)
		case r.URL.Path == "/files/model.glb":
			_, _ = io.WriteString(w, "glTF-binary")
		case r.URL.Path == "/files/preview.png":
			_, _ = w.Write(testPNG)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "input.png"), testPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(workDir, "generated", "meshy")
	tool := NewMeshyGenerate3DTool(newModel3DTestStore(t, "meshy", srv.URL), workDir, outDir)
	tool.pollInterval = time.Millisecond

	params, _ := json.Marshal(map[string]interface{}{"image": "input.png", "texture_prompt": "rusty metal"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %q", result.Error)
	}
	imageURL, _ := createBody["image_url"].(string)
	if !strings.HasPrefix(imageURL, "data:image/png;base64,") {
		t.Fatalf("expected local image inlined as data URI, got %q", imageURL)
	}
	if createBody["texture_prompt"] != "rusty metal" {
		t.Fatalf("expected texture_prompt to be forwarded, got %v", createBody["texture_prompt"])
	}
	modelPath := filepath.Join(outDir, "task-1.glb")
	if data, err := os.ReadFile(modelPath); err != nil || string(data) != "glTF-binary" {
		t.Fatalf("model not saved correctly: %v", err)
	}
	if !strings.Contains(result.Output, modelPath) {
		t.Fatalf("output should mention model path, got %q", result.Output)
	}
	imageFile, _ := result.Metadata["image_file"].(map[string]interface{})
	if imageFile["path"] != filepath.Join(outDir, "task-1-preview.png") {
		t.Fatalf("expected thumbnail preview metadata, got %v", result.Metadata)
	}
}

func TestMeshyGenerate3DTextRunsPreviewThenRefine(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var modes []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/openapi/v2/text-to-3d":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mode, _ := body["mode"].(string)
			modes = append(modes, mode)
			if mode == "refine" && body["preview_task_id"] != "preview-1" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"result":"`+mode+`-1"}`)
		case strings.HasPrefix(r.URL.Path, "/openapi/v2/text-to-3d/"):
			id := strings.TrimPrefix(r.URL.Path, "/openapi/v2/text-to-3d/")
			_, _ = io.WriteString(w, `{"id":"`+id+`","status":"SUCCEEDED","model_urls":{"glb":"`+srv.URL+`/m/`+id+`.glb"}}`)
		case strings.HasPrefix(r.URL.Path, "/m/"):
			_, _ = io.WriteString(w, r.URL.Path)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	workDir := t.TempDir()
	tool := NewMeshyGenerate3DTool(newModel3DTestStore(t, "meshy", srv.URL), workDir, "")
	tool.pollInterval = time.Millisecond
	params, _ := json.Marshal(map[string]interface{}{"prompt": "a treasure chest"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil || !result.Success {
		t.Fatalf("expected success, got err=%v result=%+v", err, result)
	}
	if strings.Join(modes, ",") != "preview,refine" {
		t.Fatalf("expected preview then refine, got %v", modes)
	}
	if data, _ := os.ReadFile(filepath.Join(workDir, "generated", "meshy", "refine-1.glb")); string(data) != "/m/refine-1.glb" {
		t.Fatalf("expected refined model to be saved, got %q", data)
	}
}

func TestTripoGenerate3DUploadsLocalImage(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var createBody map[string]interface{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/files":
			file, _, err := r.FormFile("file")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = file.Close()
			_, _ = io.WriteString(w, `{"code":0,"data":{"file_token":"file_abc"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/generation/image-to-model":
			_ = json.NewDecoder(r.Body).Decode(&createBody)
			_, _ = io.WriteString(w, `{"code":0,"data":{"task_id":"task_1"}}`)
		case r.URL.Path == "/tasks/task_1":
			_, _ = io.WriteString(w, `{"code":0,"data":{"task_id":"task_1","status":"success","progress":100,"output":{"model_url":"`+srv.URL+`/out/model_pbr.glb?sig=1","rendered_image_url":"`+srv.URL+`/out/preview.webp"}}}`)
		case strings.HasPrefix(r.URL.Path, "/out/"):
			_, _ = io.WriteString(w, "data")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	workDir := t.TempDir()
	imagePath := filepath.Join(workDir, "leonardo.png")
	if err := os.WriteFile(imagePath, testPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(workDir, "generated", "tripo")
	tool := NewTripoGenerate3DTool(newModel3DTestStore(t, "tripo", srv.URL), workDir, outDir)
	tool.pollInterval = time.Millisecond
	params, _ := json.Marshal(map[string]interface{}{"image": imagePath})
	result, err := tool.Execute(context.Background(), params)
	if err != nil || !result.Success {
		t.Fatalf("expected success, got err=%v result=%+v", err, result)
	}
	if createBody["input"] != "file_abc" || createBody["model"] != tripoDefaultModel {
		t.Fatalf("expected uploaded file token and default model, got %v", createBody)
	}
	if _, err := os.Stat(filepath.Join(outDir, "task_1.glb")); err != nil {
		t.Fatalf("expected glb saved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "task_1-preview.webp")); err != nil {
		t.Fatalf("expected preview saved: %v", err)
	}
}

func TestTripoGenerate3DReportsFailedTask(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/generation/text-to-model":
			_, _ = io.WriteString(w, `{"code":0,"data":{"task_id":"task_2"}}`)
		case "/tasks/task_2":
			_, _ = io.WriteString(w, `{"code":0,"data":{"task_id":"task_2","status":"failed","error_code":2010,"error_message":"insufficient credits"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tool := NewTripoGenerate3DTool(newModel3DTestStore(t, "tripo", srv.URL), t.TempDir(), "")
	tool.pollInterval = time.Millisecond
	params, _ := json.Marshal(map[string]interface{}{"prompt": "a chair"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "insufficient credits") {
		t.Fatalf("expected failure with task error, got %+v", result)
	}
}

func TestModel3DToolsRequireIntegration(t *testing.T) {
	t.Parallel()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	params := json.RawMessage(`{"prompt":"a cube"}`)
	meshy, _ := NewMeshyGenerate3DTool(store, "", "").Execute(context.Background(), params)
	tripo, _ := NewTripoGenerate3DTool(store, "", "").Execute(context.Background(), params)
	if meshy.Success || tripo.Success {
		t.Fatal("expected failure without configured integrations")
	}
}

func TestDownloadModel3DFileSanitizesRemoteTaskID(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "model")
	}))
	defer srv.Close()
	dir := t.TempDir()
	got, err := downloadModel3DFile(context.Background(), srv.Client(), srv.URL, dir, "../../evil", ".glb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Dir(got) != dir {
		t.Fatalf("download escaped output dir: %s", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected only the final file (no leftover temp files), got %d entries", len(entries))
	}
}
