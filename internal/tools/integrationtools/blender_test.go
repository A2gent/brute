package integrationtools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

// newFakeBlender writes an executable stub that records its argv and env into
// recordPath, so tests can assert the exact CLI contract without real Blender.
func newFakeBlender(t *testing.T, recordPath, extra string) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		": > " + recordPath + "\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> " + recordPath + "; done\n" +
		"printf 'ENV_OUTPUT_DIR=%s\\n' \"$A2GENT_BLENDER_OUTPUT_DIR\" >> " + recordPath + "\n" +
		extra + "\n" +
		"echo 'Blender quit'\n"
	path := filepath.Join(t.TempDir(), "blender")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake blender: %v", err)
	}
	return path
}

func newBlenderTestStore(t *testing.T, binaryPath string) storage.Store {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	if err := store.SaveIntegration(&storage.Integration{
		ID:        "blender-1",
		Provider:  "blender",
		Name:      "Local Blender",
		Mode:      "notify_only",
		Enabled:   true,
		Config:    map[string]string{"binary_path": binaryPath},
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("failed to save blender integration: %v", err)
	}
	return store
}

func writeBlendFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scene.blend")
	if err := os.WriteFile(path, []byte("fake"), 0o644); err != nil {
		t.Fatalf("failed to write blend file: %v", err)
	}
	return path
}

func readRecord(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read recorded args: %v", err)
	}
	return string(data)
}

func TestBlenderRenderToolNameAndSchema(t *testing.T) {
	t.Parallel()
	tool := NewBlenderRenderTool(nil, "")
	if tool.Name() != "blender_render" {
		t.Fatalf("unexpected name: %s", tool.Name())
	}
	schema := tool.Schema()
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("expected schema properties map")
	}
	for _, name := range []string{"blend_file", "frame", "frame_start", "frame_end", "engine", "format", "integration_id"} {
		if _, ok := props[name]; !ok {
			t.Fatalf("expected %s property", name)
		}
	}
	required, ok := schema["required"].([]string)
	if !ok || len(required) == 0 || required[0] != "blend_file" {
		t.Fatalf("expected blend_file to be required, got %#v", schema["required"])
	}
}

func TestBlenderRenderMissingIntegration(t *testing.T) {
	t.Parallel()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tool := NewBlenderRenderTool(store, t.TempDir())
	params, _ := json.Marshal(map[string]string{"blend_file": writeBlendFile(t)})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure without blender integration")
	}
	if !strings.Contains(strings.ToLower(result.Error), "blender") {
		t.Fatalf("expected blender config error, got %q", result.Error)
	}
}

func TestBlenderRenderMissingBlendFile(t *testing.T) {
	t.Parallel()
	record := filepath.Join(t.TempDir(), "args.txt")
	tool := NewBlenderRenderTool(newBlenderTestStore(t, newFakeBlender(t, record, "")), t.TempDir())
	params, _ := json.Marshal(map[string]string{"blend_file": filepath.Join(t.TempDir(), "absent.blend")})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure for missing blend file")
	}
	if !strings.Contains(strings.ToLower(result.Error), "not found") {
		t.Fatalf("expected not found error, got %q", result.Error)
	}
}

func TestBlenderRenderSingleFrameCollectsOutputs(t *testing.T) {
	t.Parallel()
	record := filepath.Join(t.TempDir(), "args.txt")
	// The stub emulates Blender writing frame_0001.png next to the -o prefix.
	stub := newFakeBlender(t, record, `out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  prev="$a"
done
if [ -n "$out" ]; then mkdir -p "$(dirname "$out")"; printf 'png-bytes' > "${out%####}0001.png"; fi`)

	blend := writeBlendFile(t)
	outRoot := t.TempDir()
	tool := NewBlenderRenderTool(newBlenderTestStore(t, stub), outRoot)

	params, _ := json.Marshal(map[string]interface{}{
		"blend_file":   blend,
		"frame":        7,
		"engine":       "CYCLES",
		"format":       "PNG",
		"resolution_x": 640,
		"resolution_y": 480,
		"samples":      16,
	})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %q", result.Error)
	}

	args := readRecord(t, record)
	for _, want := range []string{"-b", blend, "-E", "CYCLES", "-F", "PNG", "-f", "7"} {
		if !strings.Contains(args, want+"\n") {
			t.Fatalf("expected arg %q in %s", want, args)
		}
	}
	if !strings.Contains(args, "--python-expr") || !strings.Contains(args, "resolution_x = 640") {
		t.Fatalf("expected resolution override expression, got %s", args)
	}
	if !strings.Contains(args, "samples = 16") {
		t.Fatalf("expected samples override, got %s", args)
	}

	render, ok := result.Metadata["blender_render"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected blender_render metadata, got %#v", result.Metadata)
	}
	paths, ok := render["paths"].([]string)
	if !ok || len(paths) != 1 {
		t.Fatalf("expected one rendered path, got %#v", render["paths"])
	}
	if !strings.HasSuffix(paths[0], "0001.png") {
		t.Fatalf("unexpected render path: %s", paths[0])
	}
	if !strings.HasPrefix(paths[0], outRoot) {
		t.Fatalf("render should land under output root %s, got %s", outRoot, paths[0])
	}
	imageFile, ok := result.Metadata["image_file"].(map[string]interface{})
	if !ok || imageFile["source_tool"] != "blender_render" {
		t.Fatalf("expected image_file preview metadata, got %#v", result.Metadata["image_file"])
	}
}

func TestBlenderRenderAnimationRangeUsesFrameFlags(t *testing.T) {
	t.Parallel()
	record := filepath.Join(t.TempDir(), "args.txt")
	stub := newFakeBlender(t, record, `out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  prev="$a"
done
if [ -n "$out" ]; then mkdir -p "$(dirname "$out")"; printf 'a' > "${out%####}0002.png"; printf 'b' > "${out%####}0003.png"; fi`)

	tool := NewBlenderRenderTool(newBlenderTestStore(t, stub), t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"blend_file":  writeBlendFile(t),
		"frame_start": 2,
		"frame_end":   3,
	})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %q", result.Error)
	}

	args := readRecord(t, record)
	for _, want := range []string{"-s", "2", "-e", "3", "-a"} {
		if !strings.Contains(args, want+"\n") {
			t.Fatalf("expected arg %q in %s", want, args)
		}
	}
	render := result.Metadata["blender_render"].(map[string]interface{})
	if paths := render["paths"].([]string); len(paths) != 2 {
		t.Fatalf("expected two rendered frames, got %#v", paths)
	}
}

func TestBlenderRenderReportsFailure(t *testing.T) {
	t.Parallel()
	record := filepath.Join(t.TempDir(), "args.txt")
	stub := newFakeBlender(t, record, "echo 'Error: cannot read file' >&2; exit 1")

	tool := NewBlenderRenderTool(newBlenderTestStore(t, stub), t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{"blend_file": writeBlendFile(t)})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure when blender exits non-zero")
	}
	if !strings.Contains(result.Error, "cannot read file") {
		t.Fatalf("expected stderr detail in error, got %q", result.Error)
	}
}

func TestBlenderRunScriptToolNameAndSchema(t *testing.T) {
	t.Parallel()
	tool := NewBlenderRunScriptTool(nil, "", "")
	if tool.Name() != "blender_run_script" {
		t.Fatalf("unexpected name: %s", tool.Name())
	}
	props, ok := tool.Schema()["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("expected schema properties map")
	}
	for _, name := range []string{"script", "script_path", "blend_file", "args", "integration_id", "integration_name"} {
		if _, ok := props[name]; !ok {
			t.Fatalf("expected %s property", name)
		}
	}
}

func TestBlenderRunScriptRequiresScript(t *testing.T) {
	t.Parallel()
	record := filepath.Join(t.TempDir(), "args.txt")
	tool := NewBlenderRunScriptTool(newBlenderTestStore(t, newFakeBlender(t, record, "")), t.TempDir(), t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure without script or script_path")
	}
}

func TestBlenderRunScriptExecutesAndCollectsArtifacts(t *testing.T) {
	t.Parallel()
	record := filepath.Join(t.TempDir(), "args.txt")
	// The stub echoes the script body and drops an artifact into the exported dir.
	stub := newFakeBlender(t, record, `prev=""
script=""
for a in "$@"; do
  if [ "$prev" = "-P" ]; then script="$a"; fi
  prev="$a"
done
cat "$script"
printf 'obj' > "$A2GENT_BLENDER_OUTPUT_DIR/model.obj"`)

	outRoot := t.TempDir()
	tool := NewBlenderRunScriptTool(newBlenderTestStore(t, stub), t.TempDir(), outRoot)

	params, _ := json.Marshal(map[string]interface{}{
		"script": "import bpy\nprint('hello from bpy')",
		"args":   []string{"--quality", "high"},
	})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %q", result.Error)
	}
	if !strings.Contains(result.Output, "hello from bpy") {
		t.Fatalf("expected script body echoed through stdout, got %q", result.Output)
	}

	args := readRecord(t, record)
	for _, want := range []string{"-b", "--factory-startup", "-P", "--", "--quality", "high"} {
		if !strings.Contains(args, want+"\n") {
			t.Fatalf("expected arg %q in %s", want, args)
		}
	}
	if !strings.Contains(args, "ENV_OUTPUT_DIR="+outRoot) {
		t.Fatalf("expected A2GENT_BLENDER_OUTPUT_DIR exported under %s, got %s", outRoot, args)
	}

	artifacts, ok := result.Metadata["artifacts"].([]map[string]interface{})
	if !ok || len(artifacts) != 1 {
		t.Fatalf("expected one collected artifact, got %#v", result.Metadata["artifacts"])
	}
	if artifacts[0]["filename"] != "model.obj" {
		t.Fatalf("unexpected artifact: %#v", artifacts[0])
	}
}

func TestBlenderRunScriptUsesScriptPathAndBlendFile(t *testing.T) {
	t.Parallel()
	record := filepath.Join(t.TempDir(), "args.txt")
	stub := newFakeBlender(t, record, "")

	scriptPath := filepath.Join(t.TempDir(), "job.py")
	if err := os.WriteFile(scriptPath, []byte("import bpy\n"), 0o644); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}
	blend := writeBlendFile(t)

	tool := NewBlenderRunScriptTool(newBlenderTestStore(t, stub), t.TempDir(), t.TempDir())
	params, _ := json.Marshal(map[string]interface{}{
		"script_path": scriptPath,
		"blend_file":  blend,
	})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %q", result.Error)
	}

	args := readRecord(t, record)
	if !strings.Contains(args, blend+"\n") {
		t.Fatalf("expected blend file argument, got %s", args)
	}
	if !strings.Contains(args, scriptPath+"\n") {
		t.Fatalf("expected script path argument, got %s", args)
	}
}
