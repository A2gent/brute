package http

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/speechengine"
)

const (
	openRouterTranscriptionsURL = "https://openrouter.ai/api/v1/audio/transcriptions"
	openRouterSpeechURL         = "https://openrouter.ai/api/v1/audio/speech"
	openRouterSpeechTimeout     = 60 * time.Second
	openRouterDefaultTTSVoice   = "alloy"
)

var errOpenRouterAPIKeyMissing = errors.New("OpenRouter API key is not configured")

func openRouterSpeechModelID(raw string) (string, bool) {
	engine, model, ok := strings.Cut(strings.TrimSpace(raw), ":")
	if !ok || !strings.EqualFold(strings.TrimSpace(engine), "openrouter") {
		return "", false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}
	return model, true
}

func resolveOpenRouterSTT(requested string) (string, bool) {
	if model, ok := openRouterSpeechModelID(requested); ok {
		return model, true
	}
	if strings.TrimSpace(requested) != "" {
		return "", false
	}
	return openRouterSpeechModelID(os.Getenv("AAGENT_STT_ENGINE"))
}

func (s *Server) resolveOpenRouterAPIKey() string {
	if s != nil && s.config != nil {
		if provider, ok := s.config.Providers[string(config.ProviderOpenRouter)]; ok {
			if key := strings.TrimSpace(provider.APIKey); key != "" {
				return key
			}
		}
	}
	if s != nil {
		if key := s.apiKeyFromEnv(config.ProviderOpenRouter); key != "" {
			return key
		}
	}
	return strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
}

func (s *Server) openRouterDo(req *http.Request) (*http.Response, error) {
	client := openRouterModelsHTTPClient(http.DefaultClient)
	if s != nil && s.openRouterModelsClient != nil {
		client = s.openRouterModelsClient
	}
	return client.Do(req)
}

func tagLocalSpeechEngines(engines []speechengine.EngineStatus) {
	for i := range engines {
		if strings.TrimSpace(engines[i].Source) == "" {
			engines[i].Source = speechengine.SourceLocal
		}
	}
}

func (s *Server) openRouterSpeechEngines(ctx context.Context) []speechengine.EngineStatus {
	if s.resolveOpenRouterAPIKey() == "" {
		return []speechengine.EngineStatus{
			openRouterPlaceholderEngine("stt", "Add an OpenRouter API key in Providers to list cloud transcription models."),
			openRouterPlaceholderEngine("tts", "Add an OpenRouter API key in Providers to list cloud speech models."),
		}
	}

	engines := make([]speechengine.EngineStatus, 0, 8)
	engines = append(engines, s.openRouterCatalogEngines(ctx, "transcription", "stt")...)
	engines = append(engines, s.openRouterCatalogEngines(ctx, "speech", "tts")...)
	return engines
}

func (s *Server) openRouterCatalogEngines(ctx context.Context, modality, kind string) []speechengine.EngineStatus {
	models, err := fetchOpenRouterCatalog(ctx, s.openRouterModelsClient, s.resolveOpenRouterAPIKey(), modality)
	if err != nil {
		return []speechengine.EngineStatus{
			openRouterPlaceholderEngine(kind, fmt.Sprintf("Failed to list OpenRouter %s models: %s", modality, err.Error())),
		}
	}
	if len(models) == 0 {
		return []speechengine.EngineStatus{
			openRouterPlaceholderEngine(kind, fmt.Sprintf("OpenRouter returned no %s models.", modality)),
		}
	}
	out := make([]speechengine.EngineStatus, 0, len(models))
	for _, model := range models {
		label := model.Name
		if label == "" {
			label = model.ID
		}
		out = append(out, speechengine.EngineStatus{
			ID:           "openrouter:" + model.ID,
			Label:        label,
			Kind:         kind,
			Model:        model.ID,
			RuntimeID:    "openrouter",
			Detail:       "OpenRouter cloud model",
			Supported:    true,
			RuntimeReady: true,
			Source:       speechengine.SourceCloud,
		})
	}
	return out
}

func openRouterPlaceholderEngine(kind, detail string) speechengine.EngineStatus {
	id := "openrouter"
	if kind == "tts" {
		id = "openrouter_tts"
	}
	return speechengine.EngineStatus{
		ID:           id,
		Label:        "OpenRouter",
		Kind:         kind,
		RuntimeID:    "openrouter",
		Detail:       detail,
		Supported:    true,
		RuntimeReady: false,
		Source:       speechengine.SourceCloud,
	}
}

func (s *Server) transcribePreparedAudio(ctx context.Context, requestedEngine, audioPath, filename string, opts speechengine.TranscribeOptions) (string, error) {
	if model, ok := resolveOpenRouterSTT(requestedEngine); ok {
		payload, err := os.ReadFile(audioPath)
		if err != nil {
			return "", fmt.Errorf("failed to read audio for OpenRouter: %w", err)
		}
		return s.transcribeOpenRouter(ctx, model, payload, filename, opts.Language)
	}
	return speechengine.TranscribeSelected(ctx, requestedEngine, audioPath, opts)
}

func (s *Server) transcribeOpenRouter(ctx context.Context, model string, audio []byte, filename, language string) (string, error) {
	apiKey := s.resolveOpenRouterAPIKey()
	if apiKey == "" {
		return "", errOpenRouterAPIKeyMissing
	}
	requestCtx, cancel := context.WithTimeout(ctx, openRouterSpeechTimeout)
	defer cancel()

	payload := map[string]any{
		"model": model,
		"input_audio": map[string]string{
			"data":   base64.StdEncoding.EncodeToString(audio),
			"format": openRouterAudioFormat(filename),
		},
	}
	if lang := normalizeOpenRouterLanguage(language); lang != "" {
		payload["language"] = lang
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, openRouterTranscriptionsURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.openRouterDo(req)
	if err != nil {
		return "", fmt.Errorf("failed to reach OpenRouter transcription: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("failed to read OpenRouter transcription response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("OpenRouter transcription failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse OpenRouter transcription response: %w", err)
	}
	text := strings.TrimSpace(parsed.Text)
	if text == "" {
		return "", fmt.Errorf("OpenRouter returned an empty transcript")
	}
	return text, nil
}

func (s *Server) synthesizeOpenRouter(ctx context.Context, model, text string) ([]byte, string, error) {
	apiKey := s.resolveOpenRouterAPIKey()
	if apiKey == "" {
		return nil, "", errOpenRouterAPIKeyMissing
	}
	requestCtx, cancel := context.WithTimeout(ctx, openRouterSpeechTimeout)
	defer cancel()

	body, err := json.Marshal(map[string]any{
		"model":           model,
		"input":           text,
		"voice":           openRouterDefaultTTSVoice,
		"response_format": "mp3",
	})
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, openRouterSpeechURL, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg")

	resp, err := s.openRouterDo(req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to reach OpenRouter speech: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read OpenRouter speech response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("OpenRouter speech failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if len(respBody) == 0 {
		return nil, "", fmt.Errorf("OpenRouter returned empty speech audio")
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "audio/mpeg"
	}
	return respBody, contentType, nil
}

func normalizeOpenRouterLanguage(raw string) string {
	lang := strings.ToLower(strings.TrimSpace(strings.Split(raw, "-")[0]))
	if lang == "" || lang == "auto" {
		return ""
	}
	return lang
}

func openRouterAudioFormat(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".wav", ".wave":
		return "wav"
	case ".mp3", ".mpeg", ".mpga":
		return "mp3"
	case ".flac":
		return "flac"
	case ".m4a", ".mp4":
		return "m4a"
	case ".ogg", ".oga", ".opus":
		return "ogg"
	case ".webm":
		return "webm"
	case ".aac":
		return "aac"
	default:
		return "wav"
	}
}
