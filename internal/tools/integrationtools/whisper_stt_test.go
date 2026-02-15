package integrationtools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWhisperSTTToolExecuteWithAudioPath(t *testing.T) {
	tmpDir := t.TempDir()
	modelPath := filepath.Join(tmpDir, "ggml-tiny.bin")
	if err := os.WriteFile(modelPath, []byte("fake-model"), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}

	whisperBin := filepath.Join(tmpDir, "whisper-cli")
	if err := os.WriteFile(whisperBin, []byte(`#!/bin/sh
set -eu
OUT=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -of) OUT="$2"; shift 2 ;;
    *) shift ;;
  esac
done
/bin/echo "hello from file path" > "${OUT}.txt"
`), 0o755); err != nil {
		t.Fatalf("write fake whisper-cli: %v", err)
	}

	audioPath := filepath.Join(tmpDir, "sample.wav")
	if err := os.WriteFile(audioPath, []byte("fake-audio"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	t.Setenv("AAGENT_WHISPER_BIN", whisperBin)
	t.Setenv("AAGENT_WHISPER_MODEL", modelPath)
	t.Setenv("AAGENT_WHISPER_AUTO_SETUP", "0")
	t.Setenv("AAGENT_WHISPER_AUTO_DOWNLOAD", "0")

	tool := NewWhisperSTTTool(tmpDir)
	raw, err := json.Marshal(map[string]interface{}{
		"audio_path": filepath.Base(audioPath),
		"language":   "en-US",
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("execute returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	if !strings.Contains(res.Output, "hello from file path") {
		t.Fatalf("expected transcript in output, got: %s", res.Output)
	}
	gotTranscript, _ := res.Metadata["transcript"].(string)
	if gotTranscript != "hello from file path" {
		t.Fatalf("unexpected transcript metadata: %q", gotTranscript)
	}
}

func TestWhisperSTTToolExecuteWithBase64AudioAndOutputFile(t *testing.T) {
	tmpDir := t.TempDir()
	modelPath := filepath.Join(tmpDir, "ggml-tiny.bin")
	if err := os.WriteFile(modelPath, []byte("fake-model"), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}

	whisperBin := filepath.Join(tmpDir, "whisper-cli")
	if err := os.WriteFile(whisperBin, []byte(`#!/bin/sh
set -eu
OUT=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -of) OUT="$2"; shift 2 ;;
    *) shift ;;
  esac
done
/bin/echo "hello from base64" > "${OUT}.txt"
`), 0o755); err != nil {
		t.Fatalf("write fake whisper-cli: %v", err)
	}

	t.Setenv("AAGENT_WHISPER_BIN", whisperBin)
	t.Setenv("AAGENT_WHISPER_MODEL", modelPath)
	t.Setenv("AAGENT_WHISPER_AUTO_SETUP", "0")
	t.Setenv("AAGENT_WHISPER_AUTO_DOWNLOAD", "0")

	encoded := base64.StdEncoding.EncodeToString([]byte("fake-audio-binary"))
	outPath := filepath.Join("out", "transcript.txt")

	tool := NewWhisperSTTTool(tmpDir)
	raw, err := json.Marshal(map[string]interface{}{
		"audio_bytes_base64": encoded,
		"output_path":        outPath,
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("execute returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}

	absOut := filepath.Join(tmpDir, outPath)
	data, err := os.ReadFile(absOut)
	if err != nil {
		t.Fatalf("read transcript file: %v", err)
	}
	if strings.TrimSpace(string(data)) != "hello from base64" {
		t.Fatalf("unexpected transcript file contents: %q", string(data))
	}
	if gotPath, _ := res.Metadata["output_path"].(string); filepath.Clean(gotPath) != filepath.Clean(absOut) {
		t.Fatalf("unexpected output_path metadata: %q", gotPath)
	}
}
