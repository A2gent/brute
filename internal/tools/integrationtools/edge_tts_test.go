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

func TestEdgeTTSToolExecuteWithStreamOutput(t *testing.T) {
	tmpDir := t.TempDir()

	edgeBin := filepath.Join(tmpDir, "edge-tts")
	if err := os.WriteFile(edgeBin, []byte(`#!/bin/sh
set -eu
OUTPUT=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --write-media) OUTPUT="$2"; shift 2 ;;
    *) shift ;;
  esac
done
/bin/mkdir -p "$(dirname "$OUTPUT")"
/bin/echo "fake-mp3-audio" > "$OUTPUT"
`), 0o755); err != nil {
		t.Fatalf("write fake edge-tts: %v", err)
	}

	tool := NewEdgeTTSTool(tmpDir, speechcache.New(0))
	params := map[string]interface{}{
		"text":         "hello from edge",
		"edge_tts_bin": edgeBin,
		"output_mode":  "stream",
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
	clipID, _ := audioMeta["clip_id"].(string)
	if strings.TrimSpace(clipID) == "" {
		t.Fatalf("missing clip_id in audio_clip metadata")
	}
}

func TestEdgeTTSToolExecuteWithFileOutput(t *testing.T) {
	tmpDir := t.TempDir()

	edgeBin := filepath.Join(tmpDir, "edge-tts")
	if err := os.WriteFile(edgeBin, []byte(`#!/bin/sh
set -eu
OUTPUT=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --write-media) OUTPUT="$2"; shift 2 ;;
    *) shift ;;
  esac
done
/bin/mkdir -p "$(dirname "$OUTPUT")"
/bin/echo "fake-mp3-audio" > "$OUTPUT"
`), 0o755); err != nil {
		t.Fatalf("write fake edge-tts: %v", err)
	}

	tool := NewEdgeTTSTool(tmpDir, speechcache.New(0))
	outputPath := filepath.Join(tmpDir, "clips", "speech.mp3")
	params := map[string]interface{}{
		"text":         "hello file output",
		"edge_tts_bin": edgeBin,
		"output_mode":  "file",
		"output_path":  outputPath,
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
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("expected output file %s: %v", outputPath, err)
	}
	fileMeta, ok := res.Metadata["file_output"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing file_output metadata")
	}
	if got, _ := fileMeta["path"].(string); got != outputPath {
		t.Fatalf("unexpected file path metadata: %q", got)
	}
}
