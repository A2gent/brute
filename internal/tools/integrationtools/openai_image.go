package integrationtools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/tools"
	"github.com/google/uuid"
)

const (
	openAIDefaultImageModel = "gpt-image-1"
	openAIDefaultImageSize  = "1024x1024"
	openAIImagesEndpoint    = "/images/generations"
	openAIDefaultBaseURL    = "https://api.openai.com/v1"
)

// OpenAIGenerateImageTool calls the OpenAI Images API synchronously and saves
// results as local files. It reads the "openai" provider config for credentials.
type OpenAIGenerateImageTool struct {
	cfg       *config.Config
	outputDir string
	client    *http.Client
}

type openAIGenerateImageParams struct {
	Prompt  string `json:"prompt"`
	Model   string `json:"model,omitempty"`
	Size    string `json:"size,omitempty"`
	Quality string `json:"quality,omitempty"`
	N       int    `json:"n,omitempty"`
}

type openAIImagesRequest struct {
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	N       int    `json:"n"`
	Size    string `json:"size,omitempty"`
	Quality string `json:"quality,omitempty"`
}

type openAIImagesResponse struct {
	Created int64              `json:"created"`
	Data    []openAIImageDatum `json:"data"`
	Error   *openAIAPIError    `json:"error,omitempty"`
}

type openAIImageDatum struct {
	B64JSON       string `json:"b64_json,omitempty"`
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type openAIAPIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func NewOpenAIGenerateImageTool(cfg *config.Config, outputDir string) *OpenAIGenerateImageTool {
	return &OpenAIGenerateImageTool{
		cfg:       cfg,
		outputDir: strings.TrimSpace(outputDir),
		client:    &http.Client{Timeout: 120 * time.Second},
	}
}

func (t *OpenAIGenerateImageTool) Name() string {
	return "openai_generate_image"
}

func (t *OpenAIGenerateImageTool) Description() string {
	return "Generate images using the OpenAI Images API (e.g. gpt-image-1). Returns generated images as local file paths for preview in Caesar."
}

func (t *OpenAIGenerateImageTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Text description of the image to generate.",
			},
			"model": map[string]interface{}{
				"type":        "string",
				"description": "OpenAI image model to use (default: gpt-image-1).",
			},
			"size": map[string]interface{}{
				"type":        "string",
				"description": "Image size, e.g. 1024x1024, 1792x1024, 1024x1792 (model-dependent).",
			},
			"quality": map[string]interface{}{
				"type":        "string",
				"description": "Image quality: standard, hd, or auto (model-dependent).",
			},
			"n": map[string]interface{}{
				"type":        "integer",
				"description": "Number of images to generate (default 1).",
			},
		},
		"required": []string{"prompt"},
	}
}

func (t *OpenAIGenerateImageTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p openAIGenerateImageParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	prompt := strings.TrimSpace(p.Prompt)
	if prompt == "" {
		return &tools.Result{Success: false, Error: "prompt is required"}, nil
	}

	apiKey, baseURL := t.resolveCredentials()
	if apiKey == "" {
		return &tools.Result{Success: false, Error: "openai provider is not configured: set api_key in the openai provider settings"}, nil
	}

	model := strings.TrimSpace(p.Model)
	if model == "" {
		model = openAIDefaultImageModel
	}
	size := strings.TrimSpace(p.Size)
	if size == "" {
		size = openAIDefaultImageSize
	}
	n := p.N
	if n <= 0 {
		n = 1
	}

	reqBody := openAIImagesRequest{
		Model:  model,
		Prompt: prompt,
		N:      n,
	}
	if quality := strings.TrimSpace(p.Quality); quality != "" {
		reqBody.Quality = quality
	}
	if !strings.EqualFold(model, openAIDefaultImageModel) || size != openAIDefaultImageSize {
		reqBody.Size = size
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}

	endpoint := strings.TrimRight(baseURL, "/") + openAIImagesEndpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("OpenAI request failed: %v", err)}, nil
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to read OpenAI response: %v", err)}, nil
	}

	var apiResp openAIImagesResponse
	if err := json.Unmarshal(respBytes, &apiResp); err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to parse OpenAI response: %v", err)}, nil
	}

	if apiResp.Error != nil && strings.TrimSpace(apiResp.Error.Message) != "" {
		return &tools.Result{Success: false, Error: fmt.Sprintf("OpenAI API error: %s", apiResp.Error.Message)}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(respBytes))
		if detail == "" {
			detail = resp.Status
		}
		return &tools.Result{Success: false, Error: fmt.Sprintf("OpenAI API error (status %d): %s", resp.StatusCode, detail)}, nil
	}
	if len(apiResp.Data) == 0 {
		return &tools.Result{Success: false, Error: "OpenAI returned no image data"}, nil
	}

	generationID := uuid.NewString()
	localPaths, imageURLs, err := t.saveImages(ctx, generationID, apiResp.Data)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to save generated images: %v", err)}, nil
	}
	if len(localPaths) == 0 && len(imageURLs) == 0 {
		return &tools.Result{Success: false, Error: "OpenAI returned image entries, but none included supported image content"}, nil
	}

	return &tools.Result{
		Success:  true,
		Output:   buildOpenAIToolResultContent(generationID, localPaths, imageURLs, apiResp.Data),
		Metadata: buildOpenAIToolResultMetadata(localPaths, imageURLs),
	}, nil
}

func (t *OpenAIGenerateImageTool) resolveCredentials() (apiKey, baseURL string) {
	if t.cfg == nil {
		return "", ""
	}
	provider, ok := t.cfg.Providers[string(config.ProviderOpenAI)]
	if !ok {
		return "", ""
	}
	apiKey = strings.TrimSpace(provider.APIKey)
	baseURL = strings.TrimSpace(provider.BaseURL)
	if baseURL == "" {
		baseURL = openAIDefaultBaseURL
	}
	return apiKey, baseURL
}

func (t *OpenAIGenerateImageTool) saveImages(ctx context.Context, generationID string, data []openAIImageDatum) ([]string, []string, error) {
	outDir := t.outputDir
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "generated", "openai")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	paths := make([]string, 0, len(data))
	urls := make([]string, 0, len(data))
	for i, datum := range data {
		filename := fmt.Sprintf("%s-%d.png", generationID, i+1)
		path := filepath.Join(outDir, filename)

		b64 := strings.TrimSpace(datum.B64JSON)
		if b64 != "" {
			decoded, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to decode image %d: %w", i+1, err)
			}
			if err := os.WriteFile(path, decoded, 0o644); err != nil {
				return nil, nil, fmt.Errorf("failed to write image %d: %w", i+1, err)
			}
			paths = append(paths, path)
			continue
		}

		rawURL := strings.TrimSpace(datum.URL)
		if rawURL == "" {
			continue
		}
		if err := t.downloadImage(ctx, path, rawURL); err != nil {
			return nil, nil, fmt.Errorf("failed to download image %d: %w", i+1, err)
		}
		paths = append(paths, path)
		urls = append(urls, rawURL)
	}
	return paths, urls, nil
}

func (t *OpenAIGenerateImageTool) downloadImage(ctx context.Context, localPath, rawURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("failed to build image download request: %w", err)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("image download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = resp.Status
		}
		return fmt.Errorf("image download failed (status %d): %s", resp.StatusCode, detail)
	}

	file, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create image file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, io.LimitReader(resp.Body, 50*1024*1024)); err != nil {
		return fmt.Errorf("failed to write downloaded image: %w", err)
	}
	return nil
}

func buildOpenAIToolResultContent(generationID string, localPaths []string, imageURLs []string, data []openAIImageDatum) string {
	lines := []string{fmt.Sprintf("OpenAI image generation %s completed.", generationID)}
	if len(localPaths) > 0 {
		lines = append(lines, "Saved images:")
		for _, p := range localPaths {
			lines = append(lines, "- "+p)
		}
	}
	if len(imageURLs) > 0 {
		lines = append(lines, "Source image URLs:")
		for _, rawURL := range imageURLs {
			lines = append(lines, "- "+rawURL)
		}
	}
	for _, d := range data {
		if revised := strings.TrimSpace(d.RevisedPrompt); revised != "" {
			lines = append(lines, "Revised prompt: "+revised)
			break
		}
	}
	return strings.Join(lines, "\n")
}

func buildOpenAIToolResultMetadata(localPaths []string, imageURLs []string) map[string]interface{} {
	metadata := map[string]interface{}{
		"openai_images": map[string]interface{}{
			"paths": localPaths,
			"urls":  imageURLs,
		},
	}
	if len(localPaths) > 0 {
		// Keep image_file aligned with Leonardo so Caesar can preview generated
		// images through the existing local asset pipeline.
		metadata["image_file"] = map[string]interface{}{
			"path":        localPaths[0],
			"source_tool": "openai_generate_image",
		}
	}
	return metadata
}
