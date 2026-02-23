#!/usr/bin/env bash
set -euo pipefail

echo "Checking local audio dependencies for brute (whisper.cpp/piper bootstrap)..."

missing=()
for cmd in cmake ffmpeg pkg-config; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    missing+=("$cmd")
  fi
done

if [ ${#missing[@]} -gt 0 ]; then
  echo
  echo "❌ Missing dependencies: ${missing[*]}"
  echo "Install on macOS (Homebrew):"
  echo "  brew install cmake ffmpeg pkg-config"
  if command -v brew >/dev/null 2>&1; then
    BREW_PREFIX="$(brew --prefix)"
    echo
    echo "If installed but still not found, add Homebrew bin to PATH:"
    echo "  export PATH=\"${BREW_PREFIX}/bin:$PATH\""
  fi
  exit 1
fi

echo "✅ All required audio dependencies are available: cmake, ffmpeg, pkg-config"
