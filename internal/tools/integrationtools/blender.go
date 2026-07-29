package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/google/uuid"
)

const (
	blenderDefaultTimeout = 30 * time.Minute
	blenderMaxLogBytes    = 24 * 1024
	blenderOutputDirEnv   = "A2GENT_BLENDER_OUTPUT_DIR"
)

// blenderRunner resolves the Blender integration and executes the host binary.
// WHY: Blender has no HTTP API like ComfyUI, so access is formalized around the
// CLI. Execution is isolated from the tool schemas so a host bridge (needed when
// brute itself runs in Docker and cannot exec a host binary) can later be added
// as a second backend without changing either tool contract.
type blenderRunner struct {
	store storage.Store
}

func (r *blenderRunner) selectIntegration(integrationID, integrationName string) (*storage.Integration, error) {
	if r.store == nil {
		return nil, fmt.Errorf("blender integration store is unavailable")
	}
	all, err := r.store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item.Provider == "blender" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled blender integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("blender integration with id %q not found or disabled", id)
	}

	if name := strings.ToLower(strings.TrimSpace(integrationName)); name != "" {
		var match *storage.Integration
		for _, item := range candidates {
			if strings.ToLower(strings.TrimSpace(item.Name)) != name {
				continue
			}
			if match != nil {
				return nil, fmt.Errorf("multiple blender integrations matched name %q; pass integration_id", integrationName)
			}
			match = item
		}
		if match == nil {
			return nil, fmt.Errorf("blender integration named %q not found", integrationName)
		}
		return match, nil
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple blender integrations are enabled; pass integration_id or integration_name")
}

func (r *blenderRunner) binaryPath(integration *storage.Integration) (string, error) {
	binary := strings.TrimSpace(integration.Config["binary_path"])
	if binary == "" {
		binary = "blender"
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("blender binary %q is not executable: %v", binary, err)
	}
	return resolved, nil
}

// run executes Blender and returns trimmed stdout/stderr logs.
func (r *blenderRunner) run(ctx context.Context, integration *storage.Integration, args []string, extraEnv []string) (string, string, error) {
	binary, err := r.binaryPath(integration)
	if err != nil {
		return "", "", err
	}

	timeout := blenderDefaultTimeout
	if seconds := parsePositiveInt(integration.Config["timeout_seconds"]); seconds > 0 {
		timeout = time.Duration(seconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), extraEnv...)

	err = cmd.Run()
	out := tailLog(stdout.String())
	errLog := tailLog(stderr.String())
	if ctx.Err() == context.DeadlineExceeded {
		return out, errLog, fmt.Errorf("blender timed out after %s", timeout)
	}
	if err != nil {
		detail := strings.TrimSpace(errLog)
		if detail == "" {
			detail = strings.TrimSpace(out)
		}
		if detail == "" {
			detail = err.Error()
		}
		return out, errLog, fmt.Errorf("blender exited with error: %s", detail)
	}
	return out, errLog, nil
}

func tailLog(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) <= blenderMaxLogBytes {
		return trimmed
	}
	return "...(truncated)...\n" + trimmed[len(trimmed)-blenderMaxLogBytes:]
}

// resolveExistingPath resolves relative paths against workDir and requires the
// target to exist, so agents get a clear error instead of a Blender stack trace.
func resolveExistingPath(workDir, raw, label string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if !filepath.IsAbs(path) && strings.TrimSpace(workDir) != "" {
		path = filepath.Join(workDir, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%s not found: %s", label, path)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s must be a file, not a directory: %s", label, path)
	}
	return path, nil
}

// newBlenderRunDir isolates each invocation so produced files can be collected
// by listing the directory instead of parsing Blender's stdout.
func newBlenderRunDir(root, prefix string) (string, error) {
	base := strings.TrimSpace(root)
	if base == "" {
		base = filepath.Join(os.TempDir(), "generated", "blender")
	}
	dir := filepath.Join(base, prefix+"-"+uuid.New().String()[:8])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}
	return dir, nil
}

func collectBlenderFiles(dir string) []string {
	paths := make([]string, 0, 4)
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	sort.Strings(paths)
	return paths
}

func blenderArtifactKind(mimeType string) string {
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return "image"
	case strings.HasPrefix(mimeType, "video/"):
		return "video"
	case strings.HasPrefix(mimeType, "audio/"):
		return "audio"
	case strings.HasPrefix(mimeType, "model/"):
		return "model"
	case strings.HasPrefix(mimeType, "text/"):
		return "text"
	default:
		return "file"
	}
}

// BlenderRenderTool renders a .blend file through the host Blender binary and
// returns local image paths for Caesar preview.
type BlenderRenderTool struct {
	runner    *blenderRunner
	workDir   string
	outputDir string
}

type blenderRenderParams struct {
	BlendFile            string `json:"blend_file"`
	Frame                int    `json:"frame,omitempty"`
	FrameStart           int    `json:"frame_start,omitempty"`
	FrameEnd             int    `json:"frame_end,omitempty"`
	Engine               string `json:"engine,omitempty"`
	Format               string `json:"format,omitempty"`
	Scene                string `json:"scene,omitempty"`
	Camera               string `json:"camera,omitempty"`
	ResolutionX          int    `json:"resolution_x,omitempty"`
	ResolutionY          int    `json:"resolution_y,omitempty"`
	ResolutionPercentage int    `json:"resolution_percentage,omitempty"`
	Samples              int    `json:"samples,omitempty"`
	IntegrationID        string `json:"integration_id,omitempty"`
	IntegrationName      string `json:"integration_name,omitempty"`
}

func NewBlenderRenderTool(store storage.Store, outputDir string) *BlenderRenderTool {
	return &BlenderRenderTool{
		runner:    &blenderRunner{store: store},
		outputDir: strings.TrimSpace(outputDir),
	}
}

// WithWorkDir lets the tool resolve relative blend paths against the project root.
func (t *BlenderRenderTool) WithWorkDir(workDir string) *BlenderRenderTool {
	t.workDir = strings.TrimSpace(workDir)
	return t
}

func (t *BlenderRenderTool) Name() string {
	return "blender_render"
}

func (t *BlenderRenderTool) Description() string {
	return "Render a .blend scene with the host Blender install (headless). Renders a single frame or a frame range and returns local image paths for preview."
}

func (t *BlenderRenderTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"blend_file": map[string]interface{}{
				"type":        "string",
				"description": "Path to the .blend file. Relative paths resolve from the current project/workspace.",
			},
			"frame": map[string]interface{}{
				"type":        "integer",
				"description": "Single frame to render. Defaults to the scene's current frame when no range is given.",
			},
			"frame_start": map[string]interface{}{
				"type":        "integer",
				"description": "First frame of an animation range. Requires frame_end.",
			},
			"frame_end": map[string]interface{}{
				"type":        "integer",
				"description": "Last frame of an animation range. Requires frame_start.",
			},
			"engine": map[string]interface{}{
				"type":        "string",
				"description": "Render engine override, for example CYCLES, BLENDER_EEVEE_NEXT, or BLENDER_WORKBENCH.",
			},
			"format": map[string]interface{}{
				"type":        "string",
				"description": "Output format override, for example PNG, JPEG, OPEN_EXR, or FFMPEG.",
			},
			"scene": map[string]interface{}{
				"type":        "string",
				"description": "Scene name to render when the file has multiple scenes.",
			},
			"camera": map[string]interface{}{
				"type":        "string",
				"description": "Object name of the camera to render from.",
			},
			"resolution_x": map[string]interface{}{
				"type":        "integer",
				"description": "Horizontal resolution override in pixels.",
			},
			"resolution_y": map[string]interface{}{
				"type":        "integer",
				"description": "Vertical resolution override in pixels.",
			},
			"resolution_percentage": map[string]interface{}{
				"type":        "integer",
				"description": "Resolution scale percentage, useful for fast previews (for example 50).",
			},
			"samples": map[string]interface{}{
				"type":        "integer",
				"description": "Render sample count override for Cycles/EEVEE.",
			},
			"integration_id": map[string]interface{}{
				"type":        "string",
				"description": "Specific Blender integration ID when multiple are enabled.",
			},
			"integration_name": map[string]interface{}{
				"type":        "string",
				"description": "Specific Blender integration name when multiple are enabled.",
			},
		},
		"required": []string{"blend_file"},
	}
}

func (t *BlenderRenderTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p blenderRenderParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	blendFile, err := resolveExistingPath(t.workDir, p.BlendFile, "blend_file")
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	if (p.FrameStart > 0) != (p.FrameEnd > 0) {
		return &tools.Result{Success: false, Error: "frame_start and frame_end must be provided together"}, nil
	}
	if p.FrameStart > 0 && p.FrameEnd < p.FrameStart {
		return &tools.Result{Success: false, Error: "frame_end must be greater than or equal to frame_start"}, nil
	}

	integration, err := t.runner.selectIntegration(p.IntegrationID, p.IntegrationName)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	runDir, err := newBlenderRunDir(t.outputDir, "render")
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	args := []string{"-b", blendFile}
	if scene := strings.TrimSpace(p.Scene); scene != "" {
		args = append(args, "-S", scene)
	}
	if engine := strings.TrimSpace(p.Engine); engine != "" {
		args = append(args, "-E", engine)
	}
	if format := strings.TrimSpace(p.Format); format != "" {
		args = append(args, "-F", format)
	}
	// Scene-level overrides have no CLI flags, so they are applied through a
	// short bpy expression that runs before the render starts.
	if expr := buildBlenderOverrideExpr(p); expr != "" {
		args = append(args, "--python-expr", expr)
	}
	args = append(args, "-o", filepath.Join(runDir, "frame_####"))
	if p.FrameStart > 0 {
		args = append(args, "-s", strconv.Itoa(p.FrameStart), "-e", strconv.Itoa(p.FrameEnd), "-a")
	} else if p.Frame != 0 {
		args = append(args, "-f", strconv.Itoa(p.Frame))
	} else {
		args = append(args, "-f", "1")
	}

	stdout, stderr, runErr := t.runner.run(ctx, integration, args, nil)
	if runErr != nil {
		return &tools.Result{Success: false, Error: runErr.Error(), Output: stdout}, nil
	}

	paths := collectBlenderFiles(runDir)
	if len(paths) == 0 {
		detail := strings.TrimSpace(stderr)
		if detail == "" {
			detail = strings.TrimSpace(stdout)
		}
		return &tools.Result{Success: false, Error: "blender produced no output files. " + detail}, nil
	}

	metadata := map[string]interface{}{
		"blender_render": map[string]interface{}{
			"paths":      paths,
			"blend_file": blendFile,
			"output_dir": runDir,
		},
		// Mirrors ComfyUI/Leonardo so Caesar previews the render inline.
		"image_file": map[string]interface{}{
			"path":        paths[0],
			"source_tool": "blender_render",
		},
	}

	lines := []string{fmt.Sprintf("Blender rendered %d file(s) from %s:", len(paths), blendFile)}
	for _, path := range paths {
		lines = append(lines, "- "+path)
	}
	return &tools.Result{Success: true, Output: strings.Join(lines, "\n"), Metadata: metadata}, nil
}

func buildBlenderOverrideExpr(p blenderRenderParams) string {
	lines := make([]string, 0, 8)
	if p.ResolutionX > 0 {
		lines = append(lines, fmt.Sprintf("_scene.render.resolution_x = %d", p.ResolutionX))
	}
	if p.ResolutionY > 0 {
		lines = append(lines, fmt.Sprintf("_scene.render.resolution_y = %d", p.ResolutionY))
	}
	if p.ResolutionPercentage > 0 {
		lines = append(lines, fmt.Sprintf("_scene.render.resolution_percentage = %d", p.ResolutionPercentage))
	}
	if p.Samples > 0 {
		// Sample settings live in engine-specific structs, so both are guarded.
		lines = append(lines, fmt.Sprintf("if hasattr(_scene, 'cycles'): _scene.cycles.samples = %d", p.Samples))
		lines = append(lines, fmt.Sprintf("if hasattr(_scene, 'eevee'): _scene.eevee.taa_render_samples = %d", p.Samples))
	}
	if camera := strings.TrimSpace(p.Camera); camera != "" {
		lines = append(lines, fmt.Sprintf("_camera = bpy.data.objects.get(%s)", strconv.Quote(camera)))
		lines = append(lines, "if _camera is not None: _scene.camera = _camera")
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(append([]string{"import bpy", "_scene = bpy.context.scene"}, lines...), "\n")
}
