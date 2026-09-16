package integrationtools

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/A2gent/brute/internal/tools"
)

const sttDefaultExt = ".wav"

type sttToolBase struct {
	workDir string
}

type sttParams struct {
	Engine           string `json:"engine,omitempty"`
	AudioPath        string `json:"audio_path,omitempty"`
	AudioBytesBase64 string `json:"audio_bytes_base64,omitempty"`
	AudioBase64      string `json:"audio_base64,omitempty"`
	Language         string `json:"language,omitempty"`
	TranslateToEN    *bool  `json:"translate_to_english,omitempty"`
	OutputPath       string `json:"output_path,omitempty"`
	Prompt           string `json:"prompt,omitempty"`
	Profile          string `json:"profile,omitempty"`
}

func (b *sttToolBase) prepareAudio(p sttParams) (audioPath string, cleanup func(), fail *tools.Result) {
	audioB64 := strings.TrimSpace(p.AudioBytesBase64)
	if audioB64 == "" {
		audioB64 = strings.TrimSpace(p.AudioBase64)
	}
	audioPathRaw := strings.TrimSpace(p.AudioPath)
	if audioPathRaw == "" && audioB64 == "" {
		return "", nil, &tools.Result{Success: false, Error: "one of audio_path or audio_bytes_base64 is required"}
	}
	if audioPathRaw != "" && audioB64 != "" {
		return "", nil, &tools.Result{Success: false, Error: "provide either audio_path or audio_bytes_base64, not both"}
	}

	if audioB64 != "" {
		raw, err := base64.StdEncoding.DecodeString(audioB64)
		if err != nil {
			return "", nil, &tools.Result{Success: false, Error: "audio_bytes_base64 is not valid base64"}
		}
		if len(raw) == 0 {
			return "", nil, &tools.Result{Success: false, Error: "audio_bytes_base64 payload is empty"}
		}
		tmp, err := os.CreateTemp("", "a2gent-stt-*"+sttDefaultExt)
		if err != nil {
			return "", nil, &tools.Result{Success: false, Error: fmt.Sprintf("failed to create temporary audio file: %v", err)}
		}
		cleanupPath := tmp.Name()
		if _, err := tmp.Write(raw); err != nil {
			_ = tmp.Close()
			_ = os.Remove(cleanupPath)
			return "", nil, &tools.Result{Success: false, Error: fmt.Sprintf("failed to write audio payload: %v", err)}
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(cleanupPath)
			return "", nil, &tools.Result{Success: false, Error: fmt.Sprintf("failed to finalize audio payload: %v", err)}
		}
		return cleanupPath, func() { _ = os.Remove(cleanupPath) }, nil
	}

	resolved := b.resolvePath(audioPathRaw)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, &tools.Result{Success: false, Error: fmt.Sprintf("audio file not found: %v", err)}
	}
	if info.IsDir() {
		return "", nil, &tools.Result{Success: false, Error: "audio_path must reference a file, not a directory"}
	}
	return resolved, nil, nil
}

func (b *sttToolBase) buildTranscriptResult(transcript string, p sttParams, audioPath string, durationMs int64, engine string, intro string) *tools.Result {
	outputPath := strings.TrimSpace(p.OutputPath)
	metadata := map[string]interface{}{
		"engine":               engine,
		"language":             strings.TrimSpace(p.Language),
		"translate_to_english": p.TranslateToEN,
		"audio_path":           audioPath,
		"duration_ms":          durationMs,
		"transcript":           transcript,
		"transcript_len":       len(transcript),
	}
	outputParts := []string{
		intro,
		"Text: " + transcript,
	}

	if outputPath != "" {
		resolvedOut := b.resolvePath(outputPath)
		if err := os.MkdirAll(filepath.Dir(resolvedOut), 0o755); err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to create transcript folder: %v", err)}
		}
		if err := os.WriteFile(resolvedOut, []byte(transcript+"\n"), 0o644); err != nil {
			return &tools.Result{Success: false, Error: fmt.Sprintf("failed to write transcript file: %v", err)}
		}
		metadata["output_path"] = resolvedOut
		outputParts = append(outputParts, "Saved transcript: "+resolvedOut)
	}

	return &tools.Result{
		Success:  true,
		Output:   strings.Join(outputParts, "\n"),
		Metadata: metadata,
	}
}

func (b *sttToolBase) resolvePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	baseDir := strings.TrimSpace(b.workDir)
	if baseDir == "" {
		baseDir = "."
	}
	return filepath.Clean(filepath.Join(baseDir, path))
}
