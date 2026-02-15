package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gratheon/aagent/internal/speechcache"
	"github.com/gratheon/aagent/internal/tools"
)

const (
	piperOutputModeStream = "stream"
	piperOutputModeFile   = "file"
	piperOutputModeBoth   = "both"
	piperDefaultExt       = ".wav"
)

type PiperTTSTool struct {
	workDir   string
	clipStore *speechcache.Store
}

type piperTTSParams struct {
	Text          string   `json:"text"`
	ModelPath     string   `json:"model_path,omitempty"`
	ConfigPath    string   `json:"config_path,omitempty"`
	SpeakerID     *int     `json:"speaker_id,omitempty"`
	LengthScale   *float64 `json:"length_scale,omitempty"`
	NoiseScale    *float64 `json:"noise_scale,omitempty"`
	NoiseW        *float64 `json:"noise_w,omitempty"`
	OutputMode    string   `json:"output_mode,omitempty"`
	OutputPath    string   `json:"output_path,omitempty"`
	AutoPlayAudio *bool    `json:"auto_play_audio,omitempty"`
}

func NewPiperTTSTool(workDir string, clipStore *speechcache.Store) *PiperTTSTool {
	return &PiperTTSTool{workDir: workDir, clipStore: clipStore}
}

func (t *PiperTTSTool) Name() string {
	return "piper_tts"
}

func (t *PiperTTSTool) Description() string {
	return "Generate speech audio using local Piper ONNX models. Works across platforms and supports multilingual model packs."
}

func (t *PiperTTSTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Text to synthesize.",
			},
			"model_path": map[string]interface{}{
				"type":        "string",
				"description": "Path to a Piper .onnx voice model. Defaults to PIPER_MODEL_PATH.",
			},
			"config_path": map[string]interface{}{
				"type":        "string",
				"description": "Optional path to Piper model config JSON. Defaults to <model_path>.json when present.",
			},
			"speaker_id": map[string]interface{}{
				"type":        "integer",
				"description": "Optional speaker ID for multi-speaker models.",
			},
			"length_scale": map[string]interface{}{
				"type":        "number",
				"description": "Optional Piper length scale. Lower values produce faster speech.",
			},
			"noise_scale": map[string]interface{}{
				"type":        "number",
				"description": "Optional Piper noise scale.",
			},
			"noise_w": map[string]interface{}{
				"type":        "number",
				"description": "Optional Piper noise width.",
			},
			"output_mode": map[string]interface{}{
				"type":        "string",
				"description": "stream (default), file, or both.",
				"enum":        []string{piperOutputModeStream, piperOutputModeFile, piperOutputModeBoth},
			},
			"output_path": map[string]interface{}{
				"type":        "string",
				"description": "Optional output file path when output_mode is file/both. Relative paths resolve from the active work directory.",
			},
			"auto_play_audio": map[string]interface{}{
				"type":        "boolean",
				"description": "When stream output is used, hint the webapp to auto-play the generated clip (default: true).",
			},
		},
		"required": []string{"text"},
	}
}

func (t *PiperTTSTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p piperTTSParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if _, err := exec.LookPath("piper"); err != nil {
		return &tools.Result{Success: false, Error: "piper binary not found in PATH"}, nil
	}

	text := strings.TrimSpace(p.Text)
	if text == "" {
		return &tools.Result{Success: false, Error: "text is required"}, nil
	}

	mode := strings.ToLower(strings.TrimSpace(p.OutputMode))
	if mode == "" {
		mode = piperOutputModeStream
	}
	if mode != piperOutputModeStream && mode != piperOutputModeFile && mode != piperOutputModeBoth {
		return &tools.Result{Success: false, Error: "output_mode must be one of: stream, file, both"}, nil
	}

	modelPath := strings.TrimSpace(p.ModelPath)
	if modelPath == "" {
		modelPath = strings.TrimSpace(os.Getenv("PIPER_MODEL_PATH"))
	}
	if modelPath == "" {
		return &tools.Result{Success: false, Error: "model_path is required (pass model_path or set PIPER_MODEL_PATH)"}, nil
	}
	modelPath = t.resolvePath(modelPath)
	if !strings.EqualFold(filepath.Ext(modelPath), ".onnx") {
		return &tools.Result{Success: false, Error: "model_path must point to a .onnx file"}, nil
	}
	if _, err := os.Stat(modelPath); err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("model_path is not accessible: %v", err)}, nil
	}

	configPath := strings.TrimSpace(p.ConfigPath)
	if configPath == "" {
		inferred := modelPath + ".json"
		if _, err := os.Stat(inferred); err == nil {
			configPath = inferred
		}
	}
	if configPath != "" {
		configPath = t.resolvePath(configPath)
		if _, err := os.Stat(configPath); err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("config_path is not accessible: %v", err)}, nil
		}
	}

	autoPlay := true
	if p.AutoPlayAudio != nil {
		autoPlay = *p.AutoPlayAudio
	}

	var outputPath string
	var cleanupPath string
	if mode == piperOutputModeStream {
		tmp, err := os.CreateTemp("", "a2gent-piper-*"+piperDefaultExt)
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to create temporary output file: %v", err)}, nil
		}
		cleanupPath = tmp.Name()
		outputPath = cleanupPath
		_ = tmp.Close()
	} else {
		resolved, err := t.resolveOutputPath(strings.TrimSpace(p.OutputPath))
		if err != nil {
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
		outputPath = resolved
	}
	if cleanupPath != "" {
		defer os.Remove(cleanupPath)
	}

	args := []string{
		"--model", modelPath,
		"--output_file", outputPath,
	}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	if p.SpeakerID != nil {
		args = append(args, "--speaker", strconv.Itoa(*p.SpeakerID))
	}
	if p.LengthScale != nil {
		args = append(args, "--length_scale", formatFloat(*p.LengthScale))
	}
	if p.NoiseScale != nil {
		args = append(args, "--noise_scale", formatFloat(*p.NoiseScale))
	}
	if p.NoiseW != nil {
		args = append(args, "--noise_w", formatFloat(*p.NoiseW))
	}

	cmd := exec.CommandContext(ctx, "piper", args...)
	cmd.Stdin = strings.NewReader(text + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return &tools.Result{Success: false, Error: fmt.Sprintf("piper failed: %v (%s)", err, detail)}, nil
		}
		return &tools.Result{Success: false, Error: fmt.Sprintf("piper failed: %v", err)}, nil
	}

	metadata := map[string]interface{}{
		"model_path": modelPath,
	}
	if configPath != "" {
		metadata["config_path"] = configPath
	}
	outputParts := []string{"Generated Piper speech audio."}

	if mode == piperOutputModeFile || mode == piperOutputModeBoth {
		metadata["file_output"] = map[string]interface{}{
			"path": outputPath,
		}
		outputParts = append(outputParts, "Saved file: "+outputPath)
	}

	if mode == piperOutputModeStream || mode == piperOutputModeBoth {
		if t.clipStore == nil {
			return &tools.Result{Success: false, Error: "speech clip cache is unavailable"}, nil
		}
		audio, err := os.ReadFile(outputPath)
		if err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to read synthesized audio: %v", err)}, nil
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

		metadata["audio_clip"] = map[string]interface{}{
			"clip_id":        clipID,
			"content_type":   contentType,
			"auto_play":      autoPlay,
			"generated_with": "piper_tts",
		}
		outputParts = append(outputParts, "Clip ID: "+clipID)
	}

	return &tools.Result{
		Success:  true,
		Output:   strings.Join(outputParts, "\n"),
		Metadata: metadata,
	}, nil
}

func (t *PiperTTSTool) resolveOutputPath(requested string) (string, error) {
	baseDir := strings.TrimSpace(t.workDir)
	if baseDir == "" {
		baseDir = "."
	}

	if requested == "" {
		filename := fmt.Sprintf("a2gent-piper-%d%s", time.Now().UnixMilli(), piperDefaultExt)
		requested = filepath.Join(baseDir, filename)
	}

	path := t.resolvePath(requested)
	if filepath.Ext(path) == "" {
		path += piperDefaultExt
	}
	if !strings.EqualFold(filepath.Ext(path), ".wav") {
		return "", fmt.Errorf("unsupported output file extension %q for piper_tts (supported: .wav)", filepath.Ext(path))
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}
	return path, nil
}

func (t *PiperTTSTool) resolvePath(path string) string {
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

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

var _ tools.Tool = (*PiperTTSTool)(nil)

type PiperListModelsTool struct {
	workDir string
}

type piperListModelsParams struct {
	ModelDir   string `json:"model_dir,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

func NewPiperListModelsTool(workDir string) *PiperListModelsTool {
	return &PiperListModelsTool{workDir: workDir}
}

func (t *PiperListModelsTool) Name() string {
	return "piper_list_models"
}

func (t *PiperListModelsTool) Description() string {
	return "List available local Piper .onnx models from a directory."
}

func (t *PiperListModelsTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"model_dir": map[string]interface{}{
				"type":        "string",
				"description": "Directory to scan recursively for .onnx files. Defaults to PIPER_MODEL_DIR then ./models/piper.",
			},
			"max_results": map[string]interface{}{
				"type":        "integer",
				"description": "Optional maximum number of models to include (default: 100).",
			},
		},
	}
}

func (t *PiperListModelsTool) Execute(_ context.Context, params json.RawMessage) (*tools.Result, error) {
	var p piperListModelsParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}

	modelDir := strings.TrimSpace(p.ModelDir)
	if modelDir == "" {
		modelDir = strings.TrimSpace(os.Getenv("PIPER_MODEL_DIR"))
	}
	if modelDir == "" {
		modelDir = filepath.Join(t.workDir, "models", "piper")
	}
	if !filepath.IsAbs(modelDir) {
		modelDir = filepath.Join(t.workDir, modelDir)
	}
	modelDir = filepath.Clean(modelDir)

	maxResults := p.MaxResults
	if maxResults <= 0 {
		maxResults = 100
	}

	models := make([]string, 0, 16)
	walkErr := filepath.WalkDir(modelDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(d.Name()), ".onnx") {
			models = append(models, filepath.Clean(path))
		}
		return nil
	})
	if walkErr != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to scan model_dir: %v", walkErr)}, nil
	}

	sort.Strings(models)
	if len(models) > maxResults {
		models = models[:maxResults]
	}

	entries := make([]map[string]interface{}, 0, len(models))
	lines := make([]string, 0, len(models)+2)
	lines = append(lines, fmt.Sprintf("Found %d Piper model(s) in %s", len(models), modelDir))
	for _, modelPath := range models {
		configPath := modelPath + ".json"
		_, hasConfig := os.Stat(configPath)
		entry := map[string]interface{}{
			"model_path": modelPath,
		}
		if hasConfig == nil {
			entry["config_path"] = configPath
			lines = append(lines, fmt.Sprintf("- %s (config: %s)", modelPath, configPath))
		} else {
			lines = append(lines, "- "+modelPath)
		}
		entries = append(entries, entry)
	}

	return &tools.Result{
		Success: true,
		Output:  strings.Join(lines, "\n"),
		Metadata: map[string]interface{}{
			"model_dir": modelDir,
			"models":    entries,
		},
	}, nil
}

var _ tools.Tool = (*PiperListModelsTool)(nil)
