package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const (
	// Tripo API v2 is retired on 2026-11-01, so only v3 is implemented.
	tripoDefaultBaseURL = "https://openapi.tripo3d.ai/v3"
	tripoDefaultModel   = "v3.1-20260211"
)

// TripoGenerate3DTool runs Tripo3D text-to-model or image-to-model tasks,
// waits for completion and saves the model file plus preview locally.
type TripoGenerate3DTool struct {
	store        storage.Store
	workDir      string
	outputDir    string
	client       *http.Client
	pollInterval time.Duration
}

type tripoGenerate3DParams struct {
	Prompt         string `json:"prompt"`
	NegativePrompt string `json:"negative_prompt"`
	Image          string `json:"image"`
	Texture        *bool  `json:"texture"`
	PBR            *bool  `json:"pbr"`
	Model          string `json:"model"`
	FaceLimit      int    `json:"face_limit"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type tripoEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type tripoTask struct {
	TaskID       string `json:"task_id"`
	Status       string `json:"status"`
	Progress     int    `json:"progress"`
	ErrorCode    int    `json:"error_code"`
	ErrorMessage string `json:"error_message"`
	Output       struct {
		ModelURL         string `json:"model_url"`
		RenderedImageURL string `json:"rendered_image_url"`
	} `json:"output"`
}

func NewTripoGenerate3DTool(store storage.Store, workDir, outputDir string) *TripoGenerate3DTool {
	return &TripoGenerate3DTool{
		store:        store,
		workDir:      strings.TrimSpace(workDir),
		outputDir:    strings.TrimSpace(outputDir),
		client:       &http.Client{Timeout: 120 * time.Second},
		pollInterval: model3DDefaultPollInterval,
	}
}

func (t *TripoGenerate3DTool) Name() string { return "tripo_generate_3d" }

func (t *TripoGenerate3DTool) Description() string {
	return "Generate a 3D model with Tripo3D from a text prompt (text-to-model) or from an image (image-to-model). " +
		"The image can be a local file path (for example one produced by leonardo_generate_image), a public URL, or a Tripo image task_id. " +
		"Waits for the task to finish and returns the local path of the downloaded model file (usually GLB) and a preview image."
}

func (t *TripoGenerate3DTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Text description of the object (max 1024 chars). Required unless image is set; ignored for image-to-model.",
			},
			"negative_prompt": map[string]interface{}{
				"type":        "string",
				"description": "What to avoid in text-to-model (max 255 chars).",
			},
			"image": map[string]interface{}{
				"type":        "string",
				"description": "Input image for image-to-model: local path (relative to the workspace or absolute), public http(s) URL, or Tripo task_id. PNG, JPEG or WebP up to 20 MB.",
			},
			"texture": map[string]interface{}{
				"type":        "boolean",
				"description": "Generate textures (default true).",
			},
			"pbr": map[string]interface{}{
				"type":        "boolean",
				"description": "Generate PBR materials (default true; forces texture on).",
			},
			"model": map[string]interface{}{
				"type":        "string",
				"description": "Tripo model version, e.g. v3.1-20260211 (default), v3.0-20250812, v2.5-20250123.",
			},
			"face_limit": map[string]interface{}{
				"type":        "integer",
				"description": "Optional max polycount of the output mesh.",
			},
			"timeout_seconds": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum time to wait for the task (default from integration or 600).",
			},
		},
	}
}

func (t *TripoGenerate3DTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p tripoGenerate3DParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	p.Prompt = strings.TrimSpace(p.Prompt)
	p.Image = strings.TrimSpace(p.Image)
	if p.Prompt == "" && p.Image == "" {
		return &tools.Result{Success: false, Error: "either prompt or image is required"}, nil
	}
	integration, err := findModel3DIntegration(ctx, t.store, "tripo")
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	baseURL := integration.BaseURL
	if baseURL == "" {
		baseURL = tripoDefaultBaseURL
	}
	model := firstNonEmpty(strings.TrimSpace(p.Model), integration.Model, tripoDefaultModel)
	timeout := integration.Timeout
	if p.TimeoutSeconds > 0 {
		timeout = time.Duration(p.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body := map[string]interface{}{"model": model}
	if p.Texture != nil {
		body["texture"] = *p.Texture
	}
	if p.PBR != nil {
		body["pbr"] = *p.PBR
	}
	if p.FaceLimit > 0 {
		body["face_limit"] = p.FaceLimit
	}
	endpoint := baseURL + "/generation/text-to-model"
	if p.Image != "" {
		input, err := t.resolveImageInput(ctx, baseURL, integration.APIKey, p.Image)
		if err != nil {
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
		body["input"] = input
		endpoint = baseURL + "/generation/image-to-model"
	} else {
		body["prompt"] = p.Prompt
		if np := strings.TrimSpace(p.NegativePrompt); np != "" {
			body["negative_prompt"] = np
		}
	}

	taskID, err := t.createTask(ctx, endpoint, integration.APIKey, body)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	task, err := t.waitTask(ctx, baseURL+"/tasks/"+taskID, integration.APIKey)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	return t.saveResult(ctx, task)
}

// resolveImageInput maps the image argument to Tripo's single "input" field:
// URLs and task IDs pass through, local files are uploaded for a file_token.
func (t *TripoGenerate3DTool) resolveImageInput(ctx context.Context, baseURL, apiKey, raw string) (string, error) {
	if strings.HasPrefix(raw, "task_") || strings.HasPrefix(raw, "file_") {
		return raw, nil
	}
	img, err := resolveModel3DImage(raw, t.workDir)
	if err != nil {
		return "", err
	}
	if img.URL != "" {
		if strings.HasPrefix(strings.ToLower(img.URL), "data:") {
			return "", fmt.Errorf("Tripo does not accept data URIs; pass a local file path or URL")
		}
		return img.URL, nil
	}
	return t.uploadFile(ctx, baseURL, apiKey, img)
}

func (t *TripoGenerate3DTool) uploadFile(ctx context.Context, baseURL, apiKey string, img *model3DImageInput) (string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", filepath.Base(img.Path))
	if err != nil {
		return "", fmt.Errorf("failed to build upload: %w", err)
	}
	if _, err := part.Write(img.Data); err != nil {
		return "", fmt.Errorf("failed to build upload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to build upload: %w", err)
	}
	data, err := t.do(ctx, http.MethodPost, baseURL+"/files", apiKey, writer.FormDataContentType(), buf.Bytes())
	if err != nil {
		return "", fmt.Errorf("Tripo image upload failed: %w", err)
	}
	var uploaded struct {
		FileToken string `json:"file_token"`
	}
	if err := json.Unmarshal(data, &uploaded); err != nil || uploaded.FileToken == "" {
		return "", fmt.Errorf("unexpected Tripo upload response: %s", truncateForError(data))
	}
	return uploaded.FileToken, nil
}

func (t *TripoGenerate3DTool) createTask(ctx context.Context, endpoint, apiKey string, body map[string]interface{}) (string, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("failed to encode request: %w", err)
	}
	data, err := t.do(ctx, http.MethodPost, endpoint, apiKey, "application/json", payload)
	if err != nil {
		return "", err
	}
	var created struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(data, &created); err != nil || created.TaskID == "" {
		return "", fmt.Errorf("unexpected Tripo create response: %s", truncateForError(data))
	}
	return created.TaskID, nil
}

func (t *TripoGenerate3DTool) waitTask(ctx context.Context, taskURL, apiKey string) (*tripoTask, error) {
	var task tripoTask
	err := pollModel3DTask(ctx, t.pollInterval, func(ctx context.Context) (bool, error) {
		data, err := t.do(ctx, http.MethodGet, taskURL, apiKey, "", nil)
		if err != nil {
			return false, err
		}
		if err := json.Unmarshal(data, &task); err != nil {
			return false, fmt.Errorf("failed to parse Tripo task: %w", err)
		}
		switch strings.ToLower(task.Status) {
		case "success":
			return true, nil
		case "failed", "cancelled", "banned", "expired":
			msg := strings.TrimSpace(task.ErrorMessage)
			if msg == "" {
				msg = fmt.Sprintf("error code %d", task.ErrorCode)
			}
			return false, fmt.Errorf("Tripo task %s %s: %s", task.TaskID, strings.ToLower(task.Status), msg)
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// do performs a Tripo request and returns the unwrapped "data" payload.
func (t *TripoGenerate3DTool) do(ctx context.Context, method, endpoint, apiKey, contentType string, payload []byte) ([]byte, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to build Tripo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Tripo request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read Tripo response: %w", err)
	}
	var env tripoEnvelope
	_ = json.Unmarshal(body, &env)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || env.Code != 0 {
		detail := strings.TrimSpace(env.Message)
		if detail == "" {
			detail = truncateForError(body)
		}
		return nil, fmt.Errorf("Tripo API error (status %d, code %d): %s", resp.StatusCode, env.Code, detail)
	}
	return env.Data, nil
}

func (t *TripoGenerate3DTool) saveResult(ctx context.Context, task *tripoTask) (*tools.Result, error) {
	modelURL := strings.TrimSpace(task.Output.ModelURL)
	if modelURL == "" {
		return &tools.Result{Success: false, Error: fmt.Sprintf("Tripo task %s returned no model URL", task.TaskID)}, nil
	}
	outDir := t.outputDir
	if outDir == "" {
		outDir = filepath.Join(t.workDir, "generated", "tripo")
	}
	// Extension comes from the URL: GLB by default, FBX when quad output is used.
	modelPath, err := downloadModel3DFile(ctx, t.client, modelURL, outDir, task.TaskID, model3DURLExt(modelURL, ".glb"))
	if err != nil {
		return &tools.Result{Success: false, Error: "failed to download model: " + err.Error()}, nil
	}
	thumbnailPath := ""
	if thumb := strings.TrimSpace(task.Output.RenderedImageURL); thumb != "" {
		if p, err := downloadModel3DFile(ctx, t.client, thumb, outDir, task.TaskID+"-preview", model3DURLExt(thumb, ".png")); err == nil {
			thumbnailPath = p
		}
	}
	files := []string{modelPath}
	return &tools.Result{
		Success:  true,
		Output:   model3DResultText("Tripo", task.TaskID, modelPath, thumbnailPath, files),
		Metadata: model3DResultMetadata(t.Name(), "tripo", task.TaskID, modelPath, thumbnailPath, files),
	}, nil
}
