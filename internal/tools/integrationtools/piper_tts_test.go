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

func TestPiperTTSToolExecuteWithNativeBinary(t *testing.T) {
	tmpDir := t.TempDir()

	modelPath := filepath.Join(tmpDir, "voice.onnx")
	if err := os.WriteFile(modelPath, []byte("fake-model"), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}

	piperBin := filepath.Join(tmpDir, "piper")
	if err := os.WriteFile(piperBin, []byte(`#!/bin/sh
set -eu
OUTPUT=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output_file) OUTPUT="$2"; shift 2 ;;
    *) shift ;;
  esac
done
/bin/mkdir -p "$(dirname "$OUTPUT")"
/bin/echo "fake-audio" > "$OUTPUT"
`), 0o755); err != nil {
		t.Fatalf("write fake piper: %v", err)
	}

	tool := NewPiperTTSTool(tmpDir, speechcache.New(0))
	params := map[string]interface{}{
		"text":        "hello",
		"piper_bin":   piperBin,
		"model_path":  modelPath,
		"output_mode": "stream",
	}
	raw, err := json.Marshal(params)
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
}

func TestPiperTTSToolExecuteAutoDownloadsVoice(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	t.Setenv("AAGENT_DATA_PATH", dataDir)
	t.Setenv("PATH", tmpDir)

	python3Path := filepath.Join(tmpDir, "python3")
	if err := os.WriteFile(python3Path, []byte(`#!/bin/sh
set -eu
if [ "$1" = "-m" ] && [ "$2" = "venv" ]; then
  VENV_DIR="$3"
  /bin/mkdir -p "$VENV_DIR/bin"
  /bin/cat > "$VENV_DIR/bin/python" <<'PYEOF'
#!/bin/sh
set -eu
if [ "$1" = "-m" ] && [ "$2" = "pip" ]; then
  exit 0
fi
if [ "$1" = "-m" ] && [ "$2" = "piper" ] && [ "${3:-}" = "--help" ]; then
  exit 0
fi
if [ "$1" = "-m" ] && [ "$2" = "piper.download_voices" ]; then
  shift 2
  DOWNLOAD_DIR=""
  VOICE=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --download-dir|--download_dir|--data-dir|--data_dir)
        DOWNLOAD_DIR="$2"; shift 2 ;;
      *)
        VOICE="$1"; shift ;;
    esac
  done
  /bin/mkdir -p "$DOWNLOAD_DIR"
  /usr/bin/touch "$DOWNLOAD_DIR/.voice-$VOICE-downloaded"
  exit 0
fi
if [ "$1" = "-m" ] && [ "$2" = "piper" ]; then
  shift 2
  MODEL=""
  OUTPUT=""
  MODELS_DIR=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --model) MODEL="$2"; shift 2 ;;
      --output_file) OUTPUT="$2"; shift 2 ;;
      --download-dir|--download_dir|--data-dir|--data_dir) MODELS_DIR="$2"; shift 2 ;;
      *) shift ;;
    esac
  done
  if [ -z "${MODEL##*.onnx}" ]; then
    :
  else
    if [ ! -f "$MODELS_DIR/.voice-$MODEL-downloaded" ]; then
      echo "ValueError: Unable to find voice: $MODEL (use piper.download_voices)" >&2
      exit 1
    fi
  fi
  /bin/mkdir -p "$(/usr/bin/dirname "$OUTPUT")"
  /bin/echo "fake-audio" > "$OUTPUT"
  exit 0
fi
exit 1
PYEOF
  /bin/chmod +x "$VENV_DIR/bin/python"
  exit 0
fi
exit 1
`), 0o755); err != nil {
		t.Fatalf("write fake python3: %v", err)
	}

	tool := NewPiperTTSTool(tmpDir, speechcache.New(0))
	params := map[string]interface{}{
		"text":        "hello with voice id",
		"model_path":  "en_US-lessac-medium",
		"output_mode": "stream",
	}
	raw, err := json.Marshal(params)
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
	if downloaded, _ := res.Metadata["voice_downloaded"].(bool); !downloaded {
		t.Fatalf("expected voice_downloaded=true, metadata=%v", res.Metadata)
	}
	modelsDir := filepath.Join(dataDir, defaultPiperModelsRel)
	downloadMarker := filepath.Join(modelsDir, ".voice-en_US-lessac-medium-downloaded")
	if _, err := os.Stat(downloadMarker); err != nil {
		t.Fatalf("expected downloaded voice marker %s: %v", downloadMarker, err)
	}
}

func TestPiperTTSToolExecuteAutoSelectsLanguageModel(t *testing.T) {
	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	t.Setenv("AAGENT_DATA_PATH", dataDir)
	t.Setenv("PATH", tmpDir)

	python3Path := filepath.Join(tmpDir, "python3")
	if err := os.WriteFile(python3Path, []byte(`#!/bin/sh
set -eu
if [ "$1" = "-m" ] && [ "$2" = "venv" ]; then
  VENV_DIR="$3"
  /bin/mkdir -p "$VENV_DIR/bin"
  /bin/cat > "$VENV_DIR/bin/python" <<'PYEOF'
#!/bin/sh
set -eu
if [ "$1" = "-m" ] && [ "$2" = "pip" ]; then
  exit 0
fi
if [ "$1" = "-m" ] && [ "$2" = "piper" ] && [ "${3:-}" = "--help" ]; then
  exit 0
fi
if [ "$1" = "-m" ] && [ "$2" = "piper.download_voices" ]; then
  shift 2
  DOWNLOAD_DIR=""
  VOICE=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --download-dir|--download_dir|--data-dir|--data_dir)
        DOWNLOAD_DIR="$2"; shift 2 ;;
      *)
        VOICE="$1"; shift ;;
    esac
  done
  /bin/mkdir -p "$DOWNLOAD_DIR"
  /usr/bin/touch "$DOWNLOAD_DIR/.voice-$VOICE-downloaded"
  /usr/bin/touch "$DOWNLOAD_DIR/$VOICE.onnx"
  /usr/bin/touch "$DOWNLOAD_DIR/$VOICE.onnx.json"
  exit 0
fi
if [ "$1" = "-m" ] && [ "$2" = "piper" ]; then
  shift 2
  MODEL=""
  OUTPUT=""
  MODELS_DIR=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --model) MODEL="$2"; shift 2 ;;
      --output_file) OUTPUT="$2"; shift 2 ;;
      --download-dir|--download_dir|--data-dir|--data_dir) MODELS_DIR="$2"; shift 2 ;;
      *) shift ;;
    esac
  done
  if [ -z "${MODEL##*.onnx}" ]; then
    :
  else
    if [ ! -f "$MODELS_DIR/.voice-$MODEL-downloaded" ]; then
      echo "ValueError: Unable to find voice: $MODEL (use piper.download_voices)" >&2
      exit 1
    fi
  fi
  /bin/mkdir -p "$(/usr/bin/dirname "$OUTPUT")"
  /bin/echo "fake-audio" > "$OUTPUT"
  exit 0
fi
exit 1
PYEOF
  /bin/chmod +x "$VENV_DIR/bin/python"
  exit 0
fi
exit 1
`), 0o755); err != nil {
		t.Fatalf("write fake python3: %v", err)
	}

	tool := NewPiperTTSTool(tmpDir, speechcache.New(0))
	params := map[string]interface{}{
		"text":        "Привет! Как дела?",
		"output_mode": "stream",
	}
	raw, err := json.Marshal(params)
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

	modelSelection, ok := res.Metadata["model_selection"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected model_selection metadata, got: %v", res.Metadata)
	}
	if source := asString(modelSelection["source"]); source != "auto_language" {
		t.Fatalf("expected auto_language source, got: %s", source)
	}
	if selected := asString(res.Metadata["selected_language"]); selected != "ru" {
		t.Fatalf("expected selected_language=ru, got: %s", selected)
	}

	modelsDir := filepath.Join(dataDir, defaultPiperModelsRel)
	downloadMarker := filepath.Join(modelsDir, ".voice-ru_RU-ruslan-medium-downloaded")
	if _, err := os.Stat(downloadMarker); err != nil {
		t.Fatalf("expected downloaded voice marker %s: %v", downloadMarker, err)
	}
}

func asString(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	default:
		return ""
	}
}
