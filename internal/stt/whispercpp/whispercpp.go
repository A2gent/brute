package whispercpp

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultModelName       = "ggml-tiny.bin"
	defaultModelDownload   = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-tiny.bin"
	defaultSourceVersion   = "v1.8.3"
	defaultSourceDownload  = "https://github.com/ggml-org/whisper.cpp/archive/refs/tags/v1.8.3.tar.gz"
	maxModelDownloadBytes  = 1024 * 1024 * 1024
	maxSourceDownloadBytes = 1024 * 1024 * 1024
	defaultDownloadTimeout = 30 * time.Minute
)

type Config struct {
	BinaryPath      string
	ModelPath       string
	DefaultLanguage string
	Translate       bool
	Threads         int
	AutoSetup       bool
	AutoDownload    bool
}

func Transcribe(ctx context.Context, audioPath string, language string) (string, error) {
	return TranscribeWithOptions(ctx, audioPath, language, nil)
}

func TranscribeWithOptions(ctx context.Context, audioPath string, language string, translateToEnglish *bool) (string, error) {
	cfg, err := loadConfig(ctx)
	if err != nil {
		return "", err
	}

	lang := normalizeLanguage(language)
	if lang == "" {
		lang = normalizeLanguage(cfg.DefaultLanguage)
	}
	if lang == "" {
		lang = "auto"
	}
	translate := cfg.Translate
	if translateToEnglish != nil {
		translate = *translateToEnglish
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
	if lang != "auto" {
		args = append(args, "-l", lang)
	}
	if cfg.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(cfg.Threads))
	}
	if translate {
		args = append(args, "-tr")
	}

	cmd := exec.CommandContext(ctx, cfg.BinaryPath, args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(output.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("whisper.cpp failed: %s", detail)
	}

	contents, err := os.ReadFile(outputPrefix + ".txt")
	if err != nil {
		detail := strings.TrimSpace(output.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("failed to read whisper output: %s", detail)
	}

	text := strings.TrimSpace(string(contents))
	if text == "" {
		return "", errors.New("no speech detected")
	}
	return text, nil
}

func loadConfig(ctx context.Context) (Config, error) {
	cfg := Config{
		BinaryPath:      resolveBinaryPath(),
		ModelPath:       resolveModelPath(),
		DefaultLanguage: strings.TrimSpace(os.Getenv("AAGENT_WHISPER_LANGUAGE")),
		Translate:       resolveTranslate(),
		Threads:         resolveThreads(),
		AutoSetup:       resolveAutoSetup(),
		AutoDownload:    resolveAutoDownload(),
	}

	if strings.TrimSpace(cfg.ModelPath) == "" && cfg.AutoDownload {
		path, err := ensureModelDownloaded()
		if err != nil {
			return Config{}, err
		}
		cfg.ModelPath = path
	}
	if strings.TrimSpace(cfg.ModelPath) == "" {
		return Config{}, errors.New("whisper.cpp model not found; set AAGENT_WHISPER_MODEL (for example ggml-base.bin)")
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

func ensureBinaryAvailable(ctx context.Context) (string, error) {
	dataDir := resolveDataDir()
	buildDir := filepath.Join(dataDir, "speech", "whisper", "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return "", fmt.Errorf("failed creating whisper build directory: %w", err)
	}
	binaryPath := filepath.Join(buildDir, "bin", "whisper-cli")
	if info, err := os.Stat(binaryPath); err == nil && !info.IsDir() {
		return binaryPath, nil
	}

	sourceDir, err := resolveWhisperSourceDir()
	if err != nil {
		return "", err
	}
	if err := resetBuildDirIfSourceMismatch(buildDir, sourceDir); err != nil {
		return "", err
	}

	cmakeArgs := []string{
		"-S", sourceDir,
		"-B", buildDir,
		"-DCMAKE_BUILD_TYPE=Release",
		"-DWHISPER_BUILD_TESTS=OFF",
	}
	if err := runCommand(ctx, "cmake", cmakeArgs...); err != nil {
		return "", fmt.Errorf("failed configuring whisper.cpp build: %w", err)
	}
	if err := runCommand(ctx, "cmake", "--build", buildDir, "--config", "Release", "--target", "whisper-cli", "-j"); err != nil {
		return "", fmt.Errorf("failed building whisper-cli: %w", err)
	}

	if info, err := os.Stat(binaryPath); err != nil || info.IsDir() {
		return "", errors.New("whisper-cli build completed but binary was not found")
	}
	return binaryPath, nil
}

func resetBuildDirIfSourceMismatch(buildDir string, sourceDir string) error {
	cachedSource, ok, err := readCMakeHomeDirectory(buildDir)
	if err != nil {
		return fmt.Errorf("failed reading cmake cache: %w", err)
	}
	if !ok {
		return nil
	}

	buildSource := filepath.Clean(cachedSource)
	currentSource := filepath.Clean(sourceDir)
	if buildSource == currentSource {
		return nil
	}

	if err := os.RemoveAll(buildDir); err != nil {
		return fmt.Errorf("failed to reset stale whisper build directory: %w", err)
	}
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return fmt.Errorf("failed to recreate whisper build directory: %w", err)
	}
	return nil
}

func readCMakeHomeDirectory(buildDir string) (string, bool, error) {
	cachePath := filepath.Join(buildDir, "CMakeCache.txt")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		const key = "CMAKE_HOME_DIRECTORY:INTERNAL="
		if strings.HasPrefix(line, key) {
			value := strings.TrimSpace(strings.TrimPrefix(line, key))
			if value == "" {
				return "", false, nil
			}
			return value, true, nil
		}
	}
	return "", false, nil
}

func resolveWhisperSourceDir() (string, error) {
	if raw := strings.TrimSpace(os.Getenv("AAGENT_WHISPER_SOURCE")); raw != "" {
		source := filepath.Clean(raw)
		if info, err := os.Stat(filepath.Join(source, "CMakeLists.txt")); err == nil && !info.IsDir() {
			return source, nil
		}
		return "", fmt.Errorf("invalid AAGENT_WHISPER_SOURCE: %s", source)
	}

	moduleRoot := resolveModuleCacheRoot()
	matches, err := filepath.Glob(filepath.Join(moduleRoot, "github.com", "ggerganov", "whisper.cpp@*"))
	if err != nil {
		return "", fmt.Errorf("failed to resolve whisper.cpp source in module cache: %w", err)
	}
	if len(matches) == 0 {
		source, dlErr := ensureSourceDownloaded()
		if dlErr == nil {
			return source, nil
		}
		return "", fmt.Errorf("whisper.cpp source not found in module cache and auto-download failed: %w", dlErr)
	}
	sort.Strings(matches)
	moduleSource := matches[len(matches)-1]
	if info, err := os.Stat(filepath.Join(moduleSource, "CMakeLists.txt")); err != nil || info.IsDir() {
		return "", errors.New("whisper.cpp source directory is missing CMakeLists.txt")
	}
	return ensureWritableSourceFromLocal(moduleSource)
}

func ensureWritableSourceFromLocal(source string) (string, error) {
	source = filepath.Clean(source)
	base := filepath.Base(source)
	if base == "." || base == string(filepath.Separator) || strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("invalid whisper.cpp source path: %s", source)
	}

	dataDir := resolveDataDir()
	baseDir := filepath.Join(dataDir, "speech", "whisper", "source")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create whisper source directory: %w", err)
	}
	target := filepath.Join(baseDir, base)
	if info, err := os.Stat(filepath.Join(target, "CMakeLists.txt")); err == nil && !info.IsDir() {
		return target, nil
	}

	tmp := target + ".copy"
	_ = os.RemoveAll(tmp)
	if err := copyDir(source, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("failed to copy whisper.cpp source to writable directory: %w", err)
	}
	_ = os.RemoveAll(target)
	if err := os.Rename(tmp, target); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("failed to finalize writable whisper.cpp source copy: %w", err)
	}
	return target, nil
}

func ensureSourceDownloaded() (string, error) {
	dataDir := resolveDataDir()
	baseDir := filepath.Join(dataDir, "speech", "whisper", "source")
	sourceDir := filepath.Join(baseDir, "whisper.cpp-"+defaultSourceVersion)
	if info, err := os.Stat(filepath.Join(sourceDir, "CMakeLists.txt")); err == nil && !info.IsDir() {
		return sourceDir, nil
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create whisper source directory: %w", err)
	}

	tmpTar := filepath.Join(baseDir, "whisper.cpp.tar.gz.download")
	if err := downloadFileLimited(defaultSourceDownload, tmpTar, maxSourceDownloadBytes); err != nil {
		_ = os.Remove(tmpTar)
		return "", fmt.Errorf("failed to download whisper.cpp source: %w", err)
	}
	defer os.Remove(tmpTar)

	extractDir := filepath.Join(baseDir, ".extract")
	_ = os.RemoveAll(extractDir)
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create extraction directory: %w", err)
	}
	if err := extractTarGz(tmpTar, extractDir); err != nil {
		return "", fmt.Errorf("failed to extract whisper.cpp source: %w", err)
	}

	target := filepath.Join(extractDir, "whisper.cpp-"+strings.TrimPrefix(defaultSourceVersion, "v"))
	if info, err := os.Stat(filepath.Join(target, "CMakeLists.txt")); err != nil || info.IsDir() {
		target = filepath.Join(extractDir, "whisper.cpp-"+defaultSourceVersion)
	}
	if info, err := os.Stat(filepath.Join(target, "CMakeLists.txt")); err != nil || info.IsDir() {
		return "", errors.New("downloaded whisper.cpp source did not contain CMakeLists.txt")
	}

	_ = os.RemoveAll(sourceDir)
	if err := os.Rename(target, sourceDir); err != nil {
		return "", fmt.Errorf("failed to finalize whisper source: %w", err)
	}
	return sourceDir, nil
}

func extractTarGz(path string, destination string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if name == "." || strings.HasPrefix(name, "..") || strings.Contains(name, "../") {
			continue
		}
		target := filepath.Join(destination, name)
		if !strings.HasPrefix(target, destination) {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyDir(src string, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()
		dstFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer dstFile.Close()
		if _, err := io.Copy(dstFile, srcFile); err != nil {
			return err
		}
		return nil
	})
}

func runCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(output.String())
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("%s %s failed: %s", name, strings.Join(args, " "), detail)
	}
	return nil
}

func resolveModuleCacheRoot() string {
	if raw := strings.TrimSpace(os.Getenv("GOMODCACHE")); raw != "" {
		return filepath.Clean(raw)
	}
	if raw := strings.TrimSpace(os.Getenv("GOPATH")); raw != "" {
		for _, p := range strings.Split(raw, string(os.PathListSeparator)) {
			p = strings.TrimSpace(p)
			if p != "" {
				return filepath.Join(filepath.Clean(p), "pkg", "mod")
			}
		}
	}
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return filepath.Clean(filepath.Join(".", "pkg", "mod"))
	}
	return filepath.Join(homeDir, "go", "pkg", "mod")
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

func resolveModelPath() string {
	if v := strings.TrimSpace(os.Getenv("AAGENT_WHISPER_MODEL")); v != "" {
		return filepath.Clean(v)
	}
	dataDir := resolveDataDir()
	candidates := []string{
		filepath.Join(dataDir, "speech", "whisper", "models", defaultModelName),
		filepath.Join(dataDir, "speech", "whisper", defaultModelName),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func ensureModelDownloaded() (string, error) {
	dataDir := resolveDataDir()
	modelsDir := filepath.Join(dataDir, "speech", "whisper", "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create whisper model directory: %w", err)
	}
	path := filepath.Join(modelsDir, defaultModelName)
	if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Size() > 0 {
		return path, nil
	}

	tmpPath := path + ".download"
	if err := downloadFileLimited(defaultModelDownload, tmpPath, maxModelDownloadBytes); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to auto-download whisper model: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to finalize whisper model download: %w", err)
	}
	return path, nil
}

func downloadFileLimited(url string, destination string, maxBytes int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultDownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: defaultDownloadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected HTTP status: %s", resp.Status)
	}

	f, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer f.Close()

	limited := io.LimitReader(resp.Body, maxBytes+1)
	n, err := io.Copy(f, limited)
	if err != nil {
		return err
	}
	if n > maxBytes {
		return errors.New("download exceeded size limit")
	}
	return nil
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
