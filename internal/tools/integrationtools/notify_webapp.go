package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/tools"
)

type NotifyWebAppTool struct{}

type notifyWebAppParams struct {
	Message       string `json:"message"`
	Title         string `json:"title,omitempty"`
	Level         string `json:"level,omitempty"`
	AudioClipID   string `json:"audio_clip_id,omitempty"`
	ImagePath     string `json:"image_path,omitempty"`
	ImageURL      string `json:"image_url,omitempty"`
	AutoPlayAudio *bool  `json:"auto_play_audio,omitempty"`
}

func NewNotifyWebAppTool() *NotifyWebAppTool {
	return &NotifyWebAppTool{}
}

func (t *NotifyWebAppTool) Name() string {
	return "notify_webapp"
}

func (t *NotifyWebAppTool) Description() string {
	return "Send a structured notification event to the webapp UI. Can optionally reference an audio clip ID (for example the `Clip ID` returned by macos_say_tts) for auto-play."
}

func (t *NotifyWebAppTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"message": map[string]interface{}{
				"type":        "string",
				"description": "Notification body text.",
			},
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Optional notification title.",
			},
			"level": map[string]interface{}{
				"type":        "string",
				"description": "Notification severity level.",
				"enum":        []string{"info", "success", "warning", "error"},
			},
			"audio_clip_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional speech clip ID produced by a TTS tool.",
			},
			"image_path": map[string]interface{}{
				"type":        "string",
				"description": "Optional local image file path (for example from take_screenshot_tool output path).",
			},
			"image_url": map[string]interface{}{
				"type":        "string",
				"description": "Optional absolute image URL to show in webapp notification.",
			},
			"auto_play_audio": map[string]interface{}{
				"type":        "boolean",
				"description": "Auto-play the referenced audio clip in webapp when available (default: true).",
			},
		},
		"required": []string{"message"},
	}
}

func (t *NotifyWebAppTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	_ = ctx

	var p notifyWebAppParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	message := strings.TrimSpace(p.Message)
	if message == "" {
		return &tools.Result{Success: false, Error: "message is required"}, nil
	}

	level := strings.ToLower(strings.TrimSpace(p.Level))
	if level == "" {
		level = "info"
	}
	switch level {
	case "info", "success", "warning", "error":
	default:
		return &tools.Result{Success: false, Error: "level must be one of: info, success, warning, error"}, nil
	}

	autoPlayAudio := true
	if p.AutoPlayAudio != nil {
		autoPlayAudio = *p.AutoPlayAudio
	}

	payload := map[string]interface{}{
		"title":           strings.TrimSpace(p.Title),
		"message":         message,
		"level":           level,
		"audio_clip_id":   strings.TrimSpace(p.AudioClipID),
		"image_path":      strings.TrimSpace(p.ImagePath),
		"image_url":       strings.TrimSpace(p.ImageURL),
		"auto_play_audio": autoPlayAudio,
	}

	output := "Queued webapp notification."
	if payload["title"] != "" {
		output = fmt.Sprintf("Queued webapp notification: %s", payload["title"])
	}

	return &tools.Result{
		Success: true,
		Output:  output,
		Metadata: map[string]interface{}{
			"webapp_notification": payload,
		},
	}, nil
}

var _ tools.Tool = (*NotifyWebAppTool)(nil)
