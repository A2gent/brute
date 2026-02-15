package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gratheon/aagent/internal/speechcache"
	"github.com/gratheon/aagent/internal/tools"
)

const (
	mossTTSDefaultEndpoint      = "http://127.0.0.1:8099"
	mossTTSDefaultModelID       = "OpenMOSS-Team/MOSS-TTS-Realtime"
	mossTTSOutputModeStream     = "stream"
	mossTTSOutputModeFile       = "file"
	mossTTSOutputModeBoth       = "both"
	mossTTSDefaultFileExtension = ".wav"
)

type MossRealtimeTTSTool struct {
	workDir   string
	clipStore *speechcache.Store
	client    *http.Client
}

type mossRealtimeTTSParams struct {
	Text              string   `json:"text"`
	ModelID           string   `json:"model_id,omitempty"`
	EndpointURL       string   `json:"endpoint_url,omitempty"`
	Device            string   `json:"device,omitempty"`
	ReferenceAudio    []string `json:"reference_audio,omitempty"`
	ReferenceText     []string `json:"reference_text,omitempty"`
	PromptWavPath     string   `json:"prompt_wav_path,omitempty"`
	PromptText        string   `json:"prompt_text,omitempty"`
	PromptLanguage    string   `json:"prompt_language,omitempty"`
	InferenceLanguage string   `json:"inference_language,omitempty"`
	OutputMode        string   `json:"output_mode,omitempty"`
	OutputPath        string   `json:"output_path,omitempty"`
	AutoPlayAudio     *bool    `json:"auto_play_audio,omitempty"`
}

type mossSynthesizeRequest struct {
	Text              string   `json:"text"`
	ModelID           string   `json:"model_id,omitempty"`
	Device            string   `json:"device,omitempty"`
	ReferenceAudio    []string `json:"reference_audio,omitempty"`
	ReferenceText     []string `json:"reference_text,omitempty"`
	PromptLanguage    string   `json:"prompt_language,omitempty"`
	InferenceLanguage string   `json:"inference_language,omitempty"`
}

func NewMossRealtimeTTSTool(workDir string, clipStore *speechcache.Store) *MossRealtimeTTSTool {
	return &MossRealtimeTTSTool{
		workDir:   workDir,
		clipStore: clipStore,
		client: &http.Client{
			Timeout: 10 * time.Minute,
		},
	}
}

func (t *MossRealtimeTTSTool) Name() string {
	return "moss_tts_realtime"
}

func (t *MossRealtimeTTSTool) Description() string {
	return "Generate multilingual speech using a local Dockerized MOSS-TTS-Realtime service and return web-playable clips."
}

func (t *MossRealtimeTTSTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Text to synthesize as speech.",
			},
			"endpoint_url": map[string]interface{}{
				"type":        "string",
				"description": "Optional MOSS Docker service URL. Defaults to MOSS_TTS_ENDPOINT or http://127.0.0.1:8099.",
			},
			"model_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional MOSS model ID. Defaults to OpenMOSS-Team/MOSS-TTS-Realtime.",
			},
			"device": map[string]interface{}{
				"type":        "string",
				"description": "Optional generation device hint (for example cpu, cuda).",
			},
			"reference_audio": map[string]interface{}{
				"type":        "array",
				"description": "Optional reference audio paths accessible inside the Docker container.",
				"items":       map[string]interface{}{"type": "string"},
			},
			"reference_text": map[string]interface{}{
				"type":        "array",
				"description": "Optional reference text values aligned with reference_audio.",
				"items":       map[string]interface{}{"type": "string"},
			},
			"prompt_wav_path": map[string]interface{}{
				"type":        "string",
				"description": "Optional single reference audio path alias for reference_audio[0].",
			},
			"prompt_text": map[string]interface{}{
				"type":        "string",
				"description": "Optional single reference text alias for reference_text[0].",
			},
			"prompt_language": map[string]interface{}{
				"type":        "string",
				"description": "Optional prompt language hint.",
			},
			"inference_language": map[string]interface{}{
				"type":        "string",
				"description": "Optional target language hint.",
			},
			"output_mode": map[string]interface{}{
				"type":        "string",
				"description": "stream (default), file, or both.",
				"enum":        []string{mossTTSOutputModeStream, mossTTSOutputModeFile, mossTTSOutputModeBoth},
			},
			"output_path": map[string]interface{}{
				"type":        "string",
				"description": "Optional output path when output_mode is file/both. Relative paths resolve from work directory.",
			},
			"auto_play_audio": map[string]interface{}{
				"type":        "boolean",
				"description": "When stream output is used, hint the webapp to auto-play the generated clip (default true).",
			},
		},
		"required": []string{"text"},
	}
}

func (t *MossRealtimeTTSTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p mossRealtimeTTSParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	text := strings.TrimSpace(p.Text)
	if text == "" {
		return &tools.Result{Success: false, Error: "text is required"}, nil
	}

	mode := strings.ToLower(strings.TrimSpace(p.OutputMode))
	if mode == "" {
		mode = mossTTSOutputModeStream
	}
	if mode != mossTTSOutputModeStream && mode != mossTTSOutputModeFile && mode != mossTTSOutputModeBoth {
		return &tools.Result{Success: false, Error: "output_mode must be one of: stream, file, both"}, nil
	}

	autoPlay := true
	if p.AutoPlayAudio != nil {
		autoPlay = *p.AutoPlayAudio
	}

	endpointURL := strings.TrimSpace(p.EndpointURL)
	if endpointURL == "" {
		endpointURL = strings.TrimSpace(os.Getenv("MOSS_TTS_ENDPOINT"))
	}
	if endpointURL == "" {
		endpointURL = mossTTSDefaultEndpoint
	}
	endpointURL = strings.TrimRight(endpointURL, "/")

	referenceAudio := normalizeStringSlice(p.ReferenceAudio)
	if len(referenceAudio) == 0 {
		if prompt := strings.TrimSpace(p.PromptWavPath); prompt != "" {
			referenceAudio = []string{prompt}
		}
	}
	referenceText := normalizeStringSlice(p.ReferenceText)
	if len(referenceText) == 0 {
		if prompt := strings.TrimSpace(p.PromptText); prompt != "" {
			referenceText = []string{prompt}
		}
	}
	if len(referenceText) > 0 && len(referenceAudio) > 0 && len(referenceText) != len(referenceAudio) {
		return &tools.Result{Success: false, Error: "reference_audio and reference_text must contain the same number of items"}, nil
	}

	modelID := strings.TrimSpace(p.ModelID)
	if modelID == "" {
		modelID = mossTTSDefaultModelID
	}

	requestPayload := mossSynthesizeRequest{
		Text:              text,
		ModelID:           modelID,
		Device:            strings.TrimSpace(p.Device),
		ReferenceAudio:    referenceAudio,
		ReferenceText:     referenceText,
		PromptLanguage:    strings.TrimSpace(p.PromptLanguage),
		InferenceLanguage: strings.TrimSpace(p.InferenceLanguage),
	}
	body, err := json.Marshal(requestPayload)
	if err != nil {
		return &tools.Result{Success: false, Error: "failed to encode MOSS synth request"}, nil
	}

	requestURL := endpointURL + "/synthesize"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to create MOSS service request: %v", err)}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/wav")

	resp, err := t.client.Do(req)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to call MOSS service at %s: %v", requestURL, err)}, nil
	}
	defer resp.Body.Close()

	audio, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024*1024))
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to read MOSS response: %v", err)}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(audio))
		if detail == "" {
			detail = resp.Status
		}
		return &tools.Result{Success: false, Error: fmt.Sprintf("MOSS service error (status %d): %s", resp.StatusCode, detail)}, nil
	}
	if len(audio) == 0 {
		return &tools.Result{Success: false, Error: "MOSS service returned empty audio"}, nil
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" || !strings.HasPrefix(strings.ToLower(contentType), "audio/") {
		contentType = strings.TrimSpace(http.DetectContentType(audio))
	}
	if contentType == "" || !strings.HasPrefix(strings.ToLower(contentType), "audio/") {
		contentType = "audio/wav"
	}

	metadata := map[string]interface{}{}
	outputParts := []string{"Generated MOSS realtime speech audio."}

	if mode == mossTTSOutputModeFile || mode == mossTTSOutputModeBoth {
		outputPath, err := t.resolveOutputPath(strings.TrimSpace(p.OutputPath))
		if err != nil {
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
		if err := os.WriteFile(outputPath, audio, 0o644); err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to write audio file: %v", err)}, nil
		}
		metadata["file_output"] = map[string]interface{}{"path": outputPath}
		outputParts = append(outputParts, "Saved file: "+outputPath)
	}

	if mode == mossTTSOutputModeStream || mode == mossTTSOutputModeBoth {
		if t.clipStore == nil {
			return &tools.Result{Success: false, Error: "speech clip cache is unavailable"}, nil
		}
		clipID := t.clipStore.Save(contentType, audio)
		if clipID == "" {
			return &tools.Result{Success: false, Error: "failed to cache generated speech clip"}, nil
		}
		metadata["audio_clip"] = map[string]interface{}{
			"clip_id":        clipID,
			"content_type":   contentType,
			"auto_play":      autoPlay,
			"generated_with": "moss_tts_realtime",
		}
		outputParts = append(outputParts, "Clip ID: "+clipID)
	}

	return &tools.Result{Success: true, Output: strings.Join(outputParts, "\n"), Metadata: metadata}, nil
}

func normalizeStringSlice(in []string) []string {
	out := make([]string, 0, len(in))
	for _, item := range in {
		trimmed := strings.TrimSpace(item)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (t *MossRealtimeTTSTool) resolveOutputPath(requested string) (string, error) {
	baseDir := strings.TrimSpace(t.workDir)
	if baseDir == "" {
		baseDir = "."
	}
	if requested == "" {
		filename := fmt.Sprintf("a2gent-moss-%d%s", time.Now().UnixMilli(), mossTTSDefaultFileExtension)
		requested = filepath.Join(baseDir, filename)
	}

	path := t.resolvePath(requested)
	if filepath.Ext(path) == "" {
		path += mossTTSDefaultFileExtension
	}
	if !strings.EqualFold(filepath.Ext(path), ".wav") {
		return "", fmt.Errorf("unsupported output file extension %q for moss_tts_realtime (supported: .wav)", filepath.Ext(path))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}
	return path, nil
}

func (t *MossRealtimeTTSTool) resolvePath(path string) string {
	cleaned := strings.TrimSpace(path)
	if cleaned == "" {
		return cleaned
	}
	if filepath.IsAbs(cleaned) {
		return filepath.Clean(cleaned)
	}
	baseDir := strings.TrimSpace(t.workDir)
	if baseDir == "" {
		baseDir = "."
	}
	return filepath.Clean(filepath.Join(baseDir, cleaned))
}

var _ tools.Tool = (*MossRealtimeTTSTool)(nil)
