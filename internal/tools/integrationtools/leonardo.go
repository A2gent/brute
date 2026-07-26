package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const (
	leonardoAPIBaseURL            = "https://cloud.leonardo.ai/api/rest/v1"
	leonardoAPIStatusComplete     = "COMPLETE"
	leonardoAPIStatusFailed       = "FAILED"
	leonardoAPIStatusPending      = "PENDING"
	defaultLeonardoWidth          = 1344
	defaultLeonardoHeight         = 768
	defaultLeonardoNumImages      = 1
	leonardoDefaultPollInterval   = 2 * time.Second
	leonardoDefaultTimeout        = 10 * time.Minute
	leonardoHTTPClientTimeout     = 60 * time.Second
)

type LeonardoGenerateImageTool struct {
	store        storage.Store
	outputDir    string
	apiBaseURL   string
	client       *http.Client
	pollInterval time.Duration
}

type LeonardoGenerateImageParams struct {
	Prompt          string `json:"prompt"`
	NegativePrompt  string `json:"negative_prompt,omitempty"`
	IntegrationID   string `json:"integration_id,omitempty"`
	IntegrationName string `json:"integration_name,omitempty"`
	ModelID         string `json:"model_id,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
	NumImages       int    `json:"num_images,omitempty"`
	PresetStyle     string `json:"preset_style,omitempty"`
}

func NewLeonardoGenerateImageTool(store storage.Store, outputDir string) *LeonardoGenerateImageTool {
	return &LeonardoGenerateImageTool{
		store:        store,
		outputDir:    strings.TrimSpace(outputDir),
		pollInterval: leonardoDefaultPollInterval,
		client: &http.Client{
			Timeout: leonardoHTTPClientTimeout,
		},
	}
}

func (t *LeonardoGenerateImageTool) Name() string {
	return "leonardo_generate_image"
}

func (t *LeonardoGenerateImageTool) Description() string {
	return "Generate images with Leonardo AI. Submits a generation request and polls the Leonardo API until images are ready, then returns local file paths for preview in Caesar."
}

func (t *LeonardoGenerateImageTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Prompt describing the image to generate.",
			},
			"negative_prompt": map[string]interface{}{
				"type":        "string",
				"description": "Optional negative prompt.",
			},
			"integration_id": map[string]interface{}{
				"type":        "string",
				"description": "Specific Leonardo integration ID (optional).",
			},
			"integration_name": map[string]interface{}{
				"type":        "string",
				"description": "Specific Leonardo integration name (optional).",
			},
			"model_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional Leonardo model ID override.",
			},
			"width": map[string]interface{}{
				"type":        "integer",
				"description": "Output width in pixels.",
			},
			"height": map[string]interface{}{
				"type":        "integer",
				"description": "Output height in pixels.",
			},
			"num_images": map[string]interface{}{
				"type":        "integer",
				"description": "How many images to request.",
			},
			"preset_style": map[string]interface{}{
				"type":        "string",
				"description": "Optional Leonardo preset style.",
			},
		},
		"required": []string{"prompt"},
	}
}

func (t *LeonardoGenerateImageTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p LeonardoGenerateImageParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	prompt := strings.TrimSpace(p.Prompt)
	if prompt == "" {
		return &tools.Result{Success: false, Error: "prompt is required"}, nil
	}
	if t.store == nil {
		return &tools.Result{Success: false, Error: "leonardo integration is not fully configured on the server"}, nil
	}

	integration, err := t.selectIntegration(p.IntegrationID, p.IntegrationName)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	apiKey := strings.TrimSpace(integration.Config["api_key"])
	if apiKey == "" {
		return &tools.Result{Success: false, Error: "selected leonardo integration is missing api_key"}, nil
	}

	timeoutSeconds := parsePositiveInt(integration.Config["timeout_seconds"])
	if timeoutSeconds <= 0 {
		timeoutSeconds = int(leonardoDefaultTimeout / time.Second)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	requestBody := t.buildGenerationRequest(prompt, p, integration)
	body, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to encode leonardo request: %w", err)
	}

	generationID, err := t.createGeneration(ctx, apiKey, body)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	statusPayload, err := t.waitForGeneration(ctx, apiKey, generationID)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	status := strings.ToUpper(strings.TrimSpace(extractLeonardoStatus(statusPayload)))
	if status == leonardoAPIStatusFailed {
		detail := extractLeonardoError(statusPayload)
		if detail == "" {
			detail = "Leonardo generation failed"
		}
		return &tools.Result{Success: false, Error: fmt.Sprintf("Leonardo generation %s failed: %s", generationID, detail)}, nil
	}
	if status != leonardoAPIStatusComplete {
		return &tools.Result{Success: false, Error: fmt.Sprintf("Leonardo generation %s ended with unexpected status %q", generationID, status)}, nil
	}

	imageURLs := extractLeonardoImageURLs(statusPayload)
	if len(imageURLs) == 0 {
		return &tools.Result{Success: false, Error: fmt.Sprintf("Leonardo generation %s completed without image URLs", generationID)}, nil
	}

	localPaths, err := t.downloadImages(ctx, generationID, imageURLs)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	return &tools.Result{
		Success:  true,
		Output:   buildLeonardoToolResultContent(generationID, localPaths, imageURLs),
		Metadata: buildLeonardoToolResultMetadata(localPaths, imageURLs),
	}, nil
}

func (t *LeonardoGenerateImageTool) buildGenerationRequest(prompt string, p LeonardoGenerateImageParams, integration *storage.Integration) map[string]interface{} {
	requestBody := map[string]interface{}{
		"prompt": prompt,
	}
	if negative := strings.TrimSpace(p.NegativePrompt); negative != "" {
		requestBody["negative_prompt"] = negative
	}
	if modelID := strings.TrimSpace(p.ModelID); modelID != "" {
		requestBody["modelId"] = modelID
	} else if modelID := strings.TrimSpace(integration.Config["model_id"]); modelID != "" {
		requestBody["modelId"] = modelID
	}
	if style := strings.TrimSpace(p.PresetStyle); style != "" {
		requestBody["presetStyle"] = style
	} else if style := strings.TrimSpace(integration.Config["preset_style"]); style != "" {
		requestBody["presetStyle"] = style
	}

	width := p.Width
	if width <= 0 {
		width = parsePositiveInt(integration.Config["width"])
	}
	height := p.Height
	if height <= 0 {
		height = parsePositiveInt(integration.Config["height"])
	}
	if width <= 0 {
		width = defaultLeonardoWidth
	}
	if height <= 0 {
		height = defaultLeonardoHeight
	}
	requestBody["width"] = width
	requestBody["height"] = height

	numImages := p.NumImages
	if numImages <= 0 {
		numImages = parsePositiveInt(integration.Config["num_images"])
	}
	if numImages <= 0 {
		numImages = defaultLeonardoNumImages
	}
	requestBody["num_images"] = numImages
	return requestBody
}

func (t *LeonardoGenerateImageTool) baseURL() string {
	if base := strings.TrimSpace(t.apiBaseURL); base != "" {
		return strings.TrimRight(base, "/")
	}
	return leonardoAPIBaseURL
}

func (t *LeonardoGenerateImageTool) createGeneration(ctx context.Context, apiKey string, body []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL()+"/generations", strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("failed to build leonardo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("leonardo request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", fmt.Errorf("failed to read leonardo response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(respBody))
		if detail == "" {
			detail = resp.Status
		}
		return "", fmt.Errorf("leonardo API error (status %d): %s", resp.StatusCode, detail)
	}

	generationID := extractLeonardoGenerationID(respBody)
	if generationID == "" {
		return "", fmt.Errorf("leonardo response did not include a generation id")
	}
	return generationID, nil
}

func (t *LeonardoGenerateImageTool) waitForGeneration(ctx context.Context, apiKey, generationID string) ([]byte, error) {
	interval := t.pollInterval
	if interval <= 0 {
		interval = leonardoDefaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		payload, status, err := t.fetchGenerationStatus(ctx, apiKey, generationID)
		if err != nil {
			return nil, err
		}

		switch strings.ToUpper(strings.TrimSpace(status)) {
		case leonardoAPIStatusComplete, leonardoAPIStatusFailed:
			return payload, nil
		case leonardoAPIStatusPending, "":
			// Keep polling until Leonardo reports a terminal status.
		default:
			// Unknown in-progress statuses are treated as pending.
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out waiting for Leonardo generation %s: %w", generationID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (t *LeonardoGenerateImageTool) fetchGenerationStatus(ctx context.Context, apiKey, generationID string) ([]byte, string, error) {
	endpoint := t.baseURL() + "/generations/" + url.PathEscape(generationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to build leonardo status request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("leonardo status request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read leonardo status response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(respBody))
		if detail == "" {
			detail = resp.Status
		}
		return nil, "", fmt.Errorf("leonardo status API error (status %d): %s", resp.StatusCode, detail)
	}

	return respBody, extractLeonardoStatus(respBody), nil
}

func (t *LeonardoGenerateImageTool) downloadImages(ctx context.Context, generationID string, imageURLs []string) ([]string, error) {
	outDir := t.outputDir
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "generated", "leonardo")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create leonardo output folder: %w", err)
	}

	localPaths := make([]string, 0, len(imageURLs))
	for idx, rawURL := range imageURLs {
		path, err := t.downloadImage(ctx, outDir, generationID, idx, rawURL)
		if err != nil {
			return nil, err
		}
		localPaths = append(localPaths, path)
	}
	return localPaths, nil
}

func (t *LeonardoGenerateImageTool) downloadImage(ctx context.Context, outDir, generationID string, index int, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to build image download request: %w", err)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download generated image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("failed to download generated image: %s", resp.Status)
	}

	ext := imageFileExtension(rawURL, resp.Header.Get("Content-Type"))
	path := filepath.Join(outDir, fmt.Sprintf("%s-%d%s", generationID, index+1, ext))
	file, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("failed to create generated image file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, io.LimitReader(resp.Body, 20*1024*1024)); err != nil {
		return "", fmt.Errorf("failed to store generated image: %w", err)
	}
	return path, nil
}

func buildLeonardoToolResultContent(generationID string, localPaths []string, imageURLs []string) string {
	lines := []string{
		fmt.Sprintf("Leonardo generation %s completed.", generationID),
	}
	if len(localPaths) > 0 {
		lines = append(lines, "Downloaded images:")
		for _, path := range localPaths {
			lines = append(lines, "- "+path)
		}
	}
	if len(localPaths) == 0 && len(imageURLs) > 0 {
		lines = append(lines, "Image URLs:")
		for _, rawURL := range imageURLs {
			lines = append(lines, "- "+rawURL)
		}
	}
	return strings.Join(lines, "\n")
}

func buildLeonardoToolResultMetadata(localPaths []string, imageURLs []string) map[string]interface{} {
	metadata := map[string]interface{}{
		"leonardo_images": map[string]interface{}{
			"paths": localPaths,
			"urls":  imageURLs,
		},
	}
	if len(localPaths) > 0 {
		metadata["image_file"] = map[string]interface{}{
			"path":        localPaths[0],
			"source_tool": "leonardo_generate_image",
		}
	}
	return metadata
}

func imageFileExtension(rawURL string, contentType string) string {
	if parsed, err := url.Parse(strings.TrimSpace(rawURL)); err == nil {
		if ext := strings.ToLower(filepath.Ext(parsed.Path)); ext != "" && len(ext) <= 5 {
			return ext
		}
	}
	if exts, _ := mime.ExtensionsByType(strings.TrimSpace(contentType)); len(exts) > 0 {
		return exts[0]
	}
	return ".png"
}

func parsePositiveInt(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func (t *LeonardoGenerateImageTool) selectIntegration(integrationID string, integrationName string) (*storage.Integration, error) {
	all, err := t.store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}

	candidates := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item.Provider == "leonardo" && item.Enabled {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no enabled leonardo integrations found")
	}

	if id := strings.TrimSpace(integrationID); id != "" {
		for _, item := range candidates {
			if item.ID == id {
				return item, nil
			}
		}
		return nil, fmt.Errorf("leonardo integration with id %q not found or disabled", id)
	}

	if name := strings.ToLower(strings.TrimSpace(integrationName)); name != "" {
		var match *storage.Integration
		for _, item := range candidates {
			if strings.ToLower(strings.TrimSpace(item.Name)) != name {
				continue
			}
			if match != nil {
				return nil, fmt.Errorf("multiple leonardo integrations matched name %q; pass integration_id", integrationName)
			}
			match = item
		}
		if match == nil {
			return nil, fmt.Errorf("leonardo integration named %q not found", integrationName)
		}
		return match, nil
	}

	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, fmt.Errorf("multiple leonardo integrations are enabled; pass integration_id or integration_name")
}

func extractLeonardoGenerationID(raw []byte) string {
	var payload interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	candidates := []string{
		findStringByKey(payload, "generationId"),
		findStringByKey(payload, "generation_id"),
		findStringByKey(payload, "id"),
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && strings.Contains(candidate, "-") {
			return candidate
		}
	}
	return strings.TrimSpace(candidates[0])
}

func extractLeonardoStatus(raw []byte) string {
	var payload interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(findStringByKey(payload, "status")))
}

func extractLeonardoError(raw []byte) string {
	var payload interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	for _, key := range []string{"error", "message", "detail"} {
		if value := strings.TrimSpace(findStringByKey(payload, key)); value != "" {
			return value
		}
	}
	return ""
}

func extractLeonardoImageURLs(raw []byte) []string {
	var payload interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	urls := make([]string, 0)
	collectStringsByKey(payload, "url", &urls, seen)
	collectStringsByKey(payload, "imageUrl", &urls, seen)
	collectStringsByKey(payload, "image_url", &urls, seen)
	filtered := make([]string, 0, len(urls))
	for _, candidate := range urls {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(candidate)), "http") {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func findStringByKey(value interface{}, key string) string {
	switch item := value.(type) {
	case map[string]interface{}:
		for currentKey, currentValue := range item {
			if strings.EqualFold(strings.TrimSpace(currentKey), key) {
				if text, ok := currentValue.(string); ok {
					return text
				}
			}
			if nested := findStringByKey(currentValue, key); nested != "" {
				return nested
			}
		}
	case []interface{}:
		for _, currentValue := range item {
			if nested := findStringByKey(currentValue, key); nested != "" {
				return nested
			}
		}
	}
	return ""
}

func collectStringsByKey(value interface{}, key string, out *[]string, seen map[string]struct{}) {
	switch item := value.(type) {
	case map[string]interface{}:
		for currentKey, currentValue := range item {
			if strings.EqualFold(strings.TrimSpace(currentKey), key) {
				if text, ok := currentValue.(string); ok {
					text = strings.TrimSpace(text)
					if text != "" {
						if _, exists := seen[text]; !exists {
							seen[text] = struct{}{}
							*out = append(*out, text)
						}
					}
				}
			}
			collectStringsByKey(currentValue, key, out, seen)
		}
	case []interface{}:
		for _, currentValue := range item {
			collectStringsByKey(currentValue, key, out, seen)
		}
	}
}
