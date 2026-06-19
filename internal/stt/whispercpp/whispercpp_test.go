package whispercpp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeLanguage(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: ""},
		{input: "  ", want: ""},
		{input: "en", want: "en"},
		{input: "ru-RU", want: "ru"},
		{input: "PT-BR", want: "pt"},
		{input: "auto", want: "auto"},
	}

	for _, tc := range tests {
		got := normalizeLanguage(tc.input)
		if got != tc.want {
			t.Fatalf("normalizeLanguage(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestResolveWhisperSourceDirCopiesModuleCacheToWritableDataDir(t *testing.T) {
	tmpDir := t.TempDir()
	moduleRoot := filepath.Join(tmpDir, "gomodcache")
	moduleSource := filepath.Join(moduleRoot, "github.com", "ggerganov", "whisper.cpp@v1.8.3")
	if err := os.MkdirAll(moduleSource, 0o755); err != nil {
		t.Fatalf("mkdir module source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(moduleSource, "CMakeLists.txt"), []byte("cmake_minimum_required(VERSION 3.5)\n"), 0o644); err != nil {
		t.Fatalf("write CMakeLists: %v", err)
	}
	if err := os.WriteFile(filepath.Join(moduleSource, "dummy.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write dummy file: %v", err)
	}
	// Simulate read-only module cache.
	if err := os.Chmod(moduleSource, 0o555); err != nil {
		t.Fatalf("chmod module source: %v", err)
	}
	defer os.Chmod(moduleSource, 0o755)

	dataDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}

	t.Setenv("GOMODCACHE", moduleRoot)
	t.Setenv("AAGENT_DATA_PATH", dataDir)
	t.Setenv("AAGENT_WHISPER_SOURCE", "")

	got, err := resolveWhisperSourceDir()
	if err != nil {
		t.Fatalf("resolveWhisperSourceDir failed: %v", err)
	}
	if got == moduleSource {
		t.Fatalf("expected writable copied source, got module cache path: %s", got)
	}
	wantPrefix := filepath.Join(dataDir, "speech", "whisper", "source")
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("expected source under %s, got %s", wantPrefix, got)
	}
	if _, err := os.Stat(filepath.Join(got, "CMakeLists.txt")); err != nil {
		t.Fatalf("copied source missing CMakeLists.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(got, ".writable-check"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("copied source is not writable: %v", err)
	}
}

func TestResetBuildDirIfSourceMismatch(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatalf("mkdir build dir: %v", err)
	}

	cache := "CMAKE_HOME_DIRECTORY:INTERNAL=/tmp/old-source\nOTHER:STRING=value\n"
	if err := os.WriteFile(filepath.Join(buildDir, "CMakeCache.txt"), []byte(cache), 0o644); err != nil {
		t.Fatalf("write CMakeCache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "stale.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	if err := resetBuildDirIfSourceMismatch(buildDir, "/tmp/new-source"); err != nil {
		t.Fatalf("resetBuildDirIfSourceMismatch failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(buildDir, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected build dir to be reset; stale marker still exists err=%v", err)
	}
	if _, err := os.Stat(buildDir); err != nil {
		t.Fatalf("expected build dir recreated: %v", err)
	}
}

func TestResolveTranslate(t *testing.T) {
	t.Setenv("AAGENT_WHISPER_TRANSLATE", "true")
	if !resolveTranslate() {
		t.Fatalf("expected resolveTranslate=true for true")
	}
	t.Setenv("AAGENT_WHISPER_TRANSLATE", "1")
	if !resolveTranslate() {
		t.Fatalf("expected resolveTranslate=true for 1")
	}
	t.Setenv("AAGENT_WHISPER_TRANSLATE", "false")
	if resolveTranslate() {
		t.Fatalf("expected resolveTranslate=false for false")
	}
}

func TestResolveModelNameDefaultsToSmall(t *testing.T) {
	t.Setenv("AAGENT_WHISPER_MODEL_NAME", "")
	t.Setenv("AAGENT_WHISPER_MEETING_MODEL_NAME", "")

	got, err := resolveModelName("", "")
	if err != nil {
		t.Fatalf("resolveModelName failed: %v", err)
	}
	if got != "ggml-small.bin" {
		t.Fatalf("resolveModelName default = %q, want ggml-small.bin", got)
	}
}

func TestResolveModelNameUsesMeetingTurboDefault(t *testing.T) {
	t.Setenv("AAGENT_WHISPER_MODEL_NAME", "")
	t.Setenv("AAGENT_WHISPER_MEETING_MODEL_NAME", "")

	got, err := resolveModelName("", "meeting")
	if err != nil {
		t.Fatalf("resolveModelName failed: %v", err)
	}
	if got != "ggml-large-v3-turbo.bin" {
		t.Fatalf("resolveModelName meeting default = %q, want ggml-large-v3-turbo.bin", got)
	}
}

func TestResolveModelNameAllowsEnvOverride(t *testing.T) {
	t.Setenv("AAGENT_WHISPER_MODEL_NAME", "base")
	t.Setenv("AAGENT_WHISPER_MEETING_MODEL_NAME", "")

	got, err := resolveModelName("", "")
	if err != nil {
		t.Fatalf("resolveModelName failed: %v", err)
	}
	if got != "ggml-base.bin" {
		t.Fatalf("resolveModelName env override = %q, want ggml-base.bin", got)
	}
}

func TestResolveModelNameAllowsMeetingEnvOverride(t *testing.T) {
	t.Setenv("AAGENT_WHISPER_MODEL_NAME", "base")
	t.Setenv("AAGENT_WHISPER_MEETING_MODEL_NAME", "medium")

	got, err := resolveModelName("", "meeting")
	if err != nil {
		t.Fatalf("resolveModelName failed: %v", err)
	}
	if got != "ggml-medium.bin" {
		t.Fatalf("resolveModelName meeting env override = %q, want ggml-medium.bin", got)
	}
}

func TestLoadConfigExplicitModelPathBypassesModelNameValidation(t *testing.T) {
	tmpDir := t.TempDir()
	modelPath := filepath.Join(tmpDir, "custom-model.bin")
	if err := os.WriteFile(modelPath, []byte("model"), 0o644); err != nil {
		t.Fatalf("write fake model: %v", err)
	}
	binaryPath := filepath.Join(tmpDir, "whisper-cli")
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	t.Setenv("AAGENT_WHISPER_MODEL", modelPath)
	t.Setenv("AAGENT_WHISPER_MODEL_NAME", "not-a-real-model")
	t.Setenv("AAGENT_WHISPER_BIN", binaryPath)
	t.Setenv("AAGENT_WHISPER_AUTO_SETUP", "0")
	t.Setenv("AAGENT_WHISPER_AUTO_DOWNLOAD", "0")

	cfg, err := loadConfig(context.Background(), TranscribeOptions{})
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	if cfg.ModelPath != modelPath {
		t.Fatalf("loadConfig ModelPath = %q, want %q", cfg.ModelPath, modelPath)
	}
	if cfg.ModelName != "custom-model.bin" {
		t.Fatalf("loadConfig ModelName = %q, want custom-model.bin", cfg.ModelName)
	}
}

func TestNormalizePromptKeepsRecentContext(t *testing.T) {
	var builder strings.Builder
	for i := 0; i < maxPromptRunes+20; i++ {
		builder.WriteString("a")
	}
	builder.WriteString(" final words")

	got := normalizePrompt(builder.String())
	if len([]rune(got)) != maxPromptRunes {
		t.Fatalf("normalizePrompt length = %d, want %d", len([]rune(got)), maxPromptRunes)
	}
	if !strings.HasSuffix(got, "final words") {
		t.Fatalf("normalizePrompt should keep recent prompt context, got suffix %q", got[len(got)-20:])
	}
}

func TestResolveCMakeBinaryUsesExplicitEnvPath(t *testing.T) {
	tmpDir := t.TempDir()
	cmakePath := filepath.Join(tmpDir, "cmake")
	if err := os.WriteFile(cmakePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake cmake: %v", err)
	}

	t.Setenv("AAGENT_CMAKE_BIN", cmakePath)
	got, err := resolveCMakeBinary()
	if err != nil {
		t.Fatalf("resolveCMakeBinary failed: %v", err)
	}
	if got != cmakePath {
		t.Fatalf("resolveCMakeBinary = %q, want %q", got, cmakePath)
	}
}

func TestShouldRetryWhisperWithoutGPU(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "retry on read output with gpu marker",
			err:  errors.New("failed to read whisper output: whisper_init_with_params_no_state: use gpu    = 1"),
			want: true,
		},
		{
			name: "retry on flash attn gpu marker",
			err:  errors.New("whisper.cpp failed: flash attn unsupported on gpu backend"),
			want: true,
		},
		{
			name: "retry on metal gpu marker",
			err:  errors.New("whisper.cpp failed: metal gpu init failed"),
			want: true,
		},
		{
			name: "do not retry generic error",
			err:  errors.New("whisper.cpp failed: model not found"),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldRetryWhisperWithoutGPU(tc.err)
			if got != tc.want {
				t.Fatalf("shouldRetryWhisperWithoutGPU() = %v, want %v", got, tc.want)
			}
		})
	}
}
