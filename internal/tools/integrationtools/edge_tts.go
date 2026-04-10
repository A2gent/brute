package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/tools"
)

const (
	edgeTTSOutputModeStream = "stream"
	edgeTTSOutputModeFile   = "file"
	edgeTTSOutputModeBoth   = "both"
	edgeTTSDefaultExt       = ".mp3"

	defaultEdgeTTSVoice = "en-US-EmmaMultilingualNeural"
	defaultEdgeTTSRate  = "+0%"
	defaultEdgeTTSPitch = "+0Hz"
)

type EdgeTTSTool struct {
	workDir   string
	clipStore *speechcache.Store
}

type edgeTTSParams struct {
	Text          string `json:"text"`
	Voice         string `json:"voice,omitempty"`
	Rate          string `json:"rate,omitempty"`
	Pitch         string `json:"pitch,omitempty"`
	EdgeTTSBin    string `json:"edge_tts_bin,omitempty"`
	OutputMode    string `json:"output_mode,omitempty"`
	OutputPath    string `json:"output_path,omitempty"`
	AutoPlayAudio *bool  `json:"auto_play_audio,omitempty"`
}

func NewEdgeTTSTool(workDir string, clipStore *speechcache.Store) *EdgeTTSTool {
	return &EdgeTTSTool{workDir: workDir, clipStore: clipStore}
}

func (t *EdgeTTSTool) Name() string {
	return "edge_tts"
}

func (t *EdgeTTSTool) Description() string {
	return "Generate speech audio with the edge-tts CLI. Defaults to stream output for web playback, and can also save an MP3 file."
}

func (t *EdgeTTSTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"text": map[string]interface{}{
				"type":        "string",
				"description": "Text to synthesize.",
			},
			"voice": map[string]interface{}{
				"type":        "string",
				"description": "Optional edge-tts voice name. Defaults to EDGE_TTS_VOICE, then en-US-EmmaMultilingualNeural.",
			},
			"rate": map[string]interface{}{
				"type":        "string",
				"description": "Optional edge-tts rate string such as +0%, -10%, or +15%.",
			},
			"pitch": map[string]interface{}{
				"type":        "string",
				"description": "Optional edge-tts pitch string such as +0Hz or -20Hz.",
			},
			"edge_tts_bin": map[string]interface{}{
				"type":        "string",
				"description": "Optional path to the edge-tts executable. Defaults to EDGE_TTS_BIN, then PATH/common install locations.",
			},
			"output_mode": map[string]interface{}{
				"type":        "string",
				"description": "stream (default), file, or both.",
				"enum":        []string{edgeTTSOutputModeStream, edgeTTSOutputModeFile, edgeTTSOutputModeBoth},
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

func (t *EdgeTTSTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p edgeTTSParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	text := strings.TrimSpace(p.Text)
	if text == "" {
		return &tools.Result{Success: false, Error: "text is required"}, nil
	}

	mode := strings.ToLower(strings.TrimSpace(p.OutputMode))
	if mode == "" {
		mode = edgeTTSOutputModeStream
	}
	if mode != edgeTTSOutputModeStream && mode != edgeTTSOutputModeFile && mode != edgeTTSOutputModeBoth {
		return &tools.Result{Success: false, Error: "output_mode must be one of: stream, file, both"}, nil
	}

	autoPlay := true
	if p.AutoPlayAudio != nil {
		autoPlay = *p.AutoPlayAudio
	}

	edgeTTSBin, err := resolveEdgeTTSBinary(strings.TrimSpace(p.EdgeTTSBin))
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	voice := strings.TrimSpace(p.Voice)
	if voice == "" {
		voice = strings.TrimSpace(os.Getenv("EDGE_TTS_VOICE"))
	}
	if voice == "" {
		voice = defaultEdgeTTSVoice
	}

	rate := strings.TrimSpace(p.Rate)
	if rate == "" {
		rate = strings.TrimSpace(os.Getenv("EDGE_TTS_RATE"))
	}
	if rate == "" {
		rate = defaultEdgeTTSRate
	}

	pitch := strings.TrimSpace(p.Pitch)
	if pitch == "" {
		pitch = strings.TrimSpace(os.Getenv("EDGE_TTS_PITCH"))
	}
	if pitch == "" {
		pitch = defaultEdgeTTSPitch
	}

	var outputPath string
	var cleanupPath string
	if mode == edgeTTSOutputModeStream {
		tmp, err := os.CreateTemp("", "a2gent-edge-tts-*"+edgeTTSDefaultExt)
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

	inputFile, err := os.CreateTemp("", "a2gent-edge-tts-input-*.txt")
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to create temporary input file: %v", err)}, nil
	}
	inputPath := inputFile.Name()
	_ = inputFile.Close()
	defer os.Remove(inputPath)

	if err := os.WriteFile(inputPath, []byte(text), 0o600); err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to write synthesis input: %v", err)}, nil
	}

	cmd := exec.CommandContext(ctx, edgeTTSBin,
		"--voice", voice,
		"--rate", rate,
		"--pitch", pitch,
		"--file", inputPath,
		"--write-media", outputPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return &tools.Result{Success: false, Error: fmt.Sprintf("edge-tts failed: %v (%s)", err, detail)}, nil
		}
		return &tools.Result{Success: false, Error: fmt.Sprintf("edge-tts failed: %v", err)}, nil
	}

	metadata := map[string]interface{}{
		"voice": voice,
		"rate":  rate,
		"pitch": pitch,
		"binary": map[string]interface{}{
			"path": edgeTTSBin,
		},
	}
	outputParts := []string{"Generated edge-tts speech audio."}

	if mode == edgeTTSOutputModeFile || mode == edgeTTSOutputModeBoth {
		metadata["file_output"] = map[string]interface{}{
			"path": outputPath,
		}
		outputParts = append(outputParts, "Saved file: "+outputPath)
	}

	if mode == edgeTTSOutputModeStream || mode == edgeTTSOutputModeBoth {
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
			contentType = "audio/mpeg"
		}
		clipID := t.clipStore.Save(contentType, audio)
		if clipID == "" {
			return &tools.Result{Success: false, Error: "failed to cache generated speech clip"}, nil
		}

		metadata["audio_clip"] = map[string]interface{}{
			"clip_id":        clipID,
			"content_type":   contentType,
			"auto_play":      autoPlay,
			"generated_with": "edge_tts",
		}
		outputParts = append(outputParts, "Clip ID: "+clipID)
	}

	return &tools.Result{
		Success:  true,
		Output:   strings.Join(outputParts, "\n"),
		Metadata: metadata,
	}, nil
}

func resolveEdgeTTSBinary(requested string) (string, error) {
	candidates := make([]string, 0, 12)
	if requested != "" {
		candidates = append(candidates, requested)
	}
	if env := strings.TrimSpace(os.Getenv("EDGE_TTS_BIN")); env != "" {
		candidates = append(candidates, env)
	}
	if pathEnv := strings.TrimSpace(os.Getenv("PATH")); pathEnv != "" {
		for _, entry := range strings.Split(pathEnv, string(os.PathListSeparator)) {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			candidates = append(candidates, filepath.Join(entry, "edge-tts"))
		}
	}
	homeDir, _ := os.UserHomeDir()
	if strings.TrimSpace(homeDir) != "" {
		candidates = append(candidates,
			filepath.Join(homeDir, ".local", "bin", "edge-tts"),
			filepath.Join(homeDir, "Library", "Python", "3.9", "bin", "edge-tts"),
			filepath.Join(homeDir, "Library", "Python", "3.10", "bin", "edge-tts"),
			filepath.Join(homeDir, "Library", "Python", "3.11", "bin", "edge-tts"),
			filepath.Join(homeDir, "Library", "Python", "3.12", "bin", "edge-tts"),
			filepath.Join(homeDir, "Library", "Python", "3.13", "bin", "edge-tts"),
		)
	}
	candidates = append(candidates, "/opt/homebrew/bin/edge-tts", "/usr/local/bin/edge-tts", "edge-tts")

	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}

		if strings.Contains(candidate, string(filepath.Separator)) {
			path := filepath.Clean(candidate)
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path, nil
			}
			continue
		}
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved, nil
		}
	}

	return "", fmt.Errorf("edge-tts executable not found (set edge_tts_bin, EDGE_TTS_BIN, or install edge-tts in PATH)")
}

func (t *EdgeTTSTool) resolveOutputPath(requested string) (string, error) {
	baseDir := strings.TrimSpace(t.workDir)
	if baseDir == "" {
		baseDir = "."
	}

	if requested == "" {
		filename := fmt.Sprintf("a2gent-edge-tts-%d%s", time.Now().UnixMilli(), edgeTTSDefaultExt)
		requested = filepath.Join(baseDir, filename)
	}

	path := requested
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	path = filepath.Clean(path)
	if filepath.Ext(path) == "" {
		path += edgeTTSDefaultExt
	}
	if !strings.EqualFold(filepath.Ext(path), ".mp3") {
		return "", fmt.Errorf("unsupported output file extension %q for edge_tts (supported: .mp3)", filepath.Ext(path))
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}
	return path, nil
}

var _ tools.Tool = (*EdgeTTSTool)(nil)
