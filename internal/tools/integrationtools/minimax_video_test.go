package integrationtools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newMiniMaxTestServer(t *testing.T, createBody *map[string]interface{}, failTask bool) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	polls := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") && r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/video_generation":
			_ = json.NewDecoder(r.Body).Decode(createBody)
			_, _ = io.WriteString(w, `{"task_id":"task-1","base_resp":{"status_code":0,"status_msg":"success"}}`)
		case r.URL.Path == "/v1/query/video_generation":
			if r.URL.Query().Get("task_id") != "task-1" {
				http.NotFound(w, r)
				return
			}
			polls++
			if polls < 2 {
				_, _ = io.WriteString(w, `{"task_id":"task-1","status":"Processing","base_resp":{"status_code":0}}`)
				return
			}
			if failTask {
				_, _ = io.WriteString(w, `{"task_id":"task-1","status":"Fail","base_resp":{"status_code":0,"status_msg":"content blocked"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"task_id":"task-1","status":"Success","file_id":"file-9","base_resp":{"status_code":0}}`)
		case r.URL.Path == "/v1/files/retrieve":
			if r.URL.Query().Get("file_id") != "file-9" {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, `{"file":{"download_url":"`+srv.URL+`/dl/video.mp4?sig=abc"},"base_resp":{"status_code":0}}`)
		case r.URL.Path == "/dl/video.mp4":
			_, _ = io.WriteString(w, "mp4-bytes")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMiniMaxGenerateVideoImageToVideo(t *testing.T) {
	t.Parallel()
	var createBody map[string]interface{}
	srv := newMiniMaxTestServer(t, &createBody, false)

	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "frame.png"), testPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(workDir, "generated", "minimax")
	tool := NewMiniMaxGenerateVideoTool(newModel3DTestStore(t, "minimax", srv.URL), workDir, outDir)
	tool.pollInterval = time.Millisecond

	params, _ := json.Marshal(map[string]interface{}{"prompt": "a cat walks [Push in]", "image": "frame.png", "model": "MiniMax-Hailuo-2.3", "duration": 6, "resolution": "1080p"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got %q", result.Error)
	}
	if createBody["model"] != "MiniMax-Hailuo-2.3" {
		t.Fatalf("expected Hailuo model, got %v", createBody["model"])
	}
	if createBody["resolution"] != "1080P" {
		t.Fatalf("expected resolution to be upper-cased, got %v", createBody["resolution"])
	}
	frame, _ := createBody["first_frame_image"].(string)
	if !strings.HasPrefix(frame, "data:image/png;base64,") {
		t.Fatalf("expected local image inlined as data URI, got %q", frame)
	}
	videoPath := filepath.Join(outDir, "task-1.mp4")
	if data, err := os.ReadFile(videoPath); err != nil || string(data) != "mp4-bytes" {
		t.Fatalf("video not saved correctly: %v", err)
	}
	artifacts, _ := result.Metadata["artifacts"].([]map[string]interface{})
	if len(artifacts) != 1 || artifacts[0]["kind"] != "video" || artifacts[0]["path"] != videoPath {
		t.Fatalf("expected video artifact metadata, got %v", result.Metadata)
	}
	videoFile, _ := result.Metadata["video_file"].(map[string]interface{})
	if videoFile["path"] != videoPath {
		t.Fatalf("expected video_file metadata, got %v", result.Metadata)
	}
}

func TestMiniMaxGenerateVideoResumeTaskID(t *testing.T) {
	t.Parallel()
	var createBody map[string]interface{}
	srv := newMiniMaxTestServer(t, &createBody, false)
	workDir := t.TempDir()
	tool := NewMiniMaxGenerateVideoTool(newModel3DTestStore(t, "minimax", srv.URL), workDir, filepath.Join(workDir, "out"))
	tool.pollInterval = time.Millisecond

	params, _ := json.Marshal(map[string]interface{}{"task_id": "task-1", "model": "MiniMax-Hailuo-2.3"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil || !result.Success {
		t.Fatalf("expected success, got err=%v result=%+v", err, result)
	}
	if createBody != nil {
		t.Fatalf("resume must not create a new task, got %v", createBody)
	}
}

func TestMiniMaxGenerateVideoFailedTask(t *testing.T) {
	t.Parallel()
	var createBody map[string]interface{}
	srv := newMiniMaxTestServer(t, &createBody, true)
	workDir := t.TempDir()
	tool := NewMiniMaxGenerateVideoTool(newModel3DTestStore(t, "minimax", srv.URL), workDir, filepath.Join(workDir, "out"))
	tool.pollInterval = time.Millisecond

	params, _ := json.Marshal(map[string]interface{}{"prompt": "a dog", "model": "MiniMax-Hailuo-2.3"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "content blocked") || !strings.Contains(result.Error, "task-1") {
		t.Fatalf("expected failure with reason and task id, got %+v", result)
	}
}

func TestMiniMaxGenerateVideoH3(t *testing.T) {
	t.Parallel()
	var createBody map[string]interface{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/video_generation":
			_ = json.NewDecoder(r.Body).Decode(&createBody)
			_, _ = io.WriteString(w, `{"task_id":"h3-1"}`)
		case r.URL.Path == "/v2/query/video_generation/h3-1":
			_, _ = io.WriteString(w, `{"task":{"id":"h3-1","status":"succeeded","content":{"url":"`+srv.URL+`/dl/h3.mp4"}}}`)
		case r.URL.Path == "/dl/h3.mp4":
			_, _ = io.WriteString(w, "h3-bytes")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	workDir := t.TempDir()
	outDir := filepath.Join(workDir, "out")
	tool := NewMiniMaxGenerateVideoTool(newModel3DTestStore(t, "minimax", srv.URL), workDir, outDir)
	tool.pollInterval = time.Millisecond

	params, _ := json.Marshal(map[string]interface{}{"prompt": "a fox runs", "resolution": "2k"})
	result, err := tool.Execute(context.Background(), params)
	if err != nil || !result.Success {
		t.Fatalf("expected success, got err=%v result=%+v", err, result)
	}
	if createBody["model"] != minimaxDefaultModel || createBody["resolution"] != "2K" || createBody["duration"] != float64(6) {
		t.Fatalf("unexpected H3 create body: %v", createBody)
	}
	content, _ := createBody["content"].([]interface{})
	if len(content) != 1 || content[0].(map[string]interface{})["type"] != "text" {
		t.Fatalf("expected single text content item, got %v", content)
	}
	if data, err := os.ReadFile(filepath.Join(outDir, "h3-1.mp4")); err != nil || string(data) != "h3-bytes" {
		t.Fatalf("H3 video not saved correctly: %v", err)
	}
}

func TestMiniMaxGenerateVideoRequiresInput(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()
	tool := NewMiniMaxGenerateVideoTool(newModel3DTestStore(t, "minimax", "http://127.0.0.1:1"), workDir, workDir)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil || result.Success {
		t.Fatalf("expected validation failure, got err=%v result=%+v", err, result)
	}
}
