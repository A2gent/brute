package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/tools"
)

const (
	macOSSayOutputModeStream = "stream"
	macOSSayOutputModeFile   = "file"
	macOSSayOutputModeBoth   = "both"
	macOSSayDefaultExt       = ".m4a"
)

var macOSSaySupportedExt = map[string]struct{}{
	".aiff": {},
	".aif":  {},
	".caf":  {},
	".m4a":  {},
}

type MacOSSayTTSTool struct {
	workDir   string
	clipStore *speechcache.Store
}

type macOSSayTTSParams struct {
	Text          string `json:"text"`
	Voice         string `json:"voice,omitempty"`
	Rate          int    `json:"rate,omitempty"`
	OutputMode    string `json:"output_mode,omitempty"`
	OutputPath    string `json:"output_path,omitempty"`
	AutoPlayAudio *bool  `json:"auto_play_audio,omitempty"`
}

func NewMacOSSayTTSTool(workDir string, clipStore *speechcache.Store) *MacOSSayTTSTool {
	return &MacOSSayTTSTool{workDir: workDir, clipStore: clipStore}
}

func (t *MacOSSayTTSTool) Name() string {
	return "macos_say_tts"
}

func (t *MacOSSayTTSTool) Description() string {
	return "Generate speech audio with the macOS `say` command. Defaults to in-memory stream output for web playback, and can also save to a file."
}

func (t *MacOSSayTTSTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Text to speak.",
			},
			"voice": map[string]interface{}{
				"type":        "string",
				"description": "Optional macOS voice name (for example: Samantha, Alex).",
			},
			"rate": map[string]interface{}{
				"type":        "integer",
				"description": "Optional speech rate in words per minute (>0).",
			},
			"output_mode": map[string]interface{}{
				"type":        "string",
				"description": "stream (default), file, or both.",
				"enum":        []string{macOSSayOutputModeStream, macOSSayOutputModeFile, macOSSayOutputModeBoth},
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

func (t *MacOSSayTTSTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p macOSSayTTSParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if runtime.GOOS != "darwin" {
		return &tools.Result{Success: false, Error: "macos_say_tts is only available on macOS"}, nil
	}

	text := strings.TrimSpace(p.Text)
	if text == "" {
		return &tools.Result{Success: false, Error: "text is required"}, nil
	}

	mode := strings.ToLower(strings.TrimSpace(p.OutputMode))
	if mode == "" {
		mode = macOSSayOutputModeStream
	}
	if mode != macOSSayOutputModeStream && mode != macOSSayOutputModeFile && mode != macOSSayOutputModeBoth {
		return &tools.Result{Success: false, Error: "output_mode must be one of: stream, file, both"}, nil
	}

	autoPlay := true
	if p.AutoPlayAudio != nil {
		autoPlay = *p.AutoPlayAudio
	}

	var outputPath string
	var cleanupPath string
	if mode == macOSSayOutputModeStream {
		tmp, err := os.CreateTemp("", "a2gent-say-*"+macOSSayDefaultExt)
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

	args := make([]string, 0, 8)
	if voice := strings.TrimSpace(p.Voice); voice != "" {
		args = append(args, "-v", voice)
	}
	if p.Rate > 0 {
		args = append(args, "-r", strconv.Itoa(p.Rate))
	}
	args = append(args, "-o", outputPath, text)

	cmd := exec.CommandContext(ctx, "say", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return &tools.Result{Success: false, Error: fmt.Sprintf("say failed: %v (%s)", err, detail)}, nil
		}
		return &tools.Result{Success: false, Error: fmt.Sprintf("say failed: %v", err)}, nil
	}

	metadata := map[string]interface{}{}
	outputParts := make([]string, 0, 3)
	outputParts = append(outputParts, "Generated macOS speech audio.")

	if mode == macOSSayOutputModeFile || mode == macOSSayOutputModeBoth {
		metadata["file_output"] = map[string]interface{}{
			"path": outputPath,
		}
		outputParts = append(outputParts, "Saved file: "+outputPath)
	}

	if mode == macOSSayOutputModeStream || mode == macOSSayOutputModeBoth {
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
			contentType = "audio/mp4"
		}
		clipID := t.clipStore.Save(contentType, audio)
		if clipID == "" {
			return &tools.Result{Success: false, Error: "failed to cache generated speech clip"}, nil
		}

		metadata["audio_clip"] = map[string]interface{}{
			"clip_id":        clipID,
			"content_type":   contentType,
			"auto_play":      autoPlay,
			"generated_with": "macos_say_tts",
		}
		outputParts = append(outputParts, "Clip ID: "+clipID)
	}

	return &tools.Result{
		Success:  true,
		Output:   strings.Join(outputParts, "\n"),
		Metadata: metadata,
	}, nil
}

func (t *MacOSSayTTSTool) resolveOutputPath(requested string) (string, error) {
	baseDir := strings.TrimSpace(t.workDir)
	if baseDir == "" {
		baseDir = "."
	}

	if requested == "" {
		filename := fmt.Sprintf("a2gent-say-%d%s", time.Now().UnixMilli(), macOSSayDefaultExt)
		requested = filepath.Join(baseDir, filename)
	}

	path := requested
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	path = filepath.Clean(path)
	if filepath.Ext(path) == "" {
		path += macOSSayDefaultExt
	}

	ext := strings.ToLower(filepath.Ext(path))
	if _, ok := macOSSaySupportedExt[ext]; !ok {
		return "", fmt.Errorf("unsupported output file extension %q for macOS say (supported: .aiff, .aif, .caf, .m4a)", ext)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}
	return path, nil
}

var _ tools.Tool = (*MacOSSayTTSTool)(nil)
