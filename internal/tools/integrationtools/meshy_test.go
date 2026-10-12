package integrationtools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func newMeshyTestStore(t *testing.T, config map[string]string) storage.Store {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	if err := store.SaveIntegration(&storage.Integration{
		ID: "meshy-1", Provider: "meshy", Name: "meshy", Mode: "notify_only", Enabled: true,
		Config: config, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("failed to save integration: %v", err)
	}
	return store
}

// A slow Meshy task must not hold the tool call past the MCP client timeout:
// the tool returns a resumable task_id and a second call picks it up without
// creating (and paying for) a new task.
func TestMeshyGenerate3DReturnsPendingAndResumes(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	creates := 0
	done := false
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/openapi/v1/image-to-3d":
			creates++
			_, _ = io.WriteString(w, `{"result":"task-1"}`)
		case r.URL.Path == "/openapi/v1/image-to-3d/task-1":
			if !done {
				_, _ = io.WriteString(w, `{"id":"task-1","status":"IN_PROGRESS","progress":40}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":"task-1","status":"SUCCEEDED","model_urls":{"glb":"`+srv.URL+`/files/model.glb"}}`)
		case r.URL.Path == "/files/model.glb":
			_, _ = io.WriteString(w, "glTF-binary")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tool := NewMeshyGenerate3DTool(newMeshyTestStore(t, map[string]string{"api_key": "k", "base_url": srv.URL}), t.TempDir(), "")
	tool.pollInterval = time.Millisecond
	tool.maxWait = 30 * time.Millisecond

	params, _ := json.Marshal(map[string]interface{}{"image": "https://example.com/a.png"})
	first, err := tool.Execute(context.Background(), params)
	if err != nil || !first.Success {
		t.Fatalf("expected pending success, got err=%v result=%+v", err, first)
	}
	if !strings.Contains(first.Output, "task_id=image-to-3d:task-1") || !strings.Contains(first.Output, "40%") {
		t.Fatalf("expected resumable task id and progress in output, got %q", first.Output)
	}
	if first.Metadata["model_3d_pending"] == nil {
		t.Fatalf("expected pending metadata, got %v", first.Metadata)
	}

	mu.Lock()
	done = true
	mu.Unlock()
	resume, _ := json.Marshal(map[string]interface{}{"task_id": "image-to-3d:task-1"})
	second, err := tool.Execute(context.Background(), resume)
	if err != nil || !second.Success || second.Metadata["model_3d"] == nil {
		t.Fatalf("expected resumed task to finish, got err=%v result=%+v", err, second)
	}
	if creates != 1 {
		t.Fatalf("resume must not create a new task, creates=%d", creates)
	}
}

// Resuming a finished text-to-3D preview must still run the texturing refine.
func TestMeshyGenerate3DResumePreviewRunsRefine(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var refineBody map[string]interface{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/openapi/v2/text-to-3d":
			_ = json.NewDecoder(r.Body).Decode(&refineBody)
			_, _ = io.WriteString(w, `{"result":"refine-1"}`)
		case strings.HasPrefix(r.URL.Path, "/openapi/v2/text-to-3d/"):
			id := strings.TrimPrefix(r.URL.Path, "/openapi/v2/text-to-3d/")
			_, _ = io.WriteString(w, `{"id":"`+id+`","status":"SUCCEEDED","model_urls":{"glb":"`+srv.URL+`/m/`+id+`.glb"}}`)
		case strings.HasPrefix(r.URL.Path, "/m/"):
			_, _ = io.WriteString(w, "glb")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tool := NewMeshyGenerate3DTool(newMeshyTestStore(t, map[string]string{"api_key": "k", "base_url": srv.URL}), t.TempDir(), "")
	tool.pollInterval = time.Millisecond
	params, _ := json.Marshal(map[string]interface{}{"task_id": "text-to-3d-preview:preview-1"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil || !result.Success {
		t.Fatalf("expected success, got err=%v result=%+v", err, result)
	}
	if refineBody["mode"] != "refine" || refineBody["preview_task_id"] != "preview-1" {
		t.Fatalf("expected refine of preview-1, got %v", refineBody)
	}
	if !strings.Contains(result.Output, "refine-1") {
		t.Fatalf("expected refined task in output, got %q", result.Output)
	}
}

func TestMeshyGenerate3DAppliesQualityDefaults(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		config map[string]string
		params map[string]interface{}
		want   map[string]interface{}
	}{
		{
			name:   "smart topology from integration",
			config: map[string]string{"model": "latest", "model_type": "smart-topology", "target_polycount": "5000"},
			params: map[string]interface{}{},
			want:   map[string]interface{}{"model_type": "smart-topology", "ai_model": "meshy-t2", "target_polycount": float64(5000)},
		},
		{
			name:   "standard remesh from integration",
			config: map[string]string{"target_polycount": "20000"},
			params: map[string]interface{}{},
			want:   map[string]interface{}{"should_remesh": true, "target_polycount": float64(20000)},
		},
		{
			name:   "call overrides integration",
			config: map[string]string{"target_polycount": "20000"},
			params: map[string]interface{}{"target_polycount": 3000},
			want:   map[string]interface{}{"should_remesh": true, "target_polycount": float64(3000)},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var body map[string]interface{}
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case r.Method == http.MethodPost:
					_ = json.NewDecoder(r.Body).Decode(&body)
					_, _ = io.WriteString(w, `{"result":"t"}`)
				case r.URL.Path == "/openapi/v1/image-to-3d/t":
					_, _ = io.WriteString(w, `{"id":"t","status":"SUCCEEDED","model_urls":{"glb":"`+srv.URL+`/m.glb"}}`)
				default:
					_, _ = io.WriteString(w, "glb")
				}
			}))
			defer srv.Close()

			config := map[string]string{"api_key": "k", "base_url": srv.URL}
			for k, v := range tc.config {
				config[k] = v
			}
			tool := NewMeshyGenerate3DTool(newMeshyTestStore(t, config), t.TempDir(), "")
			tool.pollInterval = time.Millisecond
			tc.params["image"] = "https://example.com/a.png"
			params, _ := json.Marshal(tc.params)
			if result, err := tool.Execute(context.Background(), params); err != nil || !result.Success {
				t.Fatalf("expected success, got err=%v result=%+v", err, result)
			}
			for k, v := range tc.want {
				if body[k] != v {
					t.Fatalf("%s = %v, want %v (body %v)", k, body[k], v, body)
				}
			}
		})
	}
}
