#!/usr/bin/env bash
set -euo pipefail

echo "Checking local speech dependencies for brute (mlx-audio + WhisperKit)..."

if [ "$(uname -s)" = "Darwin" ] && [ "$(uname -m)" != "arm64" ]; then
  echo "Note: mlx-audio engines require Apple Silicon (arm64). whisperkit may still work on Intel Macs."
fi

missing=()
python="${AAGENT_SPEECH_PYTHON:-python3}"
for cmd in "$python" ffmpeg; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    missing+=("$cmd")
  fi
done

if [ ${#missing[@]} -gt 0 ]; then
  echo
  echo "Missing dependencies: ${missing[*]}"
  echo "Install on macOS (Homebrew):"
  echo "  brew install python ffmpeg whisperkit-cli"
  echo "  pip install 'mlx-audio[stt,tts]' misaki"
  exit 1
fi

"$python" - <<'PY' || {
import json
import sys

try:
    import mlx_audio
except ImportError:
    print("mlx-audio is not installed")
    sys.exit(1)

print(json.dumps({"mlx_audio": getattr(mlx_audio, "__version__", "unknown")}))
PY
  echo
  echo "mlx-audio is not installed."
  echo "Install with: pip install 'mlx-audio[stt,tts]' misaki"
  exit 1
}

if command -v whisperkit-cli >/dev/null 2>&1; then
  echo "whisperkit-cli: $(command -v whisperkit-cli)"
elif command -v argmax-cli >/dev/null 2>&1; then
  echo "argmax-cli: $(command -v argmax-cli)"
else
  echo
  echo "WhisperKit CLI not found (optional for whisperkit engine)."
  echo "Install with: brew install whisperkit-cli"
fi

helper="$(cd "$(dirname "$0")/.." && pwd)/internal/speechengine/scripts/local-speech-mlx.py"
if [ ! -f "$helper" ]; then
  echo
  echo "Helper script not found at $helper"
  exit 1
fi
"$python" -c 'import ast,sys; ast.parse(open(sys.argv[1]).read())' "$helper"
echo "local-speech-mlx.py: $helper"
echo "All required local speech dependencies are available."
