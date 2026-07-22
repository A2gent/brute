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
	"sort"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/google/uuid"
)

const (
	comfyUIMaxWorkflowBytes = 8 * 1024 * 1024
	comfyUIMaxArtifactBytes = int64(2 * 1024 * 1024 * 1024)
)

// ComfyUIRunWorkflowTool executes API-format workflows so ComfyUI can produce
// any asset type supported by the user's installed nodes, not just images.
type ComfyUIRunWorkflowTool struct {
	runner  *ComfyUIGenerateImageTool
	workDir string
}

type comfyUIRunWorkflowParams struct {
	Workflow        json.RawMessage                   `json:"workflow,omitempty"`
	WorkflowPath    string                            `json:"workflow_path,omitempty"`
	InputOverrides  map[string]map[string]interface{} `json:"input_overrides,omitempty"`
	IntegrationID   string                            `json:"integration_id,omitempty"`
	IntegrationName string                            `json:"integration_name,omitempty"`
}

type comfyUIHistoryArtifact struct {
	Filename  string
	Subfolder string
	Type      string
	NodeID    string
	OutputKey string
	Kind      string
}

type comfyUILocalArtifact struct {
	Path      string
	Filename  string
	Kind      string
	MIMEType  string
	NodeID    string
	OutputKey string
}

type comfyUIWorkflowHistoryEntry struct {
	Outputs map[string]json.RawMessage `json:"outputs"`
	Status  struct {
		Completed bool            `json:"completed"`
		StatusStr string          `json:"status_str"`
		Messages  [][]interface{} `json:"messages"`
	} `json:"status"`
}

func NewComfyUIRunWorkflowTool(store storage.Store, workDir, outputDir string) *ComfyUIRunWorkflowTool {
	return &ComfyUIRunWorkflowTool{
		runner:  NewComfyUIGenerateImageTool(store, outputDir),
		workDir: strings.TrimSpace(workDir),
	}
}

func (t *ComfyUIRunWorkflowTool) Name() string {
	return "comfyui_run_workflow"
}

func (t *ComfyUIRunWorkflowTool) Description() string {
	return "Run any local ComfyUI API-format workflow and download its image, audio, video, 3D, text, or other file outputs. Provide workflow JSON inline or by file path; optional node input overrides make exported workflows reusable."
}

func (t *ComfyUIRunWorkflowTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"workflow": map[string]interface{}{
				"type":        "object",
				"description": "ComfyUI workflow exported with Save (API Format). May be the prompt graph directly or an object containing a prompt graph.",
			},
			"workflow_path": map[string]interface{}{
				"type":        "string",
				"description": "Path to a ComfyUI API-format workflow JSON file. Relative paths resolve from the current project/workspace. Used when workflow is omitted.",
			},
			"input_overrides": map[string]interface{}{
				"type":        "object",
				"description": "Optional node input overrides keyed by node ID, for example {\"6\": {\"text\": \"new prompt\"}}. Values merge into existing node inputs.",
				"additionalProperties": map[string]interface{}{
					"type": "object",
				},
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
	}
}

func (t *ComfyUIRunWorkflowTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p comfyUIRunWorkflowParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if t.runner == nil || t.runner.store == nil {
		return &tools.Result{Success: false, Error: "comfyui integration store is unavailable"}, nil
	}

	integration, err := t.runner.selectIntegration(p.IntegrationID, p.IntegrationName)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	workflowPath := firstNonEmpty(p.WorkflowPath, integration.Config["workflow_path"])
	workflow, err := t.loadWorkflow(p.Workflow, workflowPath)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	if err := applyComfyUIInputOverrides(workflow, p.InputOverrides); err != nil {
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

	promptID, err := t.runner.queuePrompt(ctx, baseURL, integration.Config["api_key"], uuid.NewString(), workflow)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	artifacts, err := t.waitForArtifacts(ctx, baseURL, integration.Config["api_key"], promptID)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	localArtifacts, err := t.downloadArtifacts(ctx, baseURL, integration.Config["api_key"], promptID, artifacts)
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}

	return &tools.Result{
		Success:  true,
		Output:   buildComfyUIWorkflowResultContent(promptID, localArtifacts),
		Metadata: buildComfyUIWorkflowResultMetadata(promptID, localArtifacts),
	}, nil
}

func (t *ComfyUIRunWorkflowTool) loadWorkflow(inline json.RawMessage, workflowPath string) (map[string]interface{}, error) {
	raw := inline
	if len(raw) == 0 || string(raw) == "null" {
		path := strings.TrimSpace(workflowPath)
		if path == "" {
			return nil, fmt.Errorf("either workflow or workflow_path is required (export with ComfyUI Save (API Format))")
		}
		if !filepath.IsAbs(path) {
			base := t.workDir
			if base == "" {
				base = "."
			}
			path = filepath.Join(base, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read ComfyUI workflow %q: %w", workflowPath, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("ComfyUI workflow path points to a directory: %s", workflowPath)
		}
		if info.Size() > comfyUIMaxWorkflowBytes {
			return nil, fmt.Errorf("ComfyUI workflow exceeds %d bytes", comfyUIMaxWorkflowBytes)
		}
		raw, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read ComfyUI workflow %q: %w", workflowPath, err)
		}
	}
	if len(raw) > comfyUIMaxWorkflowBytes {
		return nil, fmt.Errorf("ComfyUI workflow exceeds %d bytes", comfyUIMaxWorkflowBytes)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("failed to parse ComfyUI workflow JSON: %w", err)
	}
	if prompt, ok := decoded["prompt"].(map[string]interface{}); ok {
		decoded = prompt
	}
	if _, hasNodes := decoded["nodes"]; hasNodes {
		return nil, fmt.Errorf("ComfyUI workflow must use API format, not UI format; export it with Save (API Format)")
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("ComfyUI API workflow is empty")
	}
	for nodeID, rawNode := range decoded {
		node, ok := rawNode.(map[string]interface{})
		if !ok || strings.TrimSpace(asComfyUIString(node["class_type"])) == "" {
			return nil, fmt.Errorf("ComfyUI workflow node %q is missing class_type; use Save (API Format)", nodeID)
		}
		if inputs, exists := node["inputs"]; exists {
			if _, ok := inputs.(map[string]interface{}); !ok {
				return nil, fmt.Errorf("ComfyUI workflow node %q has invalid inputs", nodeID)
			}
		} else {
			node["inputs"] = map[string]interface{}{}
		}
	}
	return decoded, nil
}

func applyComfyUIInputOverrides(workflow map[string]interface{}, overrides map[string]map[string]interface{}) error {
	for nodeID, overrideInputs := range overrides {
		nodeRaw, ok := workflow[nodeID]
		if !ok {
			return fmt.Errorf("ComfyUI input override references unknown node %q", nodeID)
		}
		node, ok := nodeRaw.(map[string]interface{})
		if !ok {
			return fmt.Errorf("ComfyUI workflow node %q is invalid", nodeID)
		}
		inputs, ok := node["inputs"].(map[string]interface{})
		if !ok {
			inputs = map[string]interface{}{}
			node["inputs"] = inputs
		}
		for name, value := range overrideInputs {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("ComfyUI input override for node %q has an empty input name", nodeID)
			}
			inputs[name] = value
		}
	}
	return nil
}

func (t *ComfyUIRunWorkflowTool) waitForArtifacts(ctx context.Context, baseURL, apiKey, promptID string) ([]comfyUIHistoryArtifact, error) {
	interval := t.runner.pollInterval
	if interval <= 0 {
		interval = comfyUIDefaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		artifacts, done, err := t.fetchHistoryArtifacts(ctx, baseURL, apiKey, promptID)
		if err != nil {
			return nil, err
		}
		if done {
			return artifacts, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out waiting for ComfyUI workflow: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (t *ComfyUIRunWorkflowTool) fetchHistoryArtifacts(ctx context.Context, baseURL, apiKey, promptID string) ([]comfyUIHistoryArtifact, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/history/"+url.PathEscape(promptID), nil)
	if err != nil {
		return nil, false, fmt.Errorf("failed to build ComfyUI history request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	applyComfyUIAuth(req, apiKey)
	resp, err := t.runner.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("ComfyUI history request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if err != nil {
		return nil, false, fmt.Errorf("failed to read ComfyUI history response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = resp.Status
		}
		return nil, false, fmt.Errorf("ComfyUI history error (status %d): %s", resp.StatusCode, detail)
	}

	var history map[string]json.RawMessage
	if err := json.Unmarshal(body, &history); err != nil {
		return nil, false, fmt.Errorf("failed to parse ComfyUI history: %w", err)
	}
	entryRaw, ok := history[promptID]
	if !ok || len(entryRaw) == 0 {
		return nil, false, nil
	}
	var entry comfyUIWorkflowHistoryEntry
	if err := json.Unmarshal(entryRaw, &entry); err != nil {
		return nil, false, fmt.Errorf("failed to parse ComfyUI history entry: %w", err)
	}
	status := strings.ToLower(strings.TrimSpace(entry.Status.StatusStr))
	if status == "error" || status == "failed" {
		return nil, true, comfyUIExecutionError(entry.Status.StatusStr, entry.Status.Messages)
	}
	if !entry.Status.Completed && status != "success" {
		return nil, false, nil
	}
	return collectComfyUIHistoryArtifacts(entry.Outputs), true, nil
}

func comfyUIExecutionError(status string, messages [][]interface{}) error {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if len(message) < 2 || asComfyUIString(message[0]) != "execution_error" {
			continue
		}
		detail, _ := message[1].(map[string]interface{})
		nodeID := asComfyUIString(detail["node_id"])
		nodeType := asComfyUIString(detail["node_type"])
		exception := firstNonEmpty(asComfyUIString(detail["exception_message"]), asComfyUIString(detail["exception_type"]))
		location := strings.TrimSpace(strings.Join([]string{nodeID, nodeType}, " "))
		if location != "" && exception != "" {
			return fmt.Errorf("ComfyUI workflow failed at node %s: %s", location, exception)
		}
		if exception != "" {
			return fmt.Errorf("ComfyUI workflow failed: %s", exception)
		}
	}
	return fmt.Errorf("ComfyUI workflow failed with status %q", status)
}

func collectComfyUIHistoryArtifacts(outputs map[string]json.RawMessage) []comfyUIHistoryArtifact {
	nodeIDs := make([]string, 0, len(outputs))
	for nodeID := range outputs {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)

	artifacts := make([]comfyUIHistoryArtifact, 0, len(outputs))
	seen := map[string]struct{}{}
	for _, nodeID := range nodeIDs {
		var output map[string]interface{}
		if err := json.Unmarshal(outputs[nodeID], &output); err != nil {
			continue
		}
		keys := make([]string, 0, len(output))
		for key := range output {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			walkComfyUIOutput(output[key], nodeID, key, &artifacts, seen)
		}
	}
	return artifacts
}

func walkComfyUIOutput(value interface{}, nodeID, outputKey string, artifacts *[]comfyUIHistoryArtifact, seen map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]interface{}:
		filename := strings.TrimSpace(asComfyUIString(typed["filename"]))
		if filename != "" {
			appendComfyUIArtifact(comfyUIHistoryArtifact{
				Filename:  filename,
				Subfolder: strings.TrimSpace(asComfyUIString(typed["subfolder"])),
				Type:      firstNonEmpty(asComfyUIString(typed["type"]), "output"),
				NodeID:    nodeID,
				OutputKey: outputKey,
				Kind:      classifyComfyUIArtifact(filename, outputKey),
			}, artifacts, seen)
			return
		}
		for _, nested := range typed {
			walkComfyUIOutput(nested, nodeID, outputKey, artifacts, seen)
		}
	case []interface{}:
		for _, nested := range typed {
			walkComfyUIOutput(nested, nodeID, outputKey, artifacts, seen)
		}
	case string:
		// Advanced 3D preview nodes expose a relative model path inside result
		// instead of the standard SavedResult object.
		if isComfyUI3DOutputPath(typed, outputKey) {
			clean := filepath.ToSlash(filepath.Clean(typed))
			appendComfyUIArtifact(comfyUIHistoryArtifact{
				Filename:  filepath.Base(clean),
				Subfolder: strings.TrimSuffix(filepath.Dir(clean), "."),
				Type:      "output",
				NodeID:    nodeID,
				OutputKey: outputKey,
				Kind:      "3d",
			}, artifacts, seen)
		}
	}
}

func appendComfyUIArtifact(artifact comfyUIHistoryArtifact, artifacts *[]comfyUIHistoryArtifact, seen map[string]struct{}) {
	key := strings.Join([]string{artifact.Type, artifact.Subfolder, artifact.Filename}, "\x00")
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*artifacts = append(*artifacts, artifact)
}

func isComfyUI3DOutputPath(raw, outputKey string) bool {
	key := strings.ToLower(strings.TrimSpace(outputKey))
	if key != "result" && key != "model_3d" && key != "3d" {
		return false
	}
	path := filepath.ToSlash(strings.TrimSpace(raw))
	if path == "" || strings.Contains(path, "://") || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") {
		return false
	}
	return classifyComfyUIArtifact(path, outputKey) == "3d"
}

func classifyComfyUIArtifact(filename, outputKey string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".tif", ".tiff", ".svg":
		return "image"
	case ".flac", ".mp3", ".wav", ".ogg", ".opus", ".m4a", ".aac":
		return "audio"
	case ".mp4", ".webm", ".mov", ".mkv", ".avi", ".m4v":
		return "video"
	case ".glb", ".gltf", ".obj", ".fbx", ".stl", ".usdz", ".ply", ".splat", ".spz", ".ksplat":
		return "3d"
	case ".txt", ".md", ".json", ".csv", ".xml", ".yaml", ".yml":
		return "text"
	}
	key := strings.ToLower(strings.TrimSpace(outputKey))
	switch {
	case strings.Contains(key, "image") || strings.Contains(key, "mask"):
		return "image"
	case strings.Contains(key, "audio"):
		return "audio"
	case strings.Contains(key, "video") || strings.Contains(key, "animated"):
		return "video"
	case strings.Contains(key, "3d") || strings.Contains(key, "mesh") || strings.Contains(key, "model"):
		return "3d"
	default:
		return "file"
	}
}

func (t *ComfyUIRunWorkflowTool) downloadArtifacts(ctx context.Context, baseURL, apiKey, promptID string, artifacts []comfyUIHistoryArtifact) ([]comfyUILocalArtifact, error) {
	if len(artifacts) == 0 {
		return []comfyUILocalArtifact{}, nil
	}
	outDir := t.runner.outputDir
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "generated", "comfyui")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create ComfyUI output directory: %w", err)
	}

	local := make([]comfyUILocalArtifact, 0, len(artifacts))
	for i, artifact := range artifacts {
		ext := filepath.Ext(artifact.Filename)
		localName := fmt.Sprintf("%s-%d-%s", sanitizeFileToken(promptID), i+1, sanitizeComfyUIArtifactName(artifact.Filename))
		if filepath.Ext(localName) == "" && ext != "" {
			localName += ext
		}
		localPath := filepath.Join(outDir, localName)
		mimeType, err := t.downloadArtifact(ctx, baseURL, apiKey, artifact, localPath)
		if err != nil {
			return nil, err
		}
		local = append(local, comfyUILocalArtifact{
			Path:      localPath,
			Filename:  artifact.Filename,
			Kind:      artifact.Kind,
			MIMEType:  mimeType,
			NodeID:    artifact.NodeID,
			OutputKey: artifact.OutputKey,
		})
	}
	return local, nil
}

func (t *ComfyUIRunWorkflowTool) downloadArtifact(ctx context.Context, baseURL, apiKey string, artifact comfyUIHistoryArtifact, localPath string) (string, error) {
	q := url.Values{}
	q.Set("filename", artifact.Filename)
	q.Set("subfolder", artifact.Subfolder)
	q.Set("type", firstNonEmpty(artifact.Type, "output"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/view?"+q.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to build ComfyUI artifact request: %w", err)
	}
	applyComfyUIAuth(req, apiKey)
	resp, err := t.runner.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ComfyUI artifact download failed for %q: %w", artifact.Filename, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := firstNonEmpty(strings.TrimSpace(string(body)), resp.Status)
		return "", fmt.Errorf("ComfyUI artifact download failed for %q (status %d): %s", artifact.Filename, resp.StatusCode, detail)
	}
	if resp.ContentLength > comfyUIMaxArtifactBytes {
		return "", fmt.Errorf("ComfyUI artifact %q exceeds %d bytes", artifact.Filename, comfyUIMaxArtifactBytes)
	}

	tempPath := localPath + ".part"
	file, err := os.Create(tempPath)
	if err != nil {
		return "", fmt.Errorf("failed to create ComfyUI artifact file: %w", err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(resp.Body, comfyUIMaxArtifactBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("failed to write ComfyUI artifact %q: %w", artifact.Filename, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("failed to close ComfyUI artifact %q: %w", artifact.Filename, closeErr)
	}
	if written > comfyUIMaxArtifactBytes {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("ComfyUI artifact %q exceeds %d bytes", artifact.Filename, comfyUIMaxArtifactBytes)
	}
	if err := os.Rename(tempPath, localPath); err != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("failed to finalize ComfyUI artifact %q: %w", artifact.Filename, err)
	}
	mimeType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = comfyUIArtifactMIMEType(artifact.Filename)
	}
	return mimeType, nil
}

func sanitizeComfyUIArtifactName(raw string) string {
	name := filepath.Base(filepath.ToSlash(strings.TrimSpace(raw)))
	if name == "." || name == "" {
		return "artifact"
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	stem = sanitizeFileToken(stem)
	if stem == "" {
		stem = "artifact"
	}
	if len(ext) > 16 {
		ext = ""
	}
	return stem + strings.ToLower(ext)
}

func comfyUIArtifactMIMEType(filename string) string {
	if value := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); value != "" {
		return strings.Split(value, ";")[0]
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".flac":
		return "audio/flac"
	case ".glb":
		return "model/gltf-binary"
	case ".gltf":
		return "model/gltf+json"
	case ".obj":
		return "model/obj"
	default:
		return "application/octet-stream"
	}
}

func buildComfyUIWorkflowResultContent(promptID string, artifacts []comfyUILocalArtifact) string {
	if len(artifacts) == 0 {
		return fmt.Sprintf("ComfyUI workflow %s completed with no downloadable artifacts. Scalar outputs remain in ComfyUI history.", promptID)
	}
	lines := []string{fmt.Sprintf("ComfyUI workflow %s completed with %d artifact(s):", promptID, len(artifacts))}
	for _, artifact := range artifacts {
		lines = append(lines, fmt.Sprintf("- [%s] %s", artifact.Kind, artifact.Path))
	}
	return strings.Join(lines, "\n")
}

func buildComfyUIWorkflowResultMetadata(promptID string, artifacts []comfyUILocalArtifact) map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(artifacts))
	metadata := map[string]interface{}{
		"comfyui_workflow": map[string]interface{}{
			"prompt_id":      promptID,
			"artifact_count": len(artifacts),
		},
		"artifacts": items,
	}
	for _, artifact := range artifacts {
		item := map[string]interface{}{
			"path":        artifact.Path,
			"filename":    artifact.Filename,
			"kind":        artifact.Kind,
			"mime_type":   artifact.MIMEType,
			"node_id":     artifact.NodeID,
			"output_key":  artifact.OutputKey,
			"source_tool": "comfyui_run_workflow",
		}
		items = append(items, item)
		if artifact.Kind == "image" {
			if _, exists := metadata["image_file"]; !exists {
				metadata["image_file"] = map[string]interface{}{
					"path":        artifact.Path,
					"source_tool": "comfyui_run_workflow",
				}
			}
		}
	}
	metadata["artifacts"] = items
	return metadata
}

func asComfyUIString(value interface{}) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
