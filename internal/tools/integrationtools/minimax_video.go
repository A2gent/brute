package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

const (
	minimaxDefaultBaseURL = "https://api.minimax.io"
	minimaxDefaultModel   = "MiniMax-H3"
	minimaxH3Prefix       = "MiniMax-H3"
)

// MiniMaxGenerateVideoTool runs MiniMax video tasks (MiniMax H3 or legacy Hailuo),
// waits for completion and saves the MP4 locally.
type MiniMaxGenerateVideoTool struct {
	store        storage.Store
	workDir      string
	outputDir    string
	client       *http.Client
	pollInterval time.Duration
}

type minimaxGenerateVideoParams struct {
	Prompt          string `json:"prompt"`
	Image           string `json:"image"`
	Model           string `json:"model"`
	Duration        int    `json:"duration"`
	Resolution      string `json:"resolution"`
	PromptOptimizer *bool  `json:"prompt_optimizer"`
	TaskID          string `json:"task_id"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
}

type minimaxBaseResp struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

type minimaxCreateResponse struct {
	TaskID   string          `json:"task_id"`
	BaseResp minimaxBaseResp `json:"base_resp"`
}

type minimaxTask struct {
	TaskID      string          `json:"task_id"`
	Status      string          `json:"status"`
	FileID      string          `json:"file_id"`
	BaseResp    minimaxBaseResp `json:"base_resp"`
	DownloadURL string          `json:"-"`
}

type minimaxH3TaskResponse struct {
	Task struct {
		Status  string `json:"status"`
		Content struct {
			URL string `json:"url"`
		} `json:"content"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"task"`
}

type minimaxFileResponse struct {
	File struct {
		DownloadURL string `json:"download_url"`
	} `json:"file"`
	BaseResp minimaxBaseResp `json:"base_resp"`
}

func NewMiniMaxGenerateVideoTool(store storage.Store, workDir, outputDir string) *MiniMaxGenerateVideoTool {
	return &MiniMaxGenerateVideoTool{
		store:        store,
		workDir:      strings.TrimSpace(workDir),
		outputDir:    strings.TrimSpace(outputDir),
		client:       &http.Client{Timeout: 120 * time.Second},
		pollInterval: model3DDefaultPollInterval,
	}
}

func (t *MiniMaxGenerateVideoTool) Name() string { return "minimax_generate_video" }

func (t *MiniMaxGenerateVideoTool) Description() string {
	return "Generate a video with MiniMax (H3 or Hailuo) from a text prompt (text-to-video) or from a first-frame image (image-to-video). " +
		"The image can be a local file path (for example one produced by leonardo_generate_image) or a public URL. " +
		"Camera moves can be written into the prompt as [Pan left], [Push in], [Static shot] and so on. " +
		"Waits for the task to finish and returns the local path of the downloaded MP4 file, which Caesar previews inline. " +
		"If the wait times out, call again with the returned task_id and the same model instead of starting a new task."
}

func (t *MiniMaxGenerateVideoTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Text description of the video (max 2000 chars). Required unless image or task_id is set; always required for MiniMax-H3.",
			},
			"image": map[string]interface{}{
				"type":        "string",
				"description": "First frame for image-to-video: local path (relative to the workspace or absolute) or public http(s) URL. PNG, JPEG or WebP up to 20 MB.",
			},
			"model": map[string]interface{}{
				"type":        "string",
				"description": "MiniMax model: MiniMax-H3 (default), MiniMax-H3-Highspeed, MiniMax-H3-Lite, MiniMax-H3-Max, or legacy MiniMax-Hailuo-2.3, MiniMax-Hailuo-2.3-Fast (image-to-video only), MiniMax-Hailuo-02.",
			},
			"duration": map[string]interface{}{
				"type":        "integer",
				"description": "Video length in seconds: 4-15 for MiniMax-H3; 6 or 10 for Hailuo (10 only at 768P).",
			},
			"resolution": map[string]interface{}{
				"type":        "string",
				"description": "MiniMax-H3: 768P (default) or 2K (base H3 only). Hailuo: 768P (default) or 1080P (6 seconds only).",
			},
			"prompt_optimizer": map[string]interface{}{
				"type":        "boolean",
				"description": "Let MiniMax rewrite the prompt (default true). Set false for exact control. Hailuo only.",
			},
			"task_id": map[string]interface{}{
				"type":        "string",
				"description": "Resume waiting for a task returned by a previous call instead of creating a new one.",
			},
			"timeout_seconds": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum time to wait for the video (default from integration or 600).",
			},
		},
	}
}

func (t *MiniMaxGenerateVideoTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p minimaxGenerateVideoParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	p.Prompt = strings.TrimSpace(p.Prompt)
	p.Image = strings.TrimSpace(p.Image)
	p.TaskID = strings.TrimSpace(p.TaskID)
	if p.Prompt == "" && p.Image == "" && p.TaskID == "" {
		return &tools.Result{Success: false, Error: "either prompt, image or task_id is required"}, nil
	}
	integration, err := findModel3DIntegration(ctx, t.store, "minimax")
	if err != nil {
		return &tools.Result{Success: false, Error: err.Error()}, nil
	}
	baseURL := integration.BaseURL
	if baseURL == "" {
		baseURL = minimaxDefaultBaseURL
	}
	timeout := integration.Timeout
	if p.TimeoutSeconds > 0 {
		timeout = time.Duration(p.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	model := firstNonEmpty(strings.TrimSpace(p.Model), integration.Model, minimaxDefaultModel)
	taskID := p.TaskID
	if taskID == "" {
		body, err := t.buildCreateBody(p, model)
		if err != nil {
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
		taskID, err = t.createTask(ctx, baseURL, integration.APIKey, model, body)
		if err != nil {
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
	}
	task, err := t.waitTask(ctx, baseURL, integration.APIKey, model, taskID)
	if err != nil {
		return &tools.Result{Success: false, Error: fmt.Sprintf("%s (task_id: %s)", err.Error(), taskID)}, nil
	}
	return t.saveResult(ctx, baseURL, integration.APIKey, task)
}

func isMiniMaxH3(model string) bool {
	return strings.HasPrefix(model, minimaxH3Prefix)
}

func (t *MiniMaxGenerateVideoTool) buildCreateBody(p minimaxGenerateVideoParams, model string) (map[string]interface{}, error) {
	if isMiniMaxH3(model) {
		return t.buildH3Body(p, model)
	}
	body := map[string]interface{}{
		"model": model,
	}
	if p.Prompt != "" {
		body["prompt"] = p.Prompt
	}
	if p.Duration > 0 {
		body["duration"] = p.Duration
	}
	if res := strings.TrimSpace(p.Resolution); res != "" {
		body["resolution"] = strings.ToUpper(res)
	}
	if p.PromptOptimizer != nil {
		body["prompt_optimizer"] = *p.PromptOptimizer
	}
	if p.Image != "" {
		img, err := resolveModel3DImage(p.Image, t.workDir)
		if err != nil {
			return nil, err
		}
		if img.URL != "" {
			body["first_frame_image"] = img.URL
		} else {
			body["first_frame_image"] = img.dataURI()
		}
	}
	return body, nil
}

func (t *MiniMaxGenerateVideoTool) buildH3Body(p minimaxGenerateVideoParams, model string) (map[string]interface{}, error) {
	if p.Prompt == "" {
		return nil, fmt.Errorf("prompt is required for %s", model)
	}
	content := []map[string]interface{}{{"type": "text", "text": p.Prompt}}
	if p.Image != "" {
		img, err := resolveModel3DImage(p.Image, t.workDir)
		if err != nil {
			return nil, err
		}
		imageURL := img.URL
		if imageURL == "" {
			imageURL = img.dataURI()
		}
		content = append(content, map[string]interface{}{
			"type":      "image_url",
			"image_url": map[string]interface{}{"url": imageURL},
			"role":      "first_frame",
		})
	}
	body := map[string]interface{}{
		"model":      model,
		"content":    content,
		"resolution": "768P",
		"duration":   6,
	}
	if res := strings.TrimSpace(p.Resolution); res != "" {
		body["resolution"] = strings.ToUpper(res)
	}
	if p.Duration > 0 {
		body["duration"] = p.Duration
	}
	return body, nil
}

func (t *MiniMaxGenerateVideoTool) createTask(ctx context.Context, baseURL, apiKey, model string, body map[string]interface{}) (string, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("failed to encode request: %w", err)
	}
	endpoint := baseURL + "/v1/video_generation"
	if isMiniMaxH3(model) {
		endpoint = baseURL + "/v2/video_generation"
	}
	data, err := t.do(ctx, http.MethodPost, endpoint, apiKey, payload)
	if err != nil {
		return "", err
	}
	var created minimaxCreateResponse
	if err := json.Unmarshal(data, &created); err != nil || created.TaskID == "" {
		return "", fmt.Errorf("unexpected MiniMax create response: %s", truncateForError(data))
	}
	return created.TaskID, nil
}

func (t *MiniMaxGenerateVideoTool) waitTask(ctx context.Context, baseURL, apiKey, model, taskID string) (*minimaxTask, error) {
	if isMiniMaxH3(model) {
		return t.waitH3Task(ctx, baseURL, apiKey, taskID)
	}
	endpoint := baseURL + "/v1/query/video_generation?task_id=" + url.QueryEscape(taskID)
	var task minimaxTask
	err := pollModel3DTask(ctx, t.pollInterval, func(ctx context.Context) (bool, error) {
		data, err := t.do(ctx, http.MethodGet, endpoint, apiKey, nil)
		if err != nil {
			return false, err
		}
		if err := json.Unmarshal(data, &task); err != nil {
			return false, fmt.Errorf("failed to parse MiniMax task: %w", err)
		}
		switch strings.ToLower(task.Status) {
		case "success":
			return true, nil
		case "fail", "failed":
			msg := strings.TrimSpace(task.BaseResp.StatusMsg)
			if msg == "" {
				msg = fmt.Sprintf("status code %d", task.BaseResp.StatusCode)
			}
			return false, fmt.Errorf("MiniMax video task %s failed: %s", taskID, msg)
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	if task.TaskID == "" {
		task.TaskID = taskID
	}
	return &task, nil
}

func (t *MiniMaxGenerateVideoTool) waitH3Task(ctx context.Context, baseURL, apiKey, taskID string) (*minimaxTask, error) {
	endpoint := baseURL + "/v2/query/video_generation/" + url.PathEscape(taskID)
	var resp minimaxH3TaskResponse
	err := pollModel3DTask(ctx, t.pollInterval, func(ctx context.Context) (bool, error) {
		data, err := t.do(ctx, http.MethodGet, endpoint, apiKey, nil)
		if err != nil {
			return false, err
		}
		resp = minimaxH3TaskResponse{}
		if err := json.Unmarshal(data, &resp); err != nil {
			return false, fmt.Errorf("failed to parse MiniMax task: %w", err)
		}
		switch strings.ToLower(resp.Task.Status) {
		case "succeeded":
			if resp.Task.Content.URL == "" {
				return false, fmt.Errorf("MiniMax video task %s succeeded without a download URL", taskID)
			}
			return true, nil
		case "failed", "cancelled":
			msg := strings.TrimSpace(resp.Task.Error.Message)
			if msg == "" {
				msg = resp.Task.Status
			}
			return false, fmt.Errorf("MiniMax video task %s %s: %s", taskID, resp.Task.Status, msg)
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return &minimaxTask{TaskID: taskID, DownloadURL: resp.Task.Content.URL}, nil
}

// do performs a MiniMax request. Errors are reported in base_resp even on HTTP 200.
func (t *MiniMaxGenerateVideoTool) do(ctx context.Context, method, endpoint, apiKey string, payload []byte) ([]byte, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to build MiniMax request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("MiniMax request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read MiniMax response: %w", err)
	}
	var env struct {
		BaseResp minimaxBaseResp `json:"base_resp"`
	}
	_ = json.Unmarshal(body, &env)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || env.BaseResp.StatusCode != 0 {
		detail := strings.TrimSpace(env.BaseResp.StatusMsg)
		if detail == "" {
			detail = truncateForError(body)
		}
		return nil, fmt.Errorf("MiniMax API error (status %d, code %d): %s", resp.StatusCode, env.BaseResp.StatusCode, detail)
	}
	return body, nil
}

func (t *MiniMaxGenerateVideoTool) saveResult(ctx context.Context, baseURL, apiKey string, task *minimaxTask) (*tools.Result, error) {
	downloadURL := task.DownloadURL
	if downloadURL == "" {
		if strings.TrimSpace(task.FileID) == "" {
			return &tools.Result{Success: false, Error: fmt.Sprintf("MiniMax task %s returned no file_id", task.TaskID)}, nil
		}
		data, err := t.do(ctx, http.MethodGet, baseURL+"/v1/files/retrieve?file_id="+url.QueryEscape(task.FileID), apiKey, nil)
		if err != nil {
			return &tools.Result{Success: false, Error: err.Error()}, nil
		}
		var file minimaxFileResponse
		if err := json.Unmarshal(data, &file); err != nil || strings.TrimSpace(file.File.DownloadURL) == "" {
			return &tools.Result{Success: false, Error: "MiniMax returned no download URL: " + truncateForError(data)}, nil
		}
		downloadURL = file.File.DownloadURL
	}
	outDir := t.outputDir
	if outDir == "" {
		outDir = filepath.Join(t.workDir, "generated", "minimax")
	}
	videoPath, err := downloadModel3DFile(ctx, t.client, downloadURL, outDir, task.TaskID, model3DURLExt(downloadURL, ".mp4"))
	if err != nil {
		return &tools.Result{Success: false, Error: "failed to download video: " + err.Error()}, nil
	}
	return &tools.Result{
		Success:  true,
		Output:   fmt.Sprintf("MiniMax video generation %s completed.\nVideo file: %s", task.TaskID, videoPath),
		Metadata: minimaxVideoResultMetadata(t.Name(), task.TaskID, videoPath),
	}, nil
}

func minimaxVideoResultMetadata(sourceTool, taskID, videoPath string) map[string]interface{} {
	filename := filepath.Base(videoPath)
	return map[string]interface{}{
		"video_file": map[string]interface{}{
			"provider":    "minimax",
			"task_id":     taskID,
			"path":        videoPath,
			"source_tool": sourceTool,
		},
		"artifacts": []map[string]interface{}{{
			"kind":      "video",
			"path":      videoPath,
			"filename":  filename,
			"mime_type": "video/mp4",
		}},
	}
}
