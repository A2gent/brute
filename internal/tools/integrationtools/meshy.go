package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const meshyDefaultBaseURL = "https://api.meshy.ai"

var meshySupportedFormats = map[string]struct{}{"glb": {}, "fbx": {}, "obj": {}, "usdz": {}, "stl": {}, "3mf": {}}

// MeshyGenerate3DTool runs Meshy text-to-3D or image-to-3D tasks, waits for
// completion and saves the model file plus preview thumbnail locally.
type MeshyGenerate3DTool struct {
	store        storage.Store
	workDir      string
	outputDir    string
	client       *http.Client
	pollInterval time.Duration
	maxWait      time.Duration
}

// meshyMaxWaitPerCall keeps one tool call under the Claude CLI MCP tool
// timeout (10 min, see claudecli.defaultMCPToolTimeoutMS) with headroom for
// downloading the result.
const meshyMaxWaitPerCall = 9 * time.Minute

type meshyGenerate3DParams struct {
	Prompt          string `json:"prompt"`
	Image           string `json:"image"`
	TexturePrompt   string `json:"texture_prompt"`
	Texture         *bool  `json:"texture"`
	PBR             bool   `json:"pbr"`
	AIModel         string `json:"ai_model"`
	Format          string `json:"format"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	TaskID          string `json:"task_id"`
	ModelType       string `json:"model_type"`
	TargetPolycount int    `json:"target_polycount"`
}

type meshyTask struct {
	ID           string            `json:"id"`
	Status       string            `json:"status"`
	Progress     int               `json:"progress"`
	ModelURLs    map[string]string `json:"model_urls"`
	ThumbnailURL string            `json:"thumbnail_url"`
	TaskError    struct {
		Message string `json:"message"`
	} `json:"task_error"`
}

func NewMeshyGenerate3DTool(store storage.Store, workDir, outputDir string) *MeshyGenerate3DTool {
	return &MeshyGenerate3DTool{
		store:        store,
		workDir:      strings.TrimSpace(workDir),
		outputDir:    strings.TrimSpace(outputDir),
		client:       &http.Client{Timeout: 120 * time.Second},
		pollInterval: model3DDefaultPollInterval,
		maxWait:      meshyMaxWaitPerCall,
	}
}

func (t *MeshyGenerate3DTool) Name() string { return "meshy_generate_3d" }

func (t *MeshyGenerate3DTool) Description() string {
	return "Generate a 3D model with Meshy AI from a text prompt (text-to-3D) or from an image (image-to-3D). " +
		"The image can be a local file path (for example one produced by leonardo_generate_image) or a public URL. " +
		"Waits up to ~2.5 minutes per call; slower tasks return a task_id - call again with task_id to keep waiting instead of starting a new task. " +
		"Returns the local path of the downloaded model file and a preview image."
}

func (t *MeshyGenerate3DTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Text description of the object (max 800 chars). Required unless image is set.",
			},
			"image": map[string]interface{}{
				"type":        "string",
				"description": "Input image for image-to-3D: local path (relative to the workspace or absolute) or public http(s) URL. PNG, JPEG or WebP up to 20 MB.",
			},
			"texture_prompt": map[string]interface{}{
				"type":        "string",
				"description": "Optional text guiding the texturing pass (max 800 chars).",
			},
			"texture": map[string]interface{}{
				"type":        "boolean",
				"description": "Generate textures (default true). For text-to-3D this runs an extra refine pass.",
			},
			"pbr": map[string]interface{}{
				"type":        "boolean",
				"description": "Also generate PBR maps (metallic, roughness, normal). Default false.",
			},
			"ai_model": map[string]interface{}{
				"type":        "string",
				"description": "Meshy model ID: latest (default), meshy-7.1, meshy-6, meshy-6-lite.",
			},
			"format": map[string]interface{}{
				"type":        "string",
				"description": "Output file format: glb (default), fbx, obj, usdz, stl, 3mf.",
			},
			"timeout_seconds": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum time to wait in this call (capped at 540s). Unfinished tasks return a task_id to resume.",
			},
			"task_id": map[string]interface{}{
				"type":        "string",
				"description": "Resume waiting for a task returned by a previous call (e.g. image-to-3d:<id>). Other inputs except format/texture options are ignored.",
			},
			"model_type": map[string]interface{}{
				"type":        "string",
				"description": "Mesh type: standard (high detail) or smart-topology (clean low-poly via meshy-t2). Defaults to the integration setting.",
			},
			"target_polycount": map[string]interface{}{
				"type":        "integer",
				"description": "Target face count. Standard: 100-300000 (enables remesh). Smart-topology: 100-15000. Defaults to the integration setting.",
			},
		},
	}
}

func (t *MeshyGenerate3DTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p meshyGenerate3DParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	p.Prompt = strings.TrimSpace(p.Prompt)
	p.Image = strings.TrimSpace(p.Image)
	p.TaskID = strings.TrimSpace(p.TaskID)
	if p.Prompt == "" && p.Image == "" && p.TaskID == "" {
		return &tools.Result{Success: false, Error: "either prompt, image or task_id is required"}, nil
	}
	format := strings.ToLower(strings.TrimSpace(p.Format))
	if format == "" {
		format = "glb"
	}
	if _, ok := meshySupportedFormats[format]; !ok {
		return &tools.Result{Success: false, Error: "unsupported format: " + format}, nil
	}
	integration, err := findModel3DIntegration(t.store, "meshy")
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	baseURL := integration.BaseURL
	if baseURL == "" {
		baseURL = meshyDefaultBaseURL
	}
	aiModel := strings.TrimSpace(p.AIModel)
	if aiModel == "" {
		aiModel = integration.Model
	}
	texture := p.Texture == nil || *p.Texture
	timeout := integration.Timeout
	if p.TimeoutSeconds > 0 {
		timeout = time.Duration(p.TimeoutSeconds) * time.Second
	}
	// Cap a single call below the MCP client tool timeout; longer tasks are
	// handed back as a resumable task_id instead of failing the call.
	if t.maxWait > 0 && timeout > t.maxWait {
		timeout = t.maxWait
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var job meshyJob
	switch {
	case p.TaskID != "":
		job, err = parseMeshyJob(p.TaskID)
	case p.Image != "":
		job.kind = meshyKindImage
		job.id, err = t.createImageTo3D(waitCtx, baseURL, integration, p, aiModel, format, texture)
	default:
		job.kind = meshyKindPreview
		job.id, err = t.createTextPreview(waitCtx, baseURL, integration, p, aiModel, format)
	}
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	for {
		task, err := t.waitTask(waitCtx, job.endpoint(baseURL)+"/"+job.id, integration.APIKey)
		if err != nil {
			if waitCtx.Err() != nil && ctx.Err() == nil {
				return meshyPendingResult(t.Name(), job, task), nil
			}
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
		if job.kind != meshyKindPreview || !texture {
			return t.saveResult(ctx, task, format)
		}
		// Meshy text-to-3D is two-step: preview builds the mesh, refine textures it.
		refineID, err := t.createTextRefine(waitCtx, baseURL, integration.APIKey, p, job.id, format)
		if err != nil {
			if waitCtx.Err() != nil && ctx.Err() == nil {
				return meshyPendingResult(t.Name(), job, task), nil
			}
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
		job = meshyJob{kind: meshyKindRefine, id: refineID}
	}
}

func (t *MeshyGenerate3DTool) createImageTo3D(ctx context.Context, baseURL string, integration *model3DIntegration, p meshyGenerate3DParams, aiModel, format string, texture bool) (string, error) {
	img, err := resolveModel3DImage(p.Image, t.workDir)
	if err != nil {
		return "", err
	}
	imageURL := img.URL
	if imageURL == "" {
		// Meshy accepts base64 data URIs, so local files need no upload step.
		imageURL = img.dataURI()
	}
	body := map[string]interface{}{
		"image_url":      imageURL,
		"should_texture": texture,
		"target_formats": []string{format},
	}
	if texture && p.PBR {
		body["enable_pbr"] = true
	}
	if tp := strings.TrimSpace(firstNonEmpty(p.TexturePrompt, p.Prompt)); tp != "" && texture {
		body["texture_prompt"] = tp
	}
	if aiModel != "" {
		body["ai_model"] = aiModel
	}
	applyMeshyQuality(body, p, integration.Config)
	return t.createTask(ctx, meshyJob{kind: meshyKindImage}.endpoint(baseURL), integration.APIKey, body)
}

func (t *MeshyGenerate3DTool) createTextPreview(ctx context.Context, baseURL string, integration *model3DIntegration, p meshyGenerate3DParams, aiModel, format string) (string, error) {
	body := map[string]interface{}{
		"mode":           "preview",
		"prompt":         p.Prompt,
		"target_formats": []string{format},
	}
	if aiModel != "" {
		body["ai_model"] = aiModel
	}
	applyMeshyQuality(body, p, integration.Config)
	return t.createTask(ctx, meshyJob{kind: meshyKindPreview}.endpoint(baseURL), integration.APIKey, body)
}

func (t *MeshyGenerate3DTool) createTextRefine(ctx context.Context, baseURL, apiKey string, p meshyGenerate3DParams, previewID, format string) (string, error) {
	body := map[string]interface{}{
		"mode":            "refine",
		"preview_task_id": previewID,
		"target_formats":  []string{format},
	}
	if p.PBR {
		body["enable_pbr"] = true
	}
	if tp := strings.TrimSpace(p.TexturePrompt); tp != "" {
		body["texture_prompt"] = tp
	}
	return t.createTask(ctx, meshyJob{kind: meshyKindRefine}.endpoint(baseURL), apiKey, body)
}

// applyMeshyQuality sets mesh density: call params win over integration
// defaults so a project can default to lighter models without losing control.
func applyMeshyQuality(body map[string]interface{}, p meshyGenerate3DParams, config map[string]string) {
	modelType := strings.TrimSpace(firstNonEmpty(p.ModelType, config["model_type"]))
	polycount := p.TargetPolycount
	if polycount <= 0 {
		polycount, _ = strconv.Atoi(strings.TrimSpace(config["target_polycount"]))
	}
	if modelType != "" && modelType != "standard" {
		body["model_type"] = modelType
	}
	if modelType == "smart-topology" {
		// Smart Topology only runs on meshy-t* models; the standard default
		// ("latest") would be rejected.
		if m, _ := body["ai_model"].(string); !strings.HasPrefix(m, "meshy-t") {
			body["ai_model"] = "meshy-t2"
		}
	}
	if polycount > 0 {
		body["target_polycount"] = polycount
		if modelType == "" || modelType == "standard" {
			// Standard models only honour target_polycount during remesh.
			body["should_remesh"] = true
		}
	}
}

const (
	meshyKindImage   = "image-to-3d"
	meshyKindPreview = "text-to-3d-preview"
	meshyKindRefine  = "text-to-3d-refine"
)

// meshyJob identifies a Meshy task plus its stage, serialized as "kind:id" so
// the agent can resume a slow generation with one opaque task_id.
type meshyJob struct {
	kind string
	id   string
}

func (j meshyJob) String() string { return j.kind + ":" + j.id }

func (j meshyJob) endpoint(baseURL string) string {
	if j.kind == meshyKindImage {
		return baseURL + "/openapi/v1/image-to-3d"
	}
	return baseURL + "/openapi/v2/text-to-3d"
}

func parseMeshyJob(raw string) (meshyJob, error) {
	kind, id, ok := strings.Cut(raw, ":")
	id = strings.TrimSpace(id)
	switch kind {
	case meshyKindImage, meshyKindPreview, meshyKindRefine:
		if ok && id != "" {
			return meshyJob{kind: kind, id: id}, nil
		}
	}
	return meshyJob{}, fmt.Errorf("invalid task_id %q: expected %s:<id>, %s:<id> or %s:<id>", raw, meshyKindImage, meshyKindPreview, meshyKindRefine)
}

func meshyPendingResult(toolName string, job meshyJob, task *meshyTask) *tools.Result {
	progress := 0
	if task != nil {
		progress = task.Progress
	}
	return &tools.Result{
		Success: true,
		Output: fmt.Sprintf("Meshy task %s is still running (%d%%). Call %s again with task_id=%s (same format/texture options) to keep waiting. Do not start a new generation - that would be billed again.",
			job, progress, toolName, job),
		Metadata: map[string]interface{}{
			"model_3d_pending": map[string]interface{}{
				"provider": "meshy",
				"task_id":  job.String(),
				"progress": progress,
			},
		},
	}
}

func (t *MeshyGenerate3DTool) createTask(ctx context.Context, endpoint, apiKey string, body map[string]interface{}) (string, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("failed to encode request: %w", err)
	}
	respBody, err := t.do(ctx, http.MethodPost, endpoint, apiKey, payload)
	if err != nil {
		return "", err
	}
	var created struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(respBody, &created); err != nil || strings.TrimSpace(created.Result) == "" {
		return "", fmt.Errorf("unexpected Meshy create response: %s", truncateForError(respBody))
	}
	return created.Result, nil
}

func (t *MeshyGenerate3DTool) waitTask(ctx context.Context, taskURL, apiKey string) (*meshyTask, error) {
	var task meshyTask
	err := pollModel3DTask(ctx, t.pollInterval, func(ctx context.Context) (bool, error) {
		respBody, err := t.do(ctx, http.MethodGet, taskURL, apiKey, nil)
		if err != nil {
			return false, err
		}
		if err := json.Unmarshal(respBody, &task); err != nil {
			return false, fmt.Errorf("failed to parse Meshy task: %w", err)
		}
		switch strings.ToUpper(task.Status) {
		case "SUCCEEDED":
			return true, nil
		case "FAILED", "CANCELED", "EXPIRED":
			msg := strings.TrimSpace(task.TaskError.Message)
			if msg == "" {
				msg = "no details"
			}
			return false, fmt.Errorf("Meshy task %s %s: %s", task.ID, strings.ToLower(task.Status), msg)
		}
		return false, nil
	})
	// The task is returned even on error so timeouts can report progress.
	return &task, err
}

func (t *MeshyGenerate3DTool) do(ctx context.Context, method, endpoint, apiKey string, payload []byte) ([]byte, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to build Meshy request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Meshy request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read Meshy response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Meshy API error (status %d): %s", resp.StatusCode, truncateForError(body))
	}
	return body, nil
}

func (t *MeshyGenerate3DTool) saveResult(ctx context.Context, task *meshyTask, format string) (*tools.Result, error) {
	modelURL := strings.TrimSpace(task.ModelURLs[format])
	if modelURL == "" {
		return &tools.Result{Success: false, Error: fmt.Sprintf("Meshy task %s returned no %s model URL", task.ID, format)}, nil
	}
	outDir := t.outputDir
	if outDir == "" {
		outDir = filepath.Join(t.workDir, "generated", "meshy")
	}
	modelPath, err := downloadModel3DFile(ctx, t.client, modelURL, outDir, task.ID, "."+format)
	if err != nil {
		return &tools.Result{Success: false, Error: "failed to download model: " + err.Error()}, nil
	}
	files := []string{modelPath}
	// OBJ textures live in a sibling MTL file; fetch it so the model is usable.
	if format == "obj" {
		if mtl := strings.TrimSpace(task.ModelURLs["mtl"]); mtl != "" {
			if p, err := downloadModel3DFile(ctx, t.client, mtl, outDir, task.ID, ".mtl"); err == nil {
				files = append(files, p)
			}
		}
	}
	thumbnailPath := ""
	if thumb := strings.TrimSpace(task.ThumbnailURL); thumb != "" {
		if p, err := downloadModel3DFile(ctx, t.client, thumb, outDir, task.ID+"-preview", ".png"); err == nil {
			thumbnailPath = p
		}
	}
	return &tools.Result{
		Success:  true,
		Output:   model3DResultText("Meshy", task.ID, modelPath, thumbnailPath, files),
		Metadata: model3DResultMetadata(t.Name(), "meshy", task.ID, modelPath, thumbnailPath, files),
	}, nil
}

func truncateForError(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 500 {
		s = s[:500] + "..."
	}
	if s == "" {
		s = "empty response"
	}
	return s
}
