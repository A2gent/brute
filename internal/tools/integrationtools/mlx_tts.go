package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/speechengine"
	"github.com/A2gent/brute/internal/tools"
)

type mlxTTSTool struct {
	clipStore *speechcache.Store
	engine    string
	toolName  string
}

type mlxTTSParams struct {
	Text          string `json:"text"`
	Language      string `json:"language,omitempty"`
	AutoPlayAudio *bool  `json:"auto_play_audio,omitempty"`
}

func newMLXTTSTool(clipStore *speechcache.Store, engine string, toolName string) *mlxTTSTool {
	return &mlxTTSTool{clipStore: clipStore, engine: engine, toolName: toolName}
}

func NewKokoroTTSTool(clipStore *speechcache.Store) *mlxTTSTool {
	return newMLXTTSTool(clipStore, speechengine.EngineKokoro, "kokoro_tts")
}

func NewQwen3TTSTool(clipStore *speechcache.Store) *mlxTTSTool {
	return newMLXTTSTool(clipStore, speechengine.EngineQwen3TTS, "qwen3_tts")
}

func (t *mlxTTSTool) Name() string {
	return t.toolName
}

func (t *mlxTTSTool) Description() string {
	switch t.engine {
	case speechengine.EngineKokoro:
		return "Generate speech audio using the local Kokoro TTS engine (mlx-audio)."
	case speechengine.EngineQwen3TTS:
		return "Generate speech audio using the local Qwen3-TTS engine (mlx-audio)."
	default:
		return "Generate speech audio using a local mlx-audio TTS engine."
	}
}

func (t *mlxTTSTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Text to synthesize.",
			},
			"language": map[string]interface{}{
				"type":        "string",
				"description": "Optional language hint (for example: en, ru). Required for qwen3_tts when not inferable.",
			},
			"auto_play_audio": map[string]interface{}{
				"type":        "boolean",
				"description": "Hint the webapp to auto-play the generated clip (default: true).",
			},
		},
		"required": []string{"text"},
	}
}

func (t *mlxTTSTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p mlxTTSParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	text := strings.TrimSpace(p.Text)
	if text == "" {
		return &tools.Result{Success: false, Error: "text is required"}, nil
	}
	if t.clipStore == nil {
		return &tools.Result{Success: false, Error: "speech clip cache is unavailable"}, nil
	}

	autoPlay := true
	if p.AutoPlayAudio != nil {
		autoPlay = *p.AutoPlayAudio
	}

	audio, err := speechengine.SynthesizeWithLanguage(ctx, t.engine, text, strings.TrimSpace(p.Language))
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	if len(audio) == 0 {
		return &tools.Result{Success: false, Error: "generated audio is empty"}, nil
	}

	contentType := strings.TrimSpace(http.DetectContentType(audio))
	if contentType == "" || !strings.HasPrefix(contentType, "audio/") {
		contentType = "audio/wav"
	}

	clipID := t.clipStore.Save(contentType, audio)
	if clipID == "" {
		return &tools.Result{Success: false, Error: "failed to cache generated speech clip"}, nil
	}

	metadata := map[string]interface{}{
		"engine":   t.engine,
		"language": strings.TrimSpace(p.Language),
		"audio_clip": map[string]interface{}{
			"clip_id":        clipID,
			"content_type":   contentType,
			"auto_play":      autoPlay,
			"generated_with": t.toolName,
		},
	}

	return &tools.Result{
		Success: true,
		Output:  fmt.Sprintf("Generated %s speech audio.\nClip ID: %s", t.engine, clipID),
		Metadata: metadata,
	}, nil
}

var _ tools.Tool = (*mlxTTSTool)(nil)
