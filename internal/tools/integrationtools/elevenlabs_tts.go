package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const (
	elevenLabsTTSModelDefault = "eleven_multilingual_v2"
	elevenLabsTTSBaseURL      = "https://api.elevenlabs.io/v1/text-to-speech"
)

type ElevenLabsTTSTool struct {
	store     storage.Store
	clipStore *speechcache.Store
	client    *http.Client
}

type ElevenLabsTTSParams struct {
	Text            string  `json:"text"`
	IntegrationID   string  `json:"integration_id,omitempty"`
	IntegrationName string  `json:"integration_name,omitempty"`
	VoiceID         string  `json:"voice_id,omitempty"`
	Speed           float64 `json:"speed,omitempty"`
	ModelID         string  `json:"model_id,omitempty"`
}

type elevenLabsToolTTSRequest struct {
	Text          string                     `json:"text"`
	ModelID       string                     `json:"model_id"`
	VoiceSettings *elevenLabsToolVoiceConfig `json:"voice_settings,omitempty"`
}

type elevenLabsToolVoiceConfig struct {
	Speed float64 `json:"speed,omitempty"`
}

func NewElevenLabsTTSTool(store storage.Store, clipStore *speechcache.Store) *ElevenLabsTTSTool {
	return &ElevenLabsTTSTool{
		store:     store,
		clipStore: clipStore,
		client: &http.Client{
			Timeout: 45 * time.Second,
		},
	}
}

func (t *ElevenLabsTTSTool) Name() string {
	return "elevenlabs_tts"
}

func (t *ElevenLabsTTSTool) Description() string {
	return "Generate speech audio from text using an enabled ElevenLabs integration and return a clip ID for web playback."
}

func (t *ElevenLabsTTSTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Text to synthesize as speech.",
			},
			"integration_id": map[string]interface{}{
				"type":        "string",
				"description": "Specific ElevenLabs integration ID (optional).",
			},
			"integration_name": map[string]interface{}{
				"type":        "string",
				"description": "Specific ElevenLabs integration name (optional).",
			},
			"voice_id": map[string]interface{}{
				"type":        "string",
				"description": "ElevenLabs voice ID. Defaults to ELEVENLABS_VOICE_ID setting/env.",
			},
			"speed": map[string]interface{}{
				"type":        "number",
				"description": "Optional speaking speed multiplier (>0).",
			},
			"model_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional ElevenLabs model ID. Defaults to eleven_multilingual_v2.",
			},
		},
		"required": []string{"text"},
	}
}

func (t *ElevenLabsTTSTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p ElevenLabsTTSParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	text := strings.TrimSpace(p.Text)
	if text == "" {
		return &tools.Result{Success: false, Error: "text is required"}, nil
	}

	integration, err := t.selectIntegration(p.IntegrationID, p.IntegrationName)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	apiKey := strings.TrimSpace(integration.Config["api_key"])
	if apiKey == "" {
		return &tools.Result{Success: false, Error: "selected elevenlabs integration is missing api_key"}, nil
	}

	settings, _ := t.store.GetSettings()

	voiceID := strings.TrimSpace(p.VoiceID)
	if voiceID == "" {
		voiceID = strings.TrimSpace(settings["ELEVENLABS_VOICE_ID"])
	}
	if voiceID == "" {
		return &tools.Result{Success: false, Error: "voice_id is required (set ELEVENLABS_VOICE_ID in Skills or pass voice_id)"}, nil
	}

	modelID := strings.TrimSpace(p.ModelID)
	if modelID == "" {
		modelID = elevenLabsTTSModelDefault
	}

	speed := p.Speed
	if speed <= 0 {
		if raw := strings.TrimSpace(settings["ELEVENLABS_SPEED"]); raw != "" {
			if parsed, parseErr := strconv.ParseFloat(raw, 64); parseErr == nil && parsed > 0 {
				speed = parsed
			}
		}
	}

	reqPayload := elevenLabsToolTTSRequest{
		Text:    text,
		ModelID: modelID,
	}
	if speed > 0 {
		reqPayload.VoiceSettings = &elevenLabsToolVoiceConfig{Speed: speed}
	}
	body, err := json.Marshal(reqPayload)
	if err != nil {
		return &tools.Result{Success: false, Error: "failed to encode ElevenLabs request"}, nil
	}

	ttsURL := fmt.Sprintf("%s/%s", elevenLabsTTSBaseURL, url.PathEscape(voiceID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ttsURL, bytes.NewReader(body))
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to build ElevenLabs request: %v", err)}, nil
	}
	req.Header.Set("xi-api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg")

	resp, err := t.client.Do(req)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("ElevenLabs request failed: %v", err)}, nil
	}
	defer resp.Body.Close()

	audio, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to read ElevenLabs response: %v", err)}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(audio))
		if detail == "" {
			detail = resp.Status
		}
		return &tools.Result{
			Success: false,
			Error:   fmt.Sprintf("ElevenLabs API error (status %d): %s", resp.StatusCode, detail),
		}, nil
	}
	if len(audio) == 0 {
		return &tools.Result{Success: false, Error: "ElevenLabs returned an empty audio payload"}, nil
	}

	if t.clipStore == nil {
		return &tools.Result{Success: false, Error: "speech clip cache is unavailable"}, nil
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "audio/mpeg"
	}
	clipID := t.clipStore.Save(contentType, audio)
	if clipID == "" {
		return &tools.Result{Success: false, Error: "failed to cache generated speech clip"}, nil
	}

	output := fmt.Sprintf("Generated ElevenLabs speech clip.\nA2_AUDIO_CLIP_ID:%s\nText:%s", clipID, text)
	return &tools.Result{Success: true, Output: output}, nil
}

func (t *ElevenLabsTTSTool) selectIntegration(integrationID string, integrationName string) (*storage.Integration, error) {
	all, err := t.store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item.Provider == "elevenlabs" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled elevenlabs integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("elevenlabs integration with id %q not found or disabled", id)
	}

	if name := strings.ToLower(strings.TrimSpace(integrationName)); name != "" {
		var matched []*storage.Integration
		for _, item := range candidates {
			if strings.ToLower(strings.TrimSpace(item.Name)) == name {
				matched = append(matched, item)
			}
		}
		if len(matched) == 1 {
			return matched[0], nil
		}
		if len(matched) > 1 {
			return nil, fmt.Errorf("multiple elevenlabs integrations matched name %q; pass integration_id", integrationName)
		}
		return nil, fmt.Errorf("elevenlabs integration named %q not found", integrationName)
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple elevenlabs integrations are enabled; pass integration_id or integration_name")
}

var _ tools.Tool = (*ElevenLabsTTSTool)(nil)
