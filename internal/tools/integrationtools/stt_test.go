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

func writeFakeSTTPythonHelper(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-local-speech-mlx.py")
	script := `#!/usr/bin/env python3
import argparse, json, sys

parser = argparse.ArgumentParser()
sub = parser.add_subparsers(dest="cmd", required=True)
stt = sub.add_parser("stt")
stt.add_argument("--engine")
stt.add_argument("--audio")
stt.add_argument("--model")
stt.add_argument("--language", default="")
tts = sub.add_parser("tts")
tts.add_argument("--engine")
tts.add_argument("--text")
tts.add_argument("--output")
tts.add_argument("--model")
tts.add_argument("--voice")
tts.add_argument("--lang-code")
tts.add_argument("--language", default="")
args = parser.parse_args()
if args.cmd == "stt":
    print(json.dumps({"text": "fake transcript from " + args.engine}))
    sys.exit(0)
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake python helper: %v", err)
	}
	return path
}

func writeFakeSTTPythonRunner(t *testing.T, dir, helperPath string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-python3")
	script := "#!/bin/sh\nscript=\"$1\"\nshift\nexec \"$script\" \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake python runner: %v", err)
	}
	return path
}

func writeFakeWhisperKitCLIForSTT(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-whisperkit-cli")
	script := `#!/bin/sh
set -eu
report_dir=""
audio_path=""
while [ $# -gt 0 ]; do
  case "$1" in
    --report-path) report_dir="$2"; shift 2;;
    --audio-path) audio_path="$2"; shift 2;;
    *) shift;;
  esac
done
base="$(basename "$audio_path")"
base="${base%.*}"
mkdir -p "$report_dir"
printf '{"text":"whisperkit transcript"}' > "$report_dir/$base.json"
echo "whisperkit transcript"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake whisperkit cli: %v", err)
	}
	return path
}

func TestSTTToolExecuteParakeetDefault(t *testing.T) {
	tmpDir := t.TempDir()
	audioPath := filepath.Join(tmpDir, "sample.wav")
	if err := os.WriteFile(audioPath, []byte("fake-audio"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	helper := writeFakeSTTPythonHelper(t, tmpDir)
	python := writeFakeSTTPythonRunner(t, tmpDir, helper)
	t.Setenv("AAGENT_SPEECH_PYTHON", python)
	t.Setenv("AAGENT_SPEECH_MLX_SCRIPT", helper)
	t.Setenv("AAGENT_STT_ENGINE", "")

	tool := NewSTTTool(tmpDir)
	raw, err := json.Marshal(map[string]interface{}{
		"audio_path": filepath.Base(audioPath),
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
	if gotEngine, _ := res.Metadata["engine"].(string); gotEngine != "parakeet" {
		t.Fatalf("expected parakeet engine, got %q", gotEngine)
	}
	if !strings.Contains(res.Output, "fake transcript from parakeet") {
		t.Fatalf("expected transcript in output, got: %s", res.Output)
	}
}

func TestSTTToolExecuteWhisperKitEngine(t *testing.T) {
	tmpDir := t.TempDir()
	audioPath := filepath.Join(tmpDir, "sample.wav")
	if err := os.WriteFile(audioPath, []byte("fake-audio"), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	cli := writeFakeWhisperKitCLIForSTT(t, tmpDir)
	t.Setenv("AAGENT_SPEECH_WHISPERKIT_BIN", cli)

	tool := NewSTTTool(tmpDir)
	raw, err := json.Marshal(map[string]interface{}{
		"audio_path": filepath.Base(audioPath),
		"engine":     "whisperkit",
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
	if gotEngine, _ := res.Metadata["engine"].(string); gotEngine != "whisperkit" {
		t.Fatalf("expected whisperkit engine, got %q", gotEngine)
	}
}

func TestSTTToolExecuteWhisperCPPEngine(t *testing.T) {
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
/bin/echo "hello from whisper_cpp" > "${OUT}.txt"
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

	tool := NewSTTTool(tmpDir)
	raw, err := json.Marshal(map[string]interface{}{
		"audio_path": filepath.Base(audioPath),
		"engine":     "whisper_cpp",
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
	if gotEngine, _ := res.Metadata["engine"].(string); gotEngine != "whisper_cpp" {
		t.Fatalf("expected whisper_cpp engine, got %q", gotEngine)
	}
}

func TestSTTToolExecuteWithBase64Audio(t *testing.T) {
	tmpDir := t.TempDir()
	helper := writeFakeSTTPythonHelper(t, tmpDir)
	python := writeFakeSTTPythonRunner(t, tmpDir, helper)
	t.Setenv("AAGENT_SPEECH_PYTHON", python)
	t.Setenv("AAGENT_SPEECH_MLX_SCRIPT", helper)

	encoded := base64.StdEncoding.EncodeToString([]byte("fake-audio-binary"))
	tool := NewSTTTool(tmpDir)
	raw, err := json.Marshal(map[string]interface{}{
		"audio_bytes_base64": encoded,
		"engine":             "moonshine",
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
	if gotEngine, _ := res.Metadata["engine"].(string); gotEngine != "moonshine" {
		t.Fatalf("expected moonshine engine, got %q", gotEngine)
	}
}
