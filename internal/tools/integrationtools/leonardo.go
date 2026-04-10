package integrationtools

import (
	"context"
	"encoding/base64"
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

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/google/uuid"
)

const (
	leonardoAPIBaseURL                 = "https://cloud.leonardo.ai/api/rest/v1"
	leonardoGenerationStatusFailed     = "failed"
	leonardoGenerationStatusDone       = "completed"
	leonardoGenerationStatusProcessing = "processing"
	leonardoGenerationStatusWait       = "pending"
	defaultLeonardoWidth               = 1344
	defaultLeonardoHeight              = 768
	defaultLeonardoNumImages           = 1
)

type LeonardoGenerateImageTool struct {
	store          storage.Store
	sessionManager *session.Manager
	client         *http.Client
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

type LeonardoWebhookProcessor struct {
	store          storage.Store
	sessionManager *session.Manager
	outputDir      string
	client         *http.Client
}

func NewLeonardoGenerateImageTool(store storage.Store, sessionManager *session.Manager) *LeonardoGenerateImageTool {
	return &LeonardoGenerateImageTool{
		store:          store,
		sessionManager: sessionManager,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func NewLeonardoWebhookProcessor(store storage.Store, sessionManager *session.Manager, dataPath string) *LeonardoWebhookProcessor {
	root := strings.TrimSpace(dataPath)
	if root == "" {
		root = os.TempDir()
	}
	return &LeonardoWebhookProcessor{
		store:          store,
		sessionManager: sessionManager,
		outputDir:      filepath.Join(root, "generated", "leonardo"),
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (t *LeonardoGenerateImageTool) Name() string {
	return "leonardo_generate_image"
}

func (t *LeonardoGenerateImageTool) Description() string {
	return "Generate images with Leonardo AI. This is asynchronous: the session pauses until Square relays the Leonardo webhook back to the agent."
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
	if t.store == nil || t.sessionManager == nil {
		return &tools.Result{Success: false, Error: "leonardo integration is not fully configured on the server"}, nil
	}

	sessionID, _ := ctx.Value("session_id").(string)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return &tools.Result{Success: false, Error: "leonardo_generate_image requires an active session"}, nil
	}
	toolCallID, _ := ctx.Value("tool_call_id").(string)
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return &tools.Result{Success: false, Error: "leonardo_generate_image requires a tool_call_id"}, nil
	}

	integration, err := t.selectIntegration(p.IntegrationID, p.IntegrationName)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	apiKey := strings.TrimSpace(integration.Config["api_key"])
	if apiKey == "" {
		return &tools.Result{Success: false, Error: "selected leonardo integration is missing api_key"}, nil
	}

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
	if height > 0 {
		requestBody["height"] = height
	}
	numImages := p.NumImages
	if numImages <= 0 {
		numImages = parsePositiveInt(integration.Config["num_images"])
	}
	if numImages <= 0 {
		numImages = defaultLeonardoNumImages
	}
	requestBody["num_images"] = numImages

	body, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to encode leonardo request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, leonardoAPIBaseURL+"/generations", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("failed to build leonardo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("leonardo request failed: %v", err)}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("failed to read leonardo response: %v", err)}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(respBody))
		if detail == "" {
			detail = resp.Status
		}
		return &tools.Result{Success: false, Error: fmt.Sprintf("leonardo API error (status %d): %s", resp.StatusCode, detail)}, nil
	}

	generationID := extractLeonardoGenerationID(respBody)
	if generationID == "" {
		return &tools.Result{Success: false, Error: "leonardo response did not include a generation id"}, nil
	}

	now := time.Now()
	record := &storage.LeonardoGeneration{
		ID:            uuid.NewString(),
		SessionID:     sessionID,
		ToolCallID:    toolCallID,
		IntegrationID: integration.ID,
		GenerationID:  generationID,
		Status:        leonardoGenerationStatusWait,
		Prompt:        prompt,
		RequestJSON:   string(body),
		ResponseJSON:  strings.TrimSpace(string(respBody)),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := t.store.SaveLeonardoGeneration(record); err != nil {
		return nil, fmt.Errorf("failed to persist leonardo generation: %w", err)
	}

	sess, err := t.sessionManager.Get(sessionID)
	if err == nil && sess != nil {
		sess.SetStatus(session.StatusWaitingExternal)
		if err := t.sessionManager.Save(sess); err != nil {
			return nil, fmt.Errorf("failed to update session status: %w", err)
		}
	}

	return &tools.Result{
		Success: true,
		Output:  fmt.Sprintf("Leonardo generation queued.\nGeneration ID: %s\nThe session is waiting for the Leonardo webhook callback.", generationID),
		Metadata: map[string]interface{}{
			"external_wait": true,
			"leonardo_generation": map[string]interface{}{
				"generation_id":  generationID,
				"integration_id": integration.ID,
				"status":         leonardoGenerationStatusWait,
			},
		},
	}, nil
}

func (p *LeonardoWebhookProcessor) HandleWebhook(ctx context.Context, payload json.RawMessage) (sessionID string, retErr error) {
	effectivePayload, err := unwrapLeonardoWebhookPayload(payload)
	if err != nil {
		return "", err
	}

	generationID := extractLeonardoGenerationID(effectivePayload)
	if generationID == "" {
		return "", fmt.Errorf("leonardo webhook payload did not include a generation id")
	}

	record, claimed, err := p.store.ClaimLeonardoGenerationByGenerationID(
		generationID,
		leonardoGenerationStatusWait,
		leonardoGenerationStatusProcessing,
	)
	if err != nil {
		return "", err
	}
	if !claimed {
		return "", nil
	}
	defer func() {
		if retErr == nil || record == nil || record.Status != leonardoGenerationStatusProcessing {
			return
		}
		record.Status = leonardoGenerationStatusWait
		record.Error = ""
		record.UpdatedAt = time.Now()
		if saveErr := p.store.SaveLeonardoGeneration(record); saveErr != nil {
			retErr = fmt.Errorf("%w (and failed to restore leonardo generation to pending: %v)", retErr, saveErr)
		}
	}()

	sess, err := p.sessionManager.Get(record.SessionID)
	if err != nil {
		return "", fmt.Errorf("failed to load target session: %w", err)
	}

	status := extractLeonardoStatus(effectivePayload)
	imageURLs := extractLeonardoImageURLs(effectivePayload)
	localPaths := make([]string, 0, len(imageURLs))
	processingErr := ""
	if isLeonardoFailureStatus(status) {
		processingErr = extractLeonardoError(effectivePayload)
		if processingErr == "" {
			processingErr = "Leonardo generation failed"
		}
		record.Status = leonardoGenerationStatusFailed
		record.Error = processingErr
	} else {
		if len(imageURLs) == 0 {
			processingErr = "Leonardo webhook did not include any generated image URLs"
			record.Status = leonardoGenerationStatusFailed
			record.Error = processingErr
		}
		for idx, rawURL := range imageURLs {
			path, downloadErr := p.downloadImage(ctx, sess.ID, generationID, idx, rawURL)
			if downloadErr != nil {
				processingErr = downloadErr.Error()
				record.Status = leonardoGenerationStatusFailed
				record.Error = processingErr
				break
			}
			localPaths = append(localPaths, path)
		}
		if processingErr == "" {
			record.Status = leonardoGenerationStatusDone
		}
	}

	record.ResponseJSON = strings.TrimSpace(string(effectivePayload))
	record.UpdatedAt = time.Now()
	if err := p.store.SaveLeonardoGeneration(record); err != nil {
		return "", fmt.Errorf("failed to update leonardo generation status: %w", err)
	}

	toolResult := session.ToolResult{
		ToolCallID: record.ToolCallID,
		Name:       "leonardo_generate_image",
	}
	if record.Status == leonardoGenerationStatusFailed {
		toolResult.IsError = true
		toolResult.Content = fmt.Sprintf("Leonardo generation %s failed: %s", generationID, record.Error)
	} else {
		toolResult.Content = buildLeonardoToolResultContent(generationID, localPaths, imageURLs)
		toolResult.Metadata = buildLeonardoToolResultMetadata(localPaths, imageURLs)
	}

	sess.AddToolResult([]session.ToolResult{toolResult})
	sess.SetStatus(session.StatusRunning)
	if err := p.sessionManager.Save(sess); err != nil {
		return "", fmt.Errorf("failed to save leonardo tool result to session: %w", err)
	}

	return sess.ID, nil
}

func unwrapLeonardoWebhookPayload(payload json.RawMessage) (json.RawMessage, error) {
	var envelope struct {
		Metadata map[string]interface{} `json:"metadata"`
		HTTP     struct {
			BodyBase64 string `json:"body_base64"`
		} `json:"http"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return payload, nil
	}
	if envelope.Metadata == nil {
		return payload, nil
	}
	eventType, _ := envelope.Metadata["internal_event"].(string)
	if strings.TrimSpace(eventType) != "webhook_inbound" {
		return payload, nil
	}
	encoded := strings.TrimSpace(envelope.HTTP.BodyBase64)
	if encoded == "" {
		return nil, fmt.Errorf("webhook relay payload did not include a body")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("failed to decode webhook relay body: %w", err)
	}
	return json.RawMessage(raw), nil
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

func (p *LeonardoWebhookProcessor) downloadImage(ctx context.Context, sessionID, generationID string, index int, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to build image download request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download generated image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("failed to download generated image: %s", resp.Status)
	}

	if err := os.MkdirAll(filepath.Join(p.outputDir, sessionID), 0o755); err != nil {
		return "", fmt.Errorf("failed to create leonardo output folder: %w", err)
	}

	ext := imageFileExtension(rawURL, resp.Header.Get("Content-Type"))
	path := filepath.Join(p.outputDir, sessionID, fmt.Sprintf("%s-%d%s", generationID, index+1, ext))
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
	return strings.ToLower(strings.TrimSpace(findStringByKey(payload, "status")))
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

func isLeonardoFailureStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error":
		return true
	default:
		return false
	}
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
