package integrationtools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/speechcache"
)

func writeFakeMLXTTSPythonHelper(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-local-speech-mlx.py")
	script := `#!/usr/bin/env python3
import argparse, struct, sys

parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd", required=True)
stt = sub.add_parser("stt")
stt.add_argument("--engine")
stt.add_argument("--audio")
stt.add_argument("--model")
tts = sub.add_parser("tts")
tts.add_argument("--engine")
tts.add_argument("--text")
tts.add_argument("--output")
tts.add_argument("--model")
tts.add_argument("--voice")
tts.add_argument("--lang-code")
tts.add_argument("--language", default="")
args = parser.parse_args()
if args.cmd == "tts":
    sample_rate = 8000
    seconds = 1
    samples = bytes([128] * sample_rate * seconds)
    data_size = len(samples)
    header = struct.pack(
        "<4sI4s4sIHHIIHH4sI",
        b"RIFF", 36 + data_size, b"WAVE", b"fmt ", 16, 1, 1,
        sample_rate, sample_rate, 1, 8, b"data", data_size,
    )
    with open(args.output, "wb") as fh:
        fh.write(header + samples)
    sys.exit(0)
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake python helper: %v", err)
	}
	return path
}

func writeFakeMLXTTSPythonRunner(t *testing.T, dir, helperPath string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-python3")
	script := "#!/bin/sh\nscript=\"$1\"\nshift\nexec \"$script\" \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake python runner: %v", err)
	}
	return path
}

func TestKokoroTTSToolExecute(t *testing.T) {
	tmpDir := t.TempDir()
	helper := writeFakeMLXTTSPythonHelper(t, tmpDir)
	python := writeFakeMLXTTSPythonRunner(t, tmpDir, helper)
	t.Setenv("AAGENT_SPEECH_PYTHON", python)
	t.Setenv("AAGENT_SPEECH_MLX_SCRIPT", helper)

	tool := NewKokoroTTSTool(speechcache.New(0))
	raw, err := json.Marshal(map[string]interface{}{
		"text":     "hello",
		"language": "en",
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Output, "Clip ID:") {
		t.Fatalf("expected clip id in output, got: %s", res.Output)
	}
	audioMeta, ok := res.Metadata["audio_clip"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing audio_clip metadata")
	}
	if strings.TrimSpace(asString(audioMeta["clip_id"])) == "" {
		t.Fatalf("missing clip_id in audio_clip metadata")
	}
	if asString(audioMeta["generated_with"]) != "kokoro_tts" {
		t.Fatalf("unexpected generated_with: %v", audioMeta["generated_with"])
	}
}

func TestQwen3TTSToolExecute(t *testing.T) {
	tmpDir := t.TempDir()
	helper := writeFakeMLXTTSPythonHelper(t, tmpDir)
	python := writeFakeMLXTTSPythonRunner(t, tmpDir, helper)
	t.Setenv("AAGENT_SPEECH_PYTHON", python)
	t.Setenv("AAGENT_SPEECH_MLX_SCRIPT", helper)

	tool := NewQwen3TTSTool(speechcache.New(0))
	raw, err := json.Marshal(map[string]interface{}{
		"text":     "hello",
		"language": "ru",
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	audioMeta, ok := res.Metadata["audio_clip"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing audio_clip metadata")
	}
	if asString(audioMeta["generated_with"]) != "qwen3_tts" {
		t.Fatalf("unexpected generated_with: %v", audioMeta["generated_with"])
	}
}
