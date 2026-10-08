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

func TestLeonardoGenerateImageNanoBananaUsesV2(t *testing.T) {
	t.Parallel()

	const generationID = "aaaaaaaa-2222-3333-4444-555555555555"
	var v2Posts atomic.Int32
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" || strings.HasPrefix(r.Header.Get("User-Agent"), "Go-http-client") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	}))
	t.Cleanup(imageServer.Close)

	v2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v2Posts.Add(1)
		var payload map[string]interface{}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		if payload["model"] != "gemini-2.5-flash-image" {
			t.Fatalf("unexpected model: %v", payload["model"])
		}
		params := payload["parameters"].(map[string]interface{})
		// Nano Banana accepts 768x1152 in the current v2 dimension lists.
		if params["width"].(float64) != 768 || params["height"].(float64) != 1152 {
			t.Fatalf("sizes not snapped: %v x %v", params["width"], params["height"])
		}
		if !strings.Contains(params["prompt"].(string), "Avoid: text") {
			t.Fatalf("negative prompt not folded in: %v", params["prompt"])
		}
		_, _ = w.Write([]byte(`{"generate":{"generationId":"` + generationID + `"}}`))
	}))
	t.Cleanup(v2Server.Close)

	v1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/generations/"+generationID {
			t.Fatalf("unexpected v1 request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"generations_by_pk":{"status":"COMPLETE","generated_images":[{"url":"` + imageServer.URL + `/i.png"}]}}`))
	}))
	t.Cleanup(v1Server.Close)

	tool := NewLeonardoGenerateImageTool(newLeonardoTestStore(t, "test-key", nil), t.TempDir())
	tool.apiBaseURL = v1Server.URL
	tool.apiV2BaseURL = v2Server.URL
	tool.pollInterval = 10 * time.Millisecond

	params, _ := json.Marshal(map[string]interface{}{
		"prompt": "stained glass", "model_id": "gemini-2.5-flash-image",
		"width": 768, "height": 1152, "negative_prompt": "text",
	})
	result, err := tool.Execute(context.Background(), params)
	if err != nil || !result.Success {
		t.Fatalf("expected success, got err=%v result=%+v", err, result)
	}
	if v2Posts.Load() != 1 {
		t.Fatalf("expected one v2 create request, got %d", v2Posts.Load())
	}
}

// Leonardo v2 can return GraphQL-style error envelopes with HTTP 200.
func TestLeonardoGenerateImageCreateErrorEnvelope(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, want string
	}{
		{"credits", `[{"extensions":{"code":"HttpException","details":"An error occurred processing your request.","statusCode":402},"message":"Insufficient tokens","path":[]}]`, "Insufficient tokens"},
		{"validation", `[{"extensions":{"code":"BadRequestException","details":{"code":"VALIDATION_ERROR","message":"parameters.width must be one of: 1344, 768"},"statusCode":400},"message":"An error occurred.","path":[]}]`, "parameters.width must be one of: 1344, 768"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			tool := NewLeonardoGenerateImageTool(newLeonardoTestStore(t, "test-key", nil), t.TempDir())
			tool.apiV2BaseURL = server.URL
			params := json.RawMessage(`{"prompt":"embers","model_id":"gemini-2.5-flash-image"}`)
			result, err := tool.Execute(context.Background(), params)
			if err != nil || result.Success || !strings.Contains(result.Error, tc.want) {
				t.Fatalf("expected actionable API failure %q, got err=%v result=%+v", tc.want, err, result)
			}
			if requests.Load() != 1 {
				t.Fatalf("error envelope must not start polling: %d requests", requests.Load())
			}
		})
	}
}

func TestLeonardoNanoBananaCurrentSizes(t *testing.T) {
	t.Parallel()
	request := buildLeonardoV2Request("gemini-2.5-flash-image", "embers", LeonardoGenerateImageParams{Width: 1536, Height: 1024}, &storage.Integration{Config: map[string]string{}})
	params := request["parameters"].(map[string]interface{})
	if params["width"] != 1344 || params["height"] != 896 {
		t.Fatalf("invalid Nano Banana sizes were not snapped to current API values: %v", params)
	}
}

func TestLeonardoGenerateImageStatusErrorEnvelope(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"extensions":{"code":"HttpException","statusCode":429},"message":"Rate limit exceeded"}]`))
	}))
	defer server.Close()
	tool := NewLeonardoGenerateImageTool(nil, t.TempDir())
	tool.apiBaseURL = server.URL
	_, _, err := tool.fetchGenerationStatus(context.Background(), "test-key", "generation-id")
	if err == nil || !strings.Contains(err.Error(), "Rate limit exceeded") {
		t.Fatalf("expected immediate status error, got %v", err)
	}
}
