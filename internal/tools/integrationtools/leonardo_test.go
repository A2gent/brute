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

func newLeonardoTestStore(t *testing.T, apiKey string, extras map[string]string) storage.Store {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := map[string]string{
		"api_key": apiKey,
	}
	for k, v := range extras {
		cfg[k] = v
	}
	now := time.Now().UTC()
	if err := store.SaveIntegration(&storage.Integration{
		ID:        "leonardo-1",
		Provider:  "leonardo",
		Name:      "Leonardo Test",
		Mode:      "notify_only",
		Enabled:   true,
		Config:    cfg,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("failed to save leonardo integration: %v", err)
	}
	return store
}

func TestLeonardoGenerateImageToolNameAndSchema(t *testing.T) {
	t.Parallel()
	tool := NewLeonardoGenerateImageTool(nil, "")
	if tool.Name() != "leonardo_generate_image" {
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
}

func TestLeonardoGenerateImageEmptyPrompt(t *testing.T) {
	t.Parallel()
	tool := NewLeonardoGenerateImageTool(newLeonardoTestStore(t, "test-key", nil), t.TempDir())
	params, _ := json.Marshal(map[string]string{"prompt": "  "})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for empty prompt")
	}
}

func TestLeonardoGenerateImageMissingIntegration(t *testing.T) {
	t.Parallel()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tool := NewLeonardoGenerateImageTool(store, t.TempDir())
	params, _ := json.Marshal(map[string]string{"prompt": "a cat"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure without leonardo integration")
	}
}

func TestLeonardoGenerateImageSuccess(t *testing.T) {
	t.Parallel()

	const generationID = "11111111-2222-3333-4444-555555555555"
	var createPosts atomic.Int32
	var statusGets atomic.Int32
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02}

	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	t.Cleanup(imageServer.Close)

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/generations":
			createPosts.Add(1)
			body, _ := io.ReadAll(r.Body)
			var payload map[string]interface{}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("invalid create payload: %v", err)
			}
			if payload["prompt"] != "a red balloon" {
				t.Fatalf("unexpected prompt: %v", payload["prompt"])
			}
			if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
				t.Fatalf("unexpected auth header: %q", auth)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"sdGenerationJob":{"generationId":"` + generationID + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/generations/"+generationID:
			statusGets.Add(1)
			if statusGets.Load() < 2 {
				_, _ = w.Write([]byte(`{"generations_by_pk":{"status":"PENDING","generated_images":[]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"generations_by_pk":{"status":"COMPLETE","generated_images":[{"url":"` + imageServer.URL + `/image.png"}]}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(apiServer.Close)

	store := newLeonardoTestStore(t, "test-key", nil)
	outDir := t.TempDir()
	tool := NewLeonardoGenerateImageTool(store, outDir)
	tool.apiBaseURL = apiServer.URL
	tool.pollInterval = 10 * time.Millisecond
	tool.client = apiServer.Client()

	params, _ := json.Marshal(map[string]string{"prompt": "a red balloon"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if createPosts.Load() != 1 {
		t.Fatalf("expected one create request, got %d", createPosts.Load())
	}
	if statusGets.Load() < 2 {
		t.Fatalf("expected at least two status polls, got %d", statusGets.Load())
	}
	if !strings.Contains(result.Output, generationID) {
		t.Fatalf("expected generation id in output, got %q", result.Output)
	}
	imageFile, ok := result.Metadata["image_file"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected image_file metadata, got %#v", result.Metadata)
	}
	path, _ := imageFile["path"].(string)
	if path == "" {
		t.Fatal("expected image path in metadata")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read saved image: %v", err)
	}
	if string(data) != string(pngBytes) {
		t.Fatal("saved image bytes mismatch")
	}
	if !strings.HasPrefix(path, outDir) {
		t.Fatalf("expected image under output dir, got %s", path)
	}
	if filepath.Base(path) != generationID+"-1.png" {
		t.Fatalf("unexpected image filename: %s", filepath.Base(path))
	}
}

func TestLeonardoGenerateImageFailedStatus(t *testing.T) {
	t.Parallel()

	const generationID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/generations":
			_, _ = w.Write([]byte(`{"sdGenerationJob":{"generationId":"` + generationID + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/generations/"+generationID:
			_, _ = w.Write([]byte(`{"generations_by_pk":{"status":"FAILED","message":"content policy violation"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(apiServer.Close)

	store := newLeonardoTestStore(t, "test-key", nil)
	tool := NewLeonardoGenerateImageTool(store, t.TempDir())
	tool.apiBaseURL = apiServer.URL
	tool.pollInterval = 10 * time.Millisecond
	tool.client = apiServer.Client()

	params, _ := json.Marshal(map[string]string{"prompt": "bad prompt"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for FAILED status")
	}
	if !strings.Contains(result.Error, "content policy violation") {
		t.Fatalf("expected failure detail, got %q", result.Error)
	}
}

func TestExtractLeonardoGenerationID(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"sdGenerationJob":{"generationId":"abc-def-ghi"}}`)
	if got := extractLeonardoGenerationID(raw); got != "abc-def-ghi" {
		t.Fatalf("unexpected generation id: %q", got)
	}
}

func TestExtractLeonardoImageURLs(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"generations_by_pk":{"generated_images":[{"url":"https://example.com/a.png"}]}}`)
	urls := extractLeonardoImageURLs(raw)
	if len(urls) != 1 || urls[0] != "https://example.com/a.png" {
		t.Fatalf("unexpected urls: %#v", urls)
	}
}
