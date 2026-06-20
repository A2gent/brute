package whispercpp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/A2gent/brute/internal/logging"
)

const maxPromptRunes = 1200

func Transcribe(ctx context.Context, audioPath string, language string) (string, error) {
	return TranscribeWithOptions(ctx, audioPath, language, nil)
}

func TranscribeWithOptions(ctx context.Context, audioPath string, language string, translateToEnglish *bool) (string, error) {
	return TranscribeWithConfig(ctx, audioPath, TranscribeOptions{
		Language:           language,
		TranslateToEnglish: translateToEnglish,
	})
}

func TranscribeWithConfig(ctx context.Context, audioPath string, opts TranscribeOptions) (string, error) {
	cfg, err := loadConfig(ctx, opts)
	if err != nil {
		return "", err
	}

	lang := normalizeLanguage(opts.Language)
	if lang == "" {
		lang = normalizeLanguage(cfg.DefaultLanguage)
	}
	if lang == "" {
		lang = "auto"
	}
	translate := cfg.Translate
	if opts.TranslateToEnglish != nil {
		translate = *opts.TranslateToEnglish
	}

	outputDir, err := os.MkdirTemp("", "aagent-whisper-out-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp output folder: %w", err)
	}
	defer os.RemoveAll(outputDir)

	outputPrefix := filepath.Join(outputDir, "transcript")
	args := []string{
		"-m", cfg.ModelPath,
		"-f", audioPath,
		"-otxt",
		"-of", outputPrefix,
		"-nt",
	}
	if lang != "" {
		args = append(args, "-l", lang)
	}
	if prompt := normalizePrompt(opts.Prompt); prompt != "" {
		args = append(args, "--prompt", prompt)
	}
	if cfg.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(cfg.Threads))
	}
	if translate {
		args = append(args, "-tr")
	}
	if resolveNoGPU() {
		logging.Info("whisper.cpp configured for CPU-only mode (AAGENT_WHISPER_NO_GPU enabled)")
		args = append(args, "-ng", "-nfa")
	}

	logging.Info("whisper.cpp run start: binary=%s model=%s no_gpu=%v lang=%s translate=%v", cfg.BinaryPath, cfg.ModelName, resolveNoGPU(), lang, translate)
	text, err := runWhisperCLI(ctx, cfg.BinaryPath, args, outputPrefix)
	if err == nil {
		logging.Info("whisper.cpp run succeeded (primary)")
		return text, nil
	}
	logging.Warn("whisper.cpp run failed (primary): %v", err)
	if resolveNoGPU() {
		return "", err
	}
	if shouldRetryWhisperWithoutGPU(err) {
		logging.Warn("whisper.cpp retry condition matched; retrying with CPU-only flags")
		retryArgs := append([]string{}, args...)
		retryArgs = append(retryArgs, "-ng", "-nfa")
		if retryText, retryErr := runWhisperCLI(ctx, cfg.BinaryPath, retryArgs, outputPrefix); retryErr == nil {
			logging.Info("whisper.cpp CPU-only retry succeeded")
			return retryText, nil
		} else {
			logging.Warn("whisper.cpp CPU-only retry failed: %v", retryErr)
			return "", fmt.Errorf("whisper.cpp GPU run failed and CPU retry failed: first=%v; retry=%v", err, retryErr)
		}
	}
	return "", err
}

func runWhisperCLI(ctx context.Context, binaryPath string, args []string, outputPrefix string) (string, error) {
	logging.Info("whisper.cpp command args: %s", summarizeWhisperArgs(args))
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(output.String())
		if detail == "" {
			detail = err.Error()
		}
		logging.Warn("whisper.cpp command failed: %s", truncateLogLine(detail, 1000))
		return "", fmt.Errorf("whisper.cpp failed: %s", detail)
	}

	contents, err := os.ReadFile(outputPrefix + ".txt")
	if err != nil {
		detail := strings.TrimSpace(output.String())
		if detail == "" {
			detail = err.Error()
		}
		logging.Warn("whisper.cpp output file missing/read failed: %s", truncateLogLine(detail, 1000))
		return "", fmt.Errorf("failed to read whisper output: %s", detail)
	}

	text := strings.TrimSpace(string(contents))
	if text == "" {
		return "", errors.New("no speech detected")
	}
	return text, nil
}

func shouldRetryWhisperWithoutGPU(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if text == "" {
		return false
	}
	if strings.Contains(text, "failed to read whisper output") && strings.Contains(text, "use gpu") {
		return true
	}
	if strings.Contains(text, "flash attn") && strings.Contains(text, "gpu") {
		return true
	}
	if strings.Contains(text, "metal") && strings.Contains(text, "gpu") {
		return true
	}
	return false
}

func summarizeWhisperArgs(args []string) string {
	parts := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		part := args[i]
		parts = append(parts, part)
	}
	return strings.Join(parts, " ")
}

func truncateLogLine(text string, max int) string {
	text = strings.TrimSpace(text)
	if max <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

func normalizePrompt(raw string) string {
	prompt := strings.Join(strings.Fields(raw), " ")
	if prompt == "" {
		return ""
	}
	runes := []rune(prompt)
	if len(runes) <= maxPromptRunes {
		return prompt
	}
	return string(runes[len(runes)-maxPromptRunes:])
}
