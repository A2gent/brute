package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/speechengine"
)

func TestFetchOpenRouterCatalogFiltersByOutputModality(t *testing.T) {
	var requested string
	client := openRouterModelsDoFunc(func(req *http.Request) (*http.Response, error) {
		requested = req.URL.String()
		if req.URL.Query().Get("output_modalities") != "transcription" {
			t.Fatalf("output_modalities = %q", req.URL.Query().Get("output_modalities"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"data": [
					{"id": "openai/whisper-1", "name": "OpenAI: Whisper"},
					{"id": " openai/whisper-large-v3 ", "name": "Whisper Large V3"},
					{"id": ""}
				]
			}`)),
			Request: req,
		}, nil
	})

	models, err := fetchOpenRouterCatalog(t.Context(), client, "test-key", "transcription")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(requested, "output_modalities=transcription") {
		t.Fatalf("catalog URL = %q", requested)
	}
	if len(models) != 2 || models[0].ID != "openai/whisper-1" || models[0].Name != "OpenAI: Whisper" {
		t.Fatalf("models = %+v", models)
	}
}

func TestSpeechRuntimeAttachesOpenRouterCloudEngines(t *testing.T) {
	client := openRouterModelsDoFunc(func(req *http.Request) (*http.Response, error) {
		modality := req.URL.Query().Get("output_modalities")
		body := `{"data":[{"id":"openai/whisper-1","name":"Whisper"}]}`
		if modality == "speech" {
			body = `{"data":[{"id":"openai/gpt-4o-mini-tts-2025-12-15","name":"GPT-4o Mini TTS"}]}`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})

	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenRouter)] = config.Provider{APIKey: "test-openrouter-key"}
	server := &Server{
		config:                 cfg,
		openRouterModelsClient: client,
		inspectSpeechRuntime: func(context.Context) speechengine.RuntimeStatus {
			return speechengine.RuntimeStatus{
				OS:   "darwin",
				Arch: "arm64",
				Engines: []speechengine.EngineStatus{{
					ID: "parakeet", Label: "Parakeet", Kind: "stt",
				}},
			}
		},
	}

	rec := httptest.NewRecorder()
	server.handleSpeechRuntime(rec, httptest.NewRequest(http.MethodGet, "/speech/runtime", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var status speechengine.RuntimeStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	byID := map[string]speechengine.EngineStatus{}
	for _, engine := range status.Engines {
		byID[engine.ID] = engine
	}
	if got := byID["parakeet"]; got.Source != speechengine.SourceLocal {
		t.Fatalf("local source = %+v", got)
	}
	stt := byID["openrouter:openai/whisper-1"]
	if stt.Kind != "stt" || stt.Source != speechengine.SourceCloud || !stt.RuntimeReady || stt.Model != "openai/whisper-1" {
		t.Fatalf("cloud STT = %+v", stt)
	}
	tts := byID["openrouter:openai/gpt-4o-mini-tts-2025-12-15"]
	if tts.Kind != "tts" || tts.Source != speechengine.SourceCloud || !tts.RuntimeReady {
		t.Fatalf("cloud TTS = %+v", tts)
	}
}

func TestSpeechRuntimeCloudPlaceholderWithoutAPIKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	var requested bool
	client := openRouterModelsDoFunc(func(req *http.Request) (*http.Response, error) {
		requested = true
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Request: req}, nil
	})
	server := &Server{
		config:                 config.DefaultConfig(),
		openRouterModelsClient: client,
		inspectSpeechRuntime: func(context.Context) speechengine.RuntimeStatus {
			return speechengine.RuntimeStatus{}
		},
	}

	rec := httptest.NewRecorder()
	server.handleSpeechRuntime(rec, httptest.NewRequest(http.MethodGet, "/speech/runtime", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if requested {
		t.Fatal("catalog should not be fetched without an API key")
	}

	var status speechengine.RuntimeStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Engines) != 2 {
		t.Fatalf("engines = %+v", status.Engines)
	}
	for _, engine := range status.Engines {
		if engine.Source != speechengine.SourceCloud || engine.RuntimeReady || engine.Model != "" {
			t.Fatalf("placeholder = %+v", engine)
		}
	}
}

func TestParseSpeechModelOpenRouter(t *testing.T) {
	engine, model, err := parseSpeechModel("openrouter:openai/gpt-4o-mini-tts-2025-12-15")
	if err != nil || engine != "openrouter" || model != "openai/gpt-4o-mini-tts-2025-12-15" {
		t.Fatalf("got %q %q %v", engine, model, err)
	}
	if _, _, err := parseSpeechModel("openrouter:"); err == nil {
		t.Fatal("expected empty OpenRouter model to fail")
	}
}

func TestTranscribeSpeechUsesOpenRouter(t *testing.T) {
	var gotBody map[string]any
	client := openRouterModelsDoFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/v1/audio/transcriptions" {
			t.Fatalf("path = %s", req.URL.Path)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer test-openrouter-key" {
			t.Fatalf("Authorization = %q", got)
		}
		if err := json.NewDecoder(req.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"text":"cloud transcript"}`)),
			Request:    req,
		}, nil
	})
	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenRouter)] = config.Provider{APIKey: "test-openrouter-key"}
	server := &Server{config: cfg, openRouterModelsClient: client}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("audio", "sample.wav")
	_, _ = part.Write([]byte("RIFF"))
	_ = writer.WriteField("engine", "openrouter:openai/whisper-1")
	_ = writer.WriteField("language", "en")
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/speech/transcribe", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	server.handleTranscribeSpeech(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cloud transcript") {
		t.Fatal(rec.Body.String())
	}
	if gotBody["model"] != "openai/whisper-1" {
		t.Fatalf("model = %#v", gotBody["model"])
	}
}

func TestCompletionSpeechUsesOpenRouter(t *testing.T) {
	var gotBody map[string]any
	client := openRouterModelsDoFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/v1/audio/speech" {
			t.Fatalf("path = %s", req.URL.Path)
		}
		if err := json.NewDecoder(req.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"audio/mpeg"}},
			Body:       io.NopCloser(strings.NewReader("cloud-audio")),
			Request:    req,
		}, nil
	})
	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenRouter)] = config.Provider{APIKey: "test-openrouter-key"}
	server := &Server{config: cfg, openRouterModelsClient: client}

	rec := httptest.NewRecorder()
	server.handleCompletionSpeech(rec, httptest.NewRequest(http.MethodPost, "/speech/completion", strings.NewReader(
		`{"text":"hello from cloud","language":"en","model":"openrouter:openai/gpt-4o-mini-tts-2025-12-15"}`,
	)))
	if rec.Code != http.StatusOK || rec.Body.String() != "cloud-audio" {
		t.Fatalf("%d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
	}
	if gotBody["model"] != "openai/gpt-4o-mini-tts-2025-12-15" || gotBody["input"] != "hello from cloud" {
		t.Fatalf("body = %#v", gotBody)
	}
}

func TestListSpeechModelsIncludesOpenRouterCloud(t *testing.T) {
	client := openRouterModelsDoFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("output_modalities") != "speech" {
			t.Fatalf("output_modalities = %q", req.URL.Query().Get("output_modalities"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"openai/gpt-4o-mini-tts-2025-12-15","name":"GPT-4o Mini TTS"}]}`)),
			Request:    req,
		}, nil
	})
	cfg := config.DefaultConfig()
	cfg.Providers[string(config.ProviderOpenRouter)] = config.Provider{APIKey: "test-openrouter-key"}
	server := &Server{config: cfg, openRouterModelsClient: client}

	rec := httptest.NewRecorder()
	server.handleListSpeechModels(rec, httptest.NewRequest(http.MethodGet, "/speech/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d: %s", rec.Code, rec.Body.String())
	}
	var payload speechModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	var cloud speechModel
	for _, model := range payload.Models {
		if model.ID == "openrouter:openai/gpt-4o-mini-tts-2025-12-15" {
			cloud = model
		}
		if model.ID == "auto" && model.Source != speechengine.SourceLocal {
			t.Fatalf("auto source = %+v", model)
		}
	}
	if cloud.Engine != "openrouter" || cloud.Source != speechengine.SourceCloud || !cloud.Available {
		t.Fatalf("cloud model = %+v", cloud)
	}
}
