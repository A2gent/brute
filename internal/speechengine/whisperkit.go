package speechengine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func transcribeWhisperKit(ctx context.Context, cfg runtimeConfig, audioPath string, opts TranscribeOptions) (string, error) {
	if strings.TrimSpace(cfg.whisperKitBin) == "" {
		return "", fmt.Errorf("%w: install whisperkit-cli (brew install whisperkit-cli) or set AAGENT_SPEECH_WHISPERKIT_BIN", ErrWhisperKitNotConfigured)
	}

	reportDir, err := os.MkdirTemp("", "aagent-whisperkit-report-*")
	if err != nil {
		return "", fmt.Errorf("failed to create whisperkit report dir: %w", err)
	}
	defer os.RemoveAll(reportDir)

	args := []string{
		"transcribe",
		"--audio-path", audioPath,
		"--model", cfg.whisperKitModel,
		"--report",
		"--report-path", reportDir,
	}
	if lang := normalizeLanguage(opts.Language); lang != "" {
		args = append(args, "--language", lang)
	}
	if prompt := normalizePrompt(opts.Prompt); prompt != "" {
		args = append(args, "--prompt", prompt)
	}
	if wantsTranslation(opts) {
		args = append(args, "--task", "translate")
	}

	_, err = runExternal(ctx, cfg.whisperKitBin, args...)
	if err != nil {
		return "", err
	}

	if text, ok := readWhisperKitReport(reportDir, audioPath); ok {
		return text, nil
	}
	return "", fmt.Errorf("%w (report dir: %s)", ErrWhisperKitReportMissing, reportDir)
}

func readWhisperKitReport(reportDir, audioPath string) (string, bool) {
	base := strings.TrimSuffix(filepath.Base(audioPath), filepath.Ext(audioPath))
	candidates := []string{
		filepath.Join(reportDir, base+".json"),
	}
	entries, err := os.ReadDir(reportDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
				continue
			}
			candidates = append(candidates, filepath.Join(reportDir, entry.Name()))
		}
	}

	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}

		raw, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		if text, ok := parseWhisperKitReportJSON(raw); ok {
			return text, true
		}
	}
	return "", false
}

func parseWhisperKitReportJSON(raw []byte) (string, bool) {
	var object struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &object); err == nil {
		text := strings.TrimSpace(object.Text)
		if text != "" {
			return text, true
		}
	}

	var results []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &results); err == nil {
		parts := make([]string, 0, len(results))
		for _, item := range results {
			text := strings.TrimSpace(item.Text)
			if text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " "), true
		}
	}

	return "", false
}
