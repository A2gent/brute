package integrationtools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func newTestOpenAIConfig(apiKey, baseURL string) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{
		APIKey:  apiKey,
		BaseURL: baseURL,
	}
	return cfg
}

func TestOpenAIGenerateImageToolSchema(t *testing.T) {
	t.Parallel()
	tool := NewOpenAIGenerateImageTool(nil, "")

	schema := tool.Schema()
	if schema["type"] != "object" {
		t.Errorf("expected schema type 'object', got %v", schema["type"])
	}
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema properties should be a map")
	}
	if _, ok := props["prompt"]; !ok {
		t.Error("schema should have 'prompt' property")
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("schema required should be []string")
	}
	found := false
	for _, r := range required {
		if r == "prompt" {
			found = true
		}
	}
	if !found {
		t.Error("prompt should be required")
	}
}

func TestOpenAIGenerateImageToolName(t *testing.T) {
	t.Parallel()
	tool := NewOpenAIGenerateImageTool(nil, "")
	if tool.Name() != "openai_generate_image" {
		t.Errorf("unexpected tool name: %s", tool.Name())
	}
}

func TestOpenAIGenerateImageEmptyPrompt(t *testing.T) {
	t.Parallel()
	tool := NewOpenAIGenerateImageTool(newTestOpenAIConfig("sk-test", ""), "")
	params, _ := json.Marshal(map[string]string{"prompt": ""})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for empty prompt")
	}
	if !strings.Contains(result.Error, "prompt") {
		t.Errorf("expected prompt error, got: %s", result.Error)
	}
}

func TestOpenAIGenerateImageMissingConfig(t *testing.T) {
	t.Parallel()
	tool := NewOpenAIGenerateImageTool(nil, "")
	params, _ := json.Marshal(map[string]string{"prompt": "a cat"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when config is nil")
	}
	if !strings.Contains(result.Error, "openai provider") {
		t.Errorf("expected config error, got: %s", result.Error)
	}
}

func TestOpenAIGenerateImageMissingAPIKey(t *testing.T) {
	t.Parallel()
	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{BaseURL: "https://api.openai.com/v1"}
	tool := NewOpenAIGenerateImageTool(cfg, "")
	params, _ := json.Marshal(map[string]string{"prompt": "a cat"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when api_key is empty")
	}
	if !strings.Contains(result.Error, "api_key") {
		t.Errorf("expected api_key error, got: %s", result.Error)
	}
}

func TestOpenAIGenerateImageHTTPError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key","type":"invalid_request_error","code":"invalid_api_key"}}`))
	}))
	defer srv.Close()

	tool := NewOpenAIGenerateImageTool(newTestOpenAIConfig("bad-key", srv.URL), "")
	params, _ := json.Marshal(map[string]string{"prompt": "a cat"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure on HTTP error")
	}
	if !strings.Contains(result.Error, "Invalid API key") {
		t.Errorf("expected Invalid API key error, got: %s", result.Error)
	}
}

func TestOpenAIGenerateImageAPIErrorInBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":{"message":"content policy violation","type":"invalid_request_error","code":"content_policy_violation"}}`))
	}))
	defer srv.Close()

	tool := NewOpenAIGenerateImageTool(newTestOpenAIConfig("sk-ok", srv.URL), "")
	params, _ := json.Marshal(map[string]string{"prompt": "bad content"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure on API error in body")
	}
	if !strings.Contains(result.Error, "content policy") {
		t.Errorf("expected content policy error, got: %s", result.Error)
	}
}

func TestOpenAIGenerateImageSuccess(t *testing.T) {
	t.Parallel()

	pngBase64 := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "images/generations") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("expected Bearer token, got: %s", auth)
		}

		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}
		if strings.TrimSpace(reqBody["prompt"].(string)) == "" {
			t.Error("expected non-empty prompt in request")
		}
		if _, ok := reqBody["response_format"]; ok {
			t.Error("did not expect response_format in request")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(openAIImagesResponse{
			Created: 1234567890,
			Data:    []openAIImageDatum{{B64JSON: pngBase64}},
		})
	}))
	defer srv.Close()

	outDir := t.TempDir()
	tool := NewOpenAIGenerateImageTool(newTestOpenAIConfig("sk-ok", srv.URL), outDir)
	params, _ := json.Marshal(map[string]string{"prompt": "a friendly cat"})

	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	imageFile, ok := result.Metadata["image_file"].(map[string]interface{})
	if !ok {
		t.Fatal("expected image_file in metadata")
	}
	path, ok := imageFile["path"].(string)
	if !ok || path == "" {
		t.Fatal("expected non-empty image path in metadata")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("image file not found at path %s: %v", path, err)
	}
	sourceTool, ok := imageFile["source_tool"].(string)
	if !ok || sourceTool != "openai_generate_image" {
		t.Errorf("expected source_tool=openai_generate_image, got: %v", imageFile["source_tool"])
	}
	if !strings.HasPrefix(filepath.ToSlash(path), filepath.ToSlash(outDir)) {
		t.Errorf("image saved outside output dir: %s", path)
	}
}

func TestOpenAIGenerateImageRequestBuilding(t *testing.T) {
	t.Parallel()

	var capturedReq map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedReq)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(openAIImagesResponse{Data: []openAIImageDatum{{B64JSON: "ZmFrZQ=="}}})
	}))
	defer srv.Close()

	tool := NewOpenAIGenerateImageTool(newTestOpenAIConfig("sk-ok", srv.URL), t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"prompt":  "a sunset",
		"model":   "dall-e-3",
		"size":    "1792x1024",
		"quality": "hd",
		"n":       1,
	})
	_, _ = tool.Execute(context.Background(), params)

	if capturedReq["model"] != "dall-e-3" {
		t.Errorf("expected model dall-e-3, got: %v", capturedReq["model"])
	}
	if capturedReq["size"] != "1792x1024" {
		t.Errorf("expected size 1792x1024, got: %v", capturedReq["size"])
	}
	if capturedReq["quality"] != "hd" {
		t.Errorf("expected quality hd, got: %v", capturedReq["quality"])
	}
	if _, ok := capturedReq["response_format"]; ok {
		t.Errorf("did not expect response_format in request: %#v", capturedReq)
	}
}

func TestOpenAIGenerateImageURLResponse(t *testing.T) {
	t.Parallel()

	pngBytes := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/images/generations":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(openAIImagesResponse{
				Data: []openAIImageDatum{{URL: srv.URL + "/remote/image.png"}},
			})
		case "/remote/image.png":
			w.Header().Set("Content-Type", "image/png")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(pngBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tool := NewOpenAIGenerateImageTool(newTestOpenAIConfig("sk-ok", srv.URL), t.TempDir())
	params, _ := json.Marshal(map[string]string{"prompt": "a cat in a garden"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}

	imageFile, ok := result.Metadata["image_file"].(map[string]interface{})
	if !ok {
		t.Fatal("expected image_file metadata for URL response")
	}
	path, ok := imageFile["path"].(string)
	if !ok || path == "" {
		t.Fatal("expected downloaded image path")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected downloaded image to exist: %v", err)
	}

	openAIImages, ok := result.Metadata["openai_images"].(map[string]interface{})
	if !ok {
		t.Fatal("expected openai_images metadata")
	}
	urls, ok := openAIImages["urls"].([]string)
	if !ok {
		t.Fatalf("expected urls metadata as []string, got: %T", openAIImages["urls"])
	}
	if len(urls) != 1 || urls[0] != srv.URL+"/remote/image.png" {
		t.Fatalf("unexpected urls metadata: %#v", urls)
	}
}

func TestOpenAIGenerateImageEmptyDataResponse(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"created":123,"data":[]}`))
	}))
	defer srv.Close()

	tool := NewOpenAIGenerateImageTool(newTestOpenAIConfig("sk-ok", srv.URL), t.TempDir())
	params, _ := json.Marshal(map[string]string{"prompt": "a cat"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure when data array is empty")
	}
}
