package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/google/uuid"
)

const (
	comfyUIDefaultBaseURL      = "http://127.0.0.1:8188"
	comfyUIDefaultWidth        = 512
	comfyUIDefaultHeight       = 512
	comfyUIDefaultSteps        = 20
	comfyUIDefaultCFG          = 7.0
	comfyUIDefaultSampler      = "euler"
	comfyUIDefaultScheduler    = "normal"
	comfyUIDefaultNegative     = "low quality, blurry, deformed"
	comfyUIDefaultTimeout      = 15 * time.Minute
	comfyUIDefaultPollInterval = 500 * time.Millisecond
)

// ComfyUIGenerateImageTool queues a local ComfyUI txt2img workflow, polls until
// complete, then downloads the output image for Caesar preview.
type ComfyUIGenerateImageTool struct {
	store        storage.Store
	outputDir    string
	client       *http.Client
	pollInterval time.Duration
}

type comfyUIGenerateImageParams struct {
	Prompt          string  `json:"prompt"`
	NegativePrompt  string  `json:"negative_prompt,omitempty"`
	Checkpoint      string  `json:"checkpoint,omitempty"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
	Steps           int     `json:"steps,omitempty"`
	CFG             float64 `json:"cfg,omitempty"`
	Seed            *int64  `json:"seed,omitempty"`
	SamplerName     string  `json:"sampler_name,omitempty"`
	Scheduler       string  `json:"scheduler,omitempty"`
	IntegrationID   string  `json:"integration_id,omitempty"`
	IntegrationName string  `json:"integration_name,omitempty"`
}

type comfyUIWorkflowOptions struct {
	Prompt         string
	NegativePrompt string
	Checkpoint     string
	Width          int
	Height         int
	Steps          int
	CFG            float64
	Seed           int64
	SamplerName    string
	Scheduler      string
}

type comfyUIPromptResponse struct {
	PromptID   string                 `json:"prompt_id"`
	Number     int                    `json:"number"`
	NodeErrors map[string]interface{} `json:"node_errors"`
	Error      *comfyUIAPIError       `json:"error"`
}

type comfyUIAPIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type comfyUIHistoryImage struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

func NewComfyUIGenerateImageTool(store storage.Store, outputDir string) *ComfyUIGenerateImageTool {
	return &ComfyUIGenerateImageTool{
		store:        store,
		outputDir:    strings.TrimSpace(outputDir),
		client:       &http.Client{Timeout: comfyUIDefaultTimeout},
		pollInterval: comfyUIDefaultPollInterval,
	}
}

func (t *ComfyUIGenerateImageTool) Name() string {
	return "comfyui_generate_image"
}

func (t *ComfyUIGenerateImageTool) Description() string {
	return "Generate images quickly and cheaply via a local ComfyUI server (default http://127.0.0.1:8188). Uses a built-in txt2img workflow; returns a local file path for Caesar preview."
}

func (t *ComfyUIGenerateImageTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Positive text prompt for the image.",
			},
			"negative_prompt": map[string]interface{}{
				"type":        "string",
				"description": "Optional negative prompt. Defaults to integration config or a quality-oriented baseline.",
			},
			"checkpoint": map[string]interface{}{
				"type":        "string",
				"description": "Checkpoint filename available in ComfyUI models/checkpoints. Overrides integration default.",
			},
			"width": map[string]interface{}{
				"type":        "integer",
				"description": "Image width in pixels (default 512, or integration default).",
			},
			"height": map[string]interface{}{
				"type":        "integer",
				"description": "Image height in pixels (default 512, or integration default).",
			},
			"steps": map[string]interface{}{
				"type":        "integer",
				"description": "Sampler steps (default 20).",
			},
			"cfg": map[string]interface{}{
				"type":        "number",
				"description": "CFG scale (default 7).",
			},
			"seed": map[string]interface{}{
				"type":        "integer",
				"description": "Optional seed. Random when omitted.",
			},
			"sampler_name": map[string]interface{}{
				"type":        "string",
				"description": "ComfyUI sampler name (default euler).",
			},
			"scheduler": map[string]interface{}{
				"type":        "string",
				"description": "ComfyUI scheduler name (default normal).",
			},
			"integration_id": map[string]interface{}{
				"type":        "string",
				"description": "Specific ComfyUI integration ID when multiple are enabled.",
			},
			"integration_name": map[string]interface{}{
				"type":        "string",
				"description": "Specific ComfyUI integration name when multiple are enabled.",
			},
		},
		"required": []string{"prompt"},
	}
}

func (t *ComfyUIGenerateImageTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p comfyUIGenerateImageParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	prompt := strings.TrimSpace(p.Prompt)
	if prompt == "" {
		return &tools.Result{Success: false, Error: "prompt is required"}, nil
	}
	if t.store == nil {
		return &tools.Result{Success: false, Error: "comfyui integration store is unavailable"}, nil
	}

	integration, err := t.selectIntegration(p.IntegrationID, p.IntegrationName)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	baseURL := strings.TrimRight(strings.TrimSpace(integration.Config["base_url"]), "/")
	if baseURL == "" {
		baseURL = comfyUIDefaultBaseURL
	}
	if !strings.HasPrefix(strings.ToLower(baseURL), "http://") && !strings.HasPrefix(strings.ToLower(baseURL), "https://") {
		return &tools.Result{Success: false, Error: "comfyui base_url must start with http:// or https://"}, nil
	}

	timeoutSeconds := parsePositiveInt(integration.Config["timeout_seconds"])
	if timeoutSeconds <= 0 {
		timeoutSeconds = int(comfyUIDefaultTimeout / time.Second)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	checkpoint := firstNonEmpty(p.Checkpoint, integration.Config["checkpoint"])
	if checkpoint == "" {
		checkpoint, err = t.discoverCheckpoint(ctx, baseURL, integration.Config["api_key"])
		if err != nil {
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
	}

	width := firstPositiveInt(p.Width, parsePositiveInt(integration.Config["width"]), comfyUIDefaultWidth)
	height := firstPositiveInt(p.Height, parsePositiveInt(integration.Config["height"]), comfyUIDefaultHeight)
	steps := firstPositiveInt(p.Steps, parsePositiveInt(integration.Config["steps"]), comfyUIDefaultSteps)
	cfg := p.CFG
	if cfg <= 0 {
		cfg = parsePositiveFloat(integration.Config["cfg"], comfyUIDefaultCFG)
	}
	negative := firstNonEmpty(p.NegativePrompt, integration.Config["negative_prompt"], comfyUIDefaultNegative)
	sampler := firstNonEmpty(p.SamplerName, integration.Config["sampler_name"], comfyUIDefaultSampler)
	scheduler := firstNonEmpty(p.Scheduler, integration.Config["scheduler"], comfyUIDefaultScheduler)

	seed := time.Now().UnixNano() & 0x7fffffffffffffff
	if p.Seed != nil {
		seed = *p.Seed
	}

	workflow := buildDefaultComfyUIWorkflow(comfyUIWorkflowOptions{
		Prompt:         prompt,
		NegativePrompt: negative,
		Checkpoint:     checkpoint,
		Width:          width,
		Height:         height,
		Steps:          steps,
		CFG:            cfg,
		Seed:           seed,
		SamplerName:    sampler,
		Scheduler:      scheduler,
	})

	clientID := uuid.NewString()
	promptID, err := t.queuePrompt(ctx, baseURL, integration.Config["api_key"], clientID, workflow)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	images, err := t.waitForImages(ctx, baseURL, integration.Config["api_key"], promptID)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	if len(images) == 0 {
		return &tools.Result{Success: false, Error: "ComfyUI finished without image outputs"}, nil
	}

	localPaths, err := t.downloadImages(ctx, baseURL, integration.Config["api_key"], promptID, images)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	return &tools.Result{
		Success:  true,
		Output:   buildComfyUIToolResultContent(promptID, checkpoint, localPaths),
		Metadata: buildComfyUIToolResultMetadata(localPaths),
	}, nil
}

func buildDefaultComfyUIWorkflow(opts comfyUIWorkflowOptions) map[string]interface{} {
	return map[string]interface{}{
		"3": map[string]interface{}{
			"class_type": "KSampler",
			"inputs": map[string]interface{}{
				"seed":         opts.Seed,
				"steps":        opts.Steps,
				"cfg":          opts.CFG,
				"sampler_name": opts.SamplerName,
				"scheduler":    opts.Scheduler,
				"denoise":      1,
				"model":        []interface{}{"4", 0},
				"positive":     []interface{}{"6", 0},
				"negative":     []interface{}{"7", 0},
				"latent_image": []interface{}{"5", 0},
			},
		},
		"4": map[string]interface{}{
			"class_type": "CheckpointLoaderSimple",
			"inputs": map[string]interface{}{
				"ckpt_name": opts.Checkpoint,
			},
		},
		"5": map[string]interface{}{
			"class_type": "EmptyLatentImage",
			"inputs": map[string]interface{}{
				"width":      opts.Width,
				"height":     opts.Height,
				"batch_size": 1,
			},
		},
		"6": map[string]interface{}{
			"class_type": "CLIPTextEncode",
			"inputs": map[string]interface{}{
				"text": opts.Prompt,
				"clip": []interface{}{"4", 1},
			},
		},
		"7": map[string]interface{}{
			"class_type": "CLIPTextEncode",
			"inputs": map[string]interface{}{
				"text": opts.NegativePrompt,
				"clip": []interface{}{"4", 1},
			},
		},
		"8": map[string]interface{}{
			"class_type": "VAEDecode",
			"inputs": map[string]interface{}{
				"samples": []interface{}{"3", 0},
				"vae":     []interface{}{"4", 2},
			},
		},
		"9": map[string]interface{}{
			"class_type": "SaveImage",
			"inputs": map[string]interface{}{
				"filename_prefix": "a2gent",
				"images":          []interface{}{"8", 0},
			},
		},
	}
}

func (t *ComfyUIGenerateImageTool) queuePrompt(ctx context.Context, baseURL, apiKey, clientID string, workflow map[string]interface{}) (string, error) {
	body, err := json.Marshal(map[string]interface{}{
		"prompt":    workflow,
		"client_id": clientID,
	})
	if err != nil {
		return "", fmt.Errorf("failed to encode ComfyUI prompt: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/prompt", strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("failed to build ComfyUI prompt request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	applyComfyUIAuth(req, apiKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ComfyUI prompt request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", fmt.Errorf("failed to read ComfyUI prompt response: %w", err)
	}

	var apiResp comfyUIPromptResponse
	_ = json.Unmarshal(respBytes, &apiResp)
	if apiResp.Error != nil && strings.TrimSpace(apiResp.Error.Message) != "" {
		return "", fmt.Errorf("ComfyUI API error: %s", apiResp.Error.Message)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(respBytes))
		if detail == "" {
			detail = resp.Status
		}
		return "", fmt.Errorf("ComfyUI API error (status %d): %s", resp.StatusCode, detail)
	}
	if strings.TrimSpace(apiResp.PromptID) == "" {
		return "", fmt.Errorf("ComfyUI returned no prompt_id")
	}
	if len(apiResp.NodeErrors) > 0 {
		raw, _ := json.Marshal(apiResp.NodeErrors)
		return "", fmt.Errorf("ComfyUI node errors: %s", string(raw))
	}
	return apiResp.PromptID, nil
}

func (t *ComfyUIGenerateImageTool) waitForImages(ctx context.Context, baseURL, apiKey, promptID string) ([]comfyUIHistoryImage, error) {
	interval := t.pollInterval
	if interval <= 0 {
		interval = comfyUIDefaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		images, done, err := t.fetchHistoryImages(ctx, baseURL, apiKey, promptID)
		if err != nil {
			return nil, err
		}
		if done {
			return images, nil
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out waiting for ComfyUI generation: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (t *ComfyUIGenerateImageTool) fetchHistoryImages(ctx context.Context, baseURL, apiKey, promptID string) ([]comfyUIHistoryImage, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/history/"+url.PathEscape(promptID), nil)
	if err != nil {
		return nil, false, fmt.Errorf("failed to build ComfyUI history request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	applyComfyUIAuth(req, apiKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("ComfyUI history request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return nil, false, fmt.Errorf("failed to read ComfyUI history response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(respBytes))
		if detail == "" {
			detail = resp.Status
		}
		return nil, false, fmt.Errorf("ComfyUI history error (status %d): %s", resp.StatusCode, detail)
	}

	var history map[string]json.RawMessage
	if err := json.Unmarshal(respBytes, &history); err != nil {
		return nil, false, fmt.Errorf("failed to parse ComfyUI history: %w", err)
	}
	entryRaw, ok := history[promptID]
	if !ok || len(entryRaw) == 0 {
		return nil, false, nil
	}

	var entry struct {
		Outputs map[string]struct {
			Images []comfyUIHistoryImage `json:"images"`
		} `json:"outputs"`
		Status struct {
			Completed bool   `json:"completed"`
			StatusStr string `json:"status_str"`
		} `json:"status"`
	}
	if err := json.Unmarshal(entryRaw, &entry); err != nil {
		return nil, false, fmt.Errorf("failed to parse ComfyUI history entry: %w", err)
	}

	images := make([]comfyUIHistoryImage, 0, 2)
	for _, node := range entry.Outputs {
		images = append(images, node.Images...)
	}

	status := strings.ToLower(strings.TrimSpace(entry.Status.StatusStr))
	if status == "error" || status == "failed" {
		return nil, true, fmt.Errorf("ComfyUI generation failed with status %q", entry.Status.StatusStr)
	}
	if entry.Status.Completed || len(images) > 0 {
		return images, true, nil
	}
	return nil, false, nil
}

func (t *ComfyUIGenerateImageTool) downloadImages(ctx context.Context, baseURL, apiKey, promptID string, images []comfyUIHistoryImage) ([]string, error) {
	outDir := t.outputDir
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "generated", "comfyui")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	paths := make([]string, 0, len(images))
	for i, image := range images {
		filename := strings.TrimSpace(image.Filename)
		if filename == "" {
			continue
		}
		ext := filepath.Ext(filename)
		if ext == "" {
			ext = ".png"
		}
		localName := fmt.Sprintf("%s-%d%s", sanitizeFileToken(promptID), i+1, ext)
		localPath := filepath.Join(outDir, localName)
		if err := t.downloadView(ctx, baseURL, apiKey, image, localPath); err != nil {
			return nil, err
		}
		paths = append(paths, localPath)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("ComfyUI returned image metadata without downloadable files")
	}
	return paths, nil
}

func (t *ComfyUIGenerateImageTool) downloadView(ctx context.Context, baseURL, apiKey string, image comfyUIHistoryImage, localPath string) error {
	q := url.Values{}
	q.Set("filename", image.Filename)
	q.Set("subfolder", image.Subfolder)
	folderType := strings.TrimSpace(image.Type)
	if folderType == "" {
		folderType = "output"
	}
	q.Set("type", folderType)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/view?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("failed to build ComfyUI view request: %w", err)
	}
	applyComfyUIAuth(req, apiKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("ComfyUI view request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = resp.Status
		}
		return fmt.Errorf("ComfyUI view failed (status %d): %s", resp.StatusCode, detail)
	}

	file, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create image file: %w", err)
	}
	defer file.Close()
	if _, err := io.Copy(file, io.LimitReader(resp.Body, 50*1024*1024)); err != nil {
		return fmt.Errorf("failed to write ComfyUI image: %w", err)
	}
	return nil
}

func (t *ComfyUIGenerateImageTool) discoverCheckpoint(ctx context.Context, baseURL, apiKey string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models/checkpoints", nil)
	if err != nil {
		return "", fmt.Errorf("failed to build checkpoint list request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	applyComfyUIAuth(req, apiKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to list ComfyUI checkpoints (set checkpoint in integration config): %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", fmt.Errorf("failed to read checkpoint list: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("failed to list ComfyUI checkpoints (status %d); set checkpoint in integration config", resp.StatusCode)
	}

	var checkpoints []string
	if err := json.Unmarshal(body, &checkpoints); err != nil {
		return "", fmt.Errorf("failed to parse checkpoint list; set checkpoint in integration config: %w", err)
	}
	for _, name := range checkpoints {
		name = strings.TrimSpace(name)
		if name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("no ComfyUI checkpoints found; install a model or set checkpoint in integration config")
}

func (t *ComfyUIGenerateImageTool) selectIntegration(integrationID string, integrationName string) (*storage.Integration, error) {
	all, err := t.store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item.Provider == "comfyui" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled comfyui integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("comfyui integration with id %q not found or disabled", id)
	}

	if name := strings.ToLower(strings.TrimSpace(integrationName)); name != "" {
		var match *storage.Integration
		for _, item := range candidates {
			if strings.ToLower(strings.TrimSpace(item.Name)) != name {
				continue
			}
			if match != nil {
				return nil, fmt.Errorf("multiple comfyui integrations matched name %q; pass integration_id", integrationName)
			}
			match = item
		}
		if match == nil {
			return nil, fmt.Errorf("comfyui integration named %q not found", integrationName)
		}
		return match, nil
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple comfyui integrations are enabled; pass integration_id or integration_name")
}

func applyComfyUIAuth(req *http.Request, apiKey string) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return
	}
	// Optional auth for reverse-proxied ComfyUI instances.
	req.Header.Set("Authorization", "Bearer "+key)
}

func buildComfyUIToolResultContent(promptID, checkpoint string, localPaths []string) string {
	lines := []string{
		fmt.Sprintf("ComfyUI image generation %s completed.", promptID),
		"Checkpoint: " + checkpoint,
		"Saved images:",
	}
	for _, path := range localPaths {
		lines = append(lines, "- "+path)
	}
	return strings.Join(lines, "\n")
}

func buildComfyUIToolResultMetadata(localPaths []string) map[string]interface{} {
	metadata := map[string]interface{}{
		"comfyui_images": map[string]interface{}{
			"paths": localPaths,
		},
	}
	if len(localPaths) > 0 {
		// Keep image_file aligned with Leonardo/OpenAI so Caesar can preview
		// generated images through the existing local asset pipeline.
		metadata["image_file"] = map[string]interface{}{
			"path":        localPaths[0],
			"source_tool": "comfyui_generate_image",
		}
	}
	return metadata
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func parsePositiveFloat(raw string, fallback float64) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func sanitizeFileToken(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return uuid.NewString()
	}
	var b strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return uuid.NewString()
	}
	if len(out) > 64 {
		return out[:64]
	}
	return out
}
