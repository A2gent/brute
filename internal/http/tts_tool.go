package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/A2gent/brute/internal/tools"
)

// ttsTool is the single text-to-speech tool shown to the model. It synthesizes with the engine
// selected in the app's Speech settings (AAGENT_TTS_ENGINE), the same path chat read-aloud uses,
// so the per-engine tools (piper_tts, edge_tts, ...) do not need to be exposed.
type ttsTool struct {
	server *Server
}

type ttsToolParams struct {
	Text          string `json:"text"`
	Language      string `json:"language,omitempty"`
	Voice         string `json:"voice,omitempty"`
	Engine        string `json:"engine,omitempty"`
	AutoPlayAudio *bool  `json:"auto_play_audio,omitempty"`
}

func newTTSTool(server *Server) *ttsTool { return &ttsTool{server: server} }

func (t *ttsTool) Name() string { return "tts" }

func (t *ttsTool) Description() string {
	return "Speak text aloud (text-to-speech). Uses the voice engine selected in the app's Speech settings; no engine choice is needed. Produces an audio clip the user can play."
}

func (t *ttsTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"text": map[string]interface{}{"type": "string", "description": "Text to synthesize."},
			"language": map[string]interface{}{
				"type":        "string",
				"description": "Optional language hint: en or ru. Omit to use the engine default.",
			},
			"voice": map[string]interface{}{
				"type":        "string",
				"description": "Optional voice override for the configured engine. Omit to use the default voice.",
			},
			"engine": map[string]interface{}{
				"type":        "string",
				"description": "Optional engine override (for example piper_tts, edge_tts, kokoro). Omit to use the engine selected in settings.",
			},
			"auto_play_audio": map[string]interface{}{
				"type":        "boolean",
				"description": "Hint the app to auto-play the clip (default: true).",
			},
		},
		"required": []string{"text"},
	}
}

func (t *ttsTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p ttsToolParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return &tools.Result{Success: false, Error: "text is required"}, nil
	}
	autoPlay := true
	if p.AutoPlayAudio != nil {
		autoPlay = *p.AutoPlayAudio
	}

	model := ""
	if engine := strings.TrimSpace(p.Engine); engine != "" {
		model = engine
		if voice := strings.TrimSpace(p.Voice); voice != "" && !strings.Contains(engine, ":") {
			model = engine + ":" + voice
		}
	} else if voice := strings.TrimSpace(p.Voice); voice != "" {
		// Voice without engine applies to the engine from settings.
		model = effectiveCompletionModel("")
		if !strings.Contains(model, ":") {
			model += ":" + voice
		}
	}

	result, synthErr := t.server.synthesizeSpeech(ctx, speechCompletionRequest{
		Text:     text,
		Language: strings.ToLower(strings.TrimSpace(p.Language)),
		Model:    model,
	})
	if synthErr != nil {
		return &tools.Result{Success: false, Error: synthErr.Error()}, nil
	}

	clipID := result.ClipID
	if clipID == "" && t.server.speechClips != nil {
		// Cloud engines return raw audio without going through the clip cache.
		clipID = t.server.speechClips.Save(result.ContentType, result.Audio)
	}
	if clipID == "" {
		return &tools.Result{Success: false, Error: "failed to cache generated speech clip"}, nil
	}
	contentType := result.ContentType
	if contentType == "" {
		contentType = http.DetectContentType(result.Audio)
	}

	return &tools.Result{
		Success: true,
		Output:  "Generated speech audio.\nClip ID: " + clipID,
		Metadata: map[string]interface{}{
			"audio_clip": map[string]interface{}{
				"clip_id":        clipID,
				"content_type":   contentType,
				"auto_play":      autoPlay,
				"generated_with": "tts",
			},
		},
	}, nil
}

var _ tools.Tool = (*ttsTool)(nil)
