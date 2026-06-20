package whispercpp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	BinaryPath      string
	ModelPath       string
	ModelName       string
	DefaultLanguage string
	Translate       bool
	Threads         int
	AutoSetup       bool
	AutoDownload    bool
}

type TranscribeOptions struct {
	Language           string
	TranslateToEnglish *bool
	Prompt             string
	Profile            string
	ModelName          string
}

func loadConfig(ctx context.Context, opts TranscribeOptions) (Config, error) {
	modelName := ""
	if explicitModelPath := strings.TrimSpace(os.Getenv("AAGENT_WHISPER_MODEL")); explicitModelPath != "" {
		modelName = filepath.Base(filepath.Clean(explicitModelPath))
	} else {
		resolvedModelName, err := resolveModelName(opts.ModelName, opts.Profile)
		if err != nil {
			return Config{}, err
		}
		modelName = resolvedModelName
	}

	cfg := Config{
		BinaryPath:      resolveBinaryPath(),
		ModelPath:       resolveModelPath(modelName),
		ModelName:       modelName,
		DefaultLanguage: strings.TrimSpace(os.Getenv("AAGENT_WHISPER_LANGUAGE")),
		Translate:       resolveTranslate(),
		Threads:         resolveThreads(),
		AutoSetup:       resolveAutoSetup(),
		AutoDownload:    resolveAutoDownload(),
	}

	if strings.TrimSpace(cfg.ModelPath) == "" && cfg.AutoDownload {
		path, err := ensureModelDownloaded(modelName)
		if err != nil {
			return Config{}, err
		}
		cfg.ModelPath = path
	}
	if strings.TrimSpace(cfg.ModelPath) == "" {
		return Config{}, errors.New("whisper.cpp model not found; set AAGENT_WHISPER_MODEL or AAGENT_WHISPER_MODEL_NAME (for example small or large-v3-turbo)")
	}
	if info, err := os.Stat(cfg.ModelPath); err != nil || info.IsDir() {
		return Config{}, fmt.Errorf("invalid AAGENT_WHISPER_MODEL path: %s", cfg.ModelPath)
	}

	if strings.TrimSpace(cfg.BinaryPath) == "" && cfg.AutoSetup {
		path, err := ensureBinaryAvailable(ctx)
		if err != nil {
			return Config{}, err
		}
		cfg.BinaryPath = path
	}
	if strings.TrimSpace(cfg.BinaryPath) == "" {
		return Config{}, errors.New("whisper.cpp binary not found; auto-setup failed. Set AAGENT_WHISPER_BIN or install whisper-cli in PATH")
	}
	if info, err := os.Stat(cfg.BinaryPath); err != nil || info.IsDir() {
		return Config{}, fmt.Errorf("invalid AAGENT_WHISPER_BIN path: %s", cfg.BinaryPath)
	}
	return cfg, nil
}

func resolveBinaryPath() string {
	if v := strings.TrimSpace(os.Getenv("AAGENT_WHISPER_BIN")); v != "" {
		return filepath.Clean(v)
	}
	if v, err := exec.LookPath("whisper-cli"); err == nil {
		return v
	}
	dataDir := resolveDataDir()
	candidates := []string{
		filepath.Join(dataDir, "speech", "whisper", "build", "bin", "whisper-cli"),
		filepath.Join(dataDir, "speech", "whisper", "whisper-cli"),
		filepath.Join(dataDir, "speech", "whisper", "bin", "whisper-cli"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func resolveModelPath(modelName string) string {
	if v := strings.TrimSpace(os.Getenv("AAGENT_WHISPER_MODEL")); v != "" {
		return filepath.Clean(v)
	}
	dataDir := resolveDataDir()
	candidates := []string{
		filepath.Join(dataDir, "speech", "whisper", "models", modelName),
		filepath.Join(dataDir, "speech", "whisper", modelName),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func resolveDataDir() string {
	if raw := strings.TrimSpace(os.Getenv("AAGENT_DATA_PATH")); raw != "" {
		return filepath.Clean(raw)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return filepath.Clean(filepath.Join(".", ".aagent-data"))
	}
	return filepath.Join(homeDir, ".local", "share", "aagent")
}

func resolveThreads() int {
	raw := strings.TrimSpace(os.Getenv("AAGENT_WHISPER_THREADS"))
	if raw == "" {
		return 0
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

func resolveAutoSetup() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("AAGENT_WHISPER_AUTO_SETUP")))
	switch raw {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func resolveAutoDownload() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("AAGENT_WHISPER_AUTO_DOWNLOAD")))
	switch raw {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func resolveTranslate() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("AAGENT_WHISPER_TRANSLATE")))
	switch raw {
	case "", "0", "false", "no", "off":
		return false
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func resolveNoGPU() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("AAGENT_WHISPER_NO_GPU")))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func normalizeLanguage(raw string) string {
	lang := strings.TrimSpace(strings.ToLower(raw))
	if lang == "" {
		return ""
	}
	if lang == "auto" {
		return "auto"
	}
	if strings.Contains(lang, "-") {
		parts := strings.SplitN(lang, "-", 2)
		lang = parts[0]
	}
	return lang
}
