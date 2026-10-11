package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
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
}

type meshyGenerate3DParams struct {
	Prompt         string `json:"prompt"`
	Image          string `json:"image"`
	TexturePrompt  string `json:"texture_prompt"`
	Texture        *bool  `json:"texture"`
	PBR            bool   `json:"pbr"`
	AIModel        string `json:"ai_model"`
	Format         string `json:"format"`
	TimeoutSeconds int    `json:"timeout_seconds"`
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
	}
}

func (t *MeshyGenerate3DTool) Name() string { return "meshy_generate_3d" }

func (t *MeshyGenerate3DTool) Description() string {
	return "Generate a 3D model with Meshy AI from a text prompt (text-to-3D) or from an image (image-to-3D). " +
		"The image can be a local file path (for example one produced by leonardo_generate_image) or a public URL. " +
		"Waits for the task to finish (usually 1-5 minutes) and returns the local path of the downloaded model file and a preview image."
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
				"description": "Maximum time to wait for the task (default from integration or 600).",
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
	if p.Prompt == "" && p.Image == "" {
		return &tools.Result{Success: false, Error: "either prompt or image is required"}, nil
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
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var task *meshyTask
	if p.Image != "" {
		task, err = t.runImageTo3D(ctx, baseURL, integration.APIKey, p, aiModel, format, texture)
	} else {
		task, err = t.runTextTo3D(ctx, baseURL, integration.APIKey, p, aiModel, format, texture)
	}
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	return t.saveResult(ctx, task, format)
}

func (t *MeshyGenerate3DTool) runImageTo3D(ctx context.Context, baseURL, apiKey string, p meshyGenerate3DParams, aiModel, format string, texture bool) (*meshyTask, error) {
	img, err := resolveModel3DImage(p.Image, t.workDir)
	if err != nil {
		return nil, err
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
	endpoint := baseURL + "/openapi/v1/image-to-3d"
	id, err := t.createTask(ctx, endpoint, apiKey, body)
	if err != nil {
		return nil, err
	}
	return t.waitTask(ctx, endpoint+"/"+id, apiKey)
}

func (t *MeshyGenerate3DTool) runTextTo3D(ctx context.Context, baseURL, apiKey string, p meshyGenerate3DParams, aiModel, format string, texture bool) (*meshyTask, error) {
	endpoint := baseURL + "/openapi/v2/text-to-3d"
	preview := map[string]interface{}{
		"mode":           "preview",
		"prompt":         p.Prompt,
		"target_formats": []string{format},
	}
	if aiModel != "" {
		preview["ai_model"] = aiModel
	}
	previewID, err := t.createTask(ctx, endpoint, apiKey, preview)
	if err != nil {
		return nil, err
	}
	task, err := t.waitTask(ctx, endpoint+"/"+previewID, apiKey)
	if err != nil || !texture {
		return task, err
	}
	// Meshy text-to-3D is two-step: preview builds the mesh, refine textures it.
	refine := map[string]interface{}{
		"mode":            "refine",
		"preview_task_id": previewID,
		"target_formats":  []string{format},
	}
	if p.PBR {
		refine["enable_pbr"] = true
	}
	if tp := strings.TrimSpace(p.TexturePrompt); tp != "" {
		refine["texture_prompt"] = tp
	}
	refineID, err := t.createTask(ctx, endpoint, apiKey, refine)
	if err != nil {
		return nil, err
	}
	return t.waitTask(ctx, endpoint+"/"+refineID, apiKey)
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
	if err != nil {
		return nil, err
	}
	return &task, nil
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
