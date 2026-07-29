package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const blenderMaxScriptBytes = 1 * 1024 * 1024

// BlenderRunScriptTool runs a bpy Python script inside headless Blender so
// agents can drive modelling, import/export and scene edits, not just renders.
// Blender ships its own Python interpreter, so no system Python is required.
type BlenderRunScriptTool struct {
	runner    *blenderRunner
	workDir   string
	outputDir string
}

type blenderRunScriptParams struct {
	Script          string   `json:"script,omitempty"`
	ScriptPath      string   `json:"script_path,omitempty"`
	BlendFile       string   `json:"blend_file,omitempty"`
	Args            []string `json:"args,omitempty"`
	FactoryStartup  *bool    `json:"factory_startup,omitempty"`
	IntegrationID   string   `json:"integration_id,omitempty"`
	IntegrationName string   `json:"integration_name,omitempty"`
}

func NewBlenderRunScriptTool(store storage.Store, workDir, outputDir string) *BlenderRunScriptTool {
	return &BlenderRunScriptTool{
		runner:    &blenderRunner{store: store},
		workDir:   strings.TrimSpace(workDir),
		outputDir: strings.TrimSpace(outputDir),
	}
}

func (t *BlenderRunScriptTool) Name() string {
	return "blender_run_script"
}

func (t *BlenderRunScriptTool) Description() string {
	return "Run a Python (bpy) script in headless Blender on the host: build or edit scenes, import/export meshes, batch-process assets. Files written to the A2GENT_BLENDER_OUTPUT_DIR directory are collected and returned."
}

func (t *BlenderRunScriptTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"script": map[string]interface{}{
				"type":        "string",
				"description": "Inline Python source executed by Blender's bundled interpreter. Use bpy to drive the scene.",
			},
			"script_path": map[string]interface{}{
				"type":        "string",
				"description": "Path to a .py file to run instead of inline script. Relative paths resolve from the current project/workspace.",
			},
			"blend_file": map[string]interface{}{
				"type":        "string",
				"description": "Optional .blend file to open before running the script. Omit to start from Blender's default scene.",
			},
			"args": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Extra arguments passed to the script after `--`, readable via sys.argv.",
			},
			"factory_startup": map[string]interface{}{
				"type":        "boolean",
				"description": "Run with --factory-startup to ignore user preferences and add-ons (default true for reproducibility).",
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
	}
}

func (t *BlenderRunScriptTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p blenderRunScriptParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	inline := strings.TrimSpace(p.Script)
	if inline == "" && strings.TrimSpace(p.ScriptPath) == "" {
		return &tools.Result{Success: false, Error: "either script or script_path is required"}, nil
	}
	if len(p.Script) > blenderMaxScriptBytes {
		return &tools.Result{Success: false, Error: "script exceeds the 1MB limit; use script_path instead"}, nil
	}

	integration, err := t.runner.selectIntegration(p.IntegrationID, p.IntegrationName)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	scriptPath := ""
	if inline != "" {
		file, err := os.CreateTemp("", "a2gent-blender-*.py")
		if err != nil {
			return &tools.Result{Success: false, Error: "failed to stage script: " + err.Error()}, nil
		}
		defer os.Remove(file.Name())
		if _, err := file.WriteString(p.Script); err != nil {
			file.Close()
			return &tools.Result{Success: false, Error: "failed to stage script: " + err.Error()}, nil
		}
		file.Close()
		scriptPath = file.Name()
	} else {
		scriptPath, err = resolveExistingPath(t.workDir, p.ScriptPath, "script_path")
		if err != nil {
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
	}

	blendFile := ""
	if strings.TrimSpace(p.BlendFile) != "" {
		blendFile, err = resolveExistingPath(t.workDir, p.BlendFile, "blend_file")
		if err != nil {
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
	}

	runDir, err := newBlenderRunDir(t.outputDir, "script")
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	factoryStartup := p.FactoryStartup == nil || *p.FactoryStartup
	args := make([]string, 0, 8)
	if factoryStartup {
		args = append(args, "--factory-startup")
	}
	args = append(args, "-b")
	if blendFile != "" {
		args = append(args, blendFile)
	}
	args = append(args, "-P", scriptPath)
	if len(p.Args) > 0 {
		args = append(args, "--")
		args = append(args, p.Args...)
	}

	// Scripts learn where to write results through this env var; everything left
	// in that directory is collected as an artifact.
	stdout, stderr, runErr := t.runner.run(ctx, integration, args, []string{blenderOutputDirEnv + "=" + runDir})
	if runErr != nil {
		return &tools.Result{Success: false, Error: runErr.Error(), Output: stdout}, nil
	}

	artifacts := make([]map[string]interface{}, 0, 4)
	for _, path := range collectBlenderFiles(runDir) {
		mimeType := comfyUIArtifactMIMEType(path)
		artifacts = append(artifacts, map[string]interface{}{
			"path":      path,
			"filename":  filepath.Base(path),
			"mime_type": mimeType,
			"kind":      blenderArtifactKind(mimeType),
		})
	}

	output := strings.TrimSpace(stdout)
	if stderr != "" {
		output = strings.TrimSpace(output + "\n[stderr]\n" + stderr)
	}
	if len(artifacts) > 0 {
		lines := []string{output, fmt.Sprintf("Collected %d artifact(s):", len(artifacts))}
		for _, artifact := range artifacts {
			lines = append(lines, fmt.Sprintf("- [%s] %s", artifact["kind"], artifact["path"]))
		}
		output = strings.TrimSpace(strings.Join(lines, "\n"))
	}
	if output == "" {
		output = "Blender script completed with no output."
	}

	metadata := map[string]interface{}{
		"artifacts":  artifacts,
		"output_dir": runDir,
	}
	// Preview the first image artifact inline, matching the render tool.
	for _, artifact := range artifacts {
		if artifact["kind"] == "image" {
			metadata["image_file"] = map[string]interface{}{
				"path":        artifact["path"],
				"source_tool": "blender_run_script",
			}
			break
		}
	}

	return &tools.Result{Success: true, Output: output, Metadata: metadata}, nil
}
