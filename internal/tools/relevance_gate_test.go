package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/llm/jev"
)

// These tests deliberately exercise only the public tool contract. They must
// fail to build until NewRelevanceGateTool is implemented.
type relevanceGateVerdict struct {
	Path       string   `json:"path"`
	Action     string   `json:"action"`
	Confidence *float64 `json:"confidence,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Content    string   `json:"content,omitempty"`
	Error      string   `json:"error,omitempty"`
}

type relevanceGateExecutor interface {
	Execute(context.Context, json.RawMessage) (*Result, error)
}

type relevanceGateCapturedRequest struct {
	Raw     string
	Request jev.SystemOneRequest
}

func relevanceGateServer(t *testing.T, status int, body string) (*jev.Client, func() []relevanceGateCapturedRequest) {
	t.Helper()
	var mu sync.Mutex
	var requests []relevanceGateCapturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" {
			t.Errorf("unexpected endpoint: %s %s", r.Method, r.URL.Path)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		var request jev.SystemOneRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		requests = append(requests, relevanceGateCapturedRequest{Raw: string(raw), Request: request})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return jev.NewClient("test-key", "", server.URL+"/v1"), func() []relevanceGateCapturedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]relevanceGateCapturedRequest(nil), requests...)
	}
}

func relevanceGateFile(t *testing.T, dir, path, content string) {
	t.Helper()
	fullPath := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func relevanceGateExecute(t *testing.T, tool relevanceGateExecutor, params any) []relevanceGateVerdict {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), raw)
	if err != nil || result == nil || !result.Success {
		t.Fatalf("Execute: result=%+v err=%v", result, err)
	}
	if !strings.HasPrefix(strings.TrimSpace(result.Output), "[") {
		t.Fatalf("expected JSON array, got %q", result.Output)
	}
	var verdicts []relevanceGateVerdict
	if err := json.Unmarshal([]byte(result.Output), &verdicts); err != nil {
		t.Fatalf("decode verdicts: %v; output=%q", err, result.Output)
	}
	return verdicts
}

func relevanceGateAssertOrder(t *testing.T, verdicts []relevanceGateVerdict, paths []string) {
	t.Helper()
	if len(verdicts) != len(paths) {
		t.Fatalf("got %d verdicts, want %d: %+v", len(verdicts), len(paths), verdicts)
	}
	for i, path := range paths {
		if verdicts[i].Path != path {
			t.Errorf("verdict %d path=%q, want original path %q", i, verdicts[i].Path, path)
		}
		switch verdicts[i].Action {
		case "include", "summarize", "skip":
		default:
			t.Errorf("verdict %d has invalid action %q", i, verdicts[i].Action)
		}
	}
}

func relevanceGateAssertSummary(t *testing.T, got relevanceGateVerdict, full string) {
	t.Helper()
	if got.Action != "summarize" || got.Error != "" {
		t.Fatalf("expected successful summary, got %+v", got)
	}
	if len(got.Content) >= len(full) || !strings.Contains(got.Content, "UNIQUE-HEAD") || !strings.Contains(got.Content, "UNIQUE-TAIL") || strings.Contains(got.Content, "UNIQUE-MIDDLE") {
		t.Errorf("summary must retain head/tail and omit middle: bytes=%d full=%d", len(got.Content), len(full))
	}
	if !utf8.ValidString(got.Content) {
		t.Error("summary is not valid UTF-8")
	}
}

func TestRelevanceGateThresholdBoundaries(t *testing.T) {
	t.Parallel()
	// Check every choice at both exact thresholds, and just below each.
	for _, confidence := range []float64{0.599999, 0.60, 0.849999, 0.85, 1} {
		for _, choice := range []string{"include", "summarize", "skip"} {
			t.Run(fmt.Sprintf("%s/%.6f", choice, confidence), func(t *testing.T) {
				dir := t.TempDir()
				full := "UNIQUE-HEAD\n" + strings.Repeat("界", 40000) + "UNIQUE-MIDDLE" + strings.Repeat("界", 40000) + "\nUNIQUE-TAIL"
				relevanceGateFile(t, dir, "large.txt", full)
				body := fmt.Sprintf(`{"answers":{"file_0":{"type":"choice","choice":%q,"confidence":%g}}}`, choice, confidence)
				client, requests := relevanceGateServer(t, http.StatusOK, body)
				verdicts := relevanceGateExecute(t, NewRelevanceGateTool(dir, client), map[string]any{"paths": []string{"large.txt"}, "task_context": "Fix the parser"})
				relevanceGateAssertOrder(t, verdicts, []string{"large.txt"})
				got := verdicts[0]
				wantAction := choice
				if confidence < 0.60 {
					wantAction = "include"
				} else if confidence < 0.85 {
					wantAction = "summarize"
				}
				if got.Action != wantAction || got.Error != "" {
					t.Fatalf("choice=%s confidence=%g: got %+v, want %s", choice, confidence, got, wantAction)
				}
				switch wantAction {
				case "include":
					if got.Content != full {
						t.Errorf("include must return FULL file: got %d bytes, want %d", len(got.Content), len(full))
					}
				case "summarize":
					relevanceGateAssertSummary(t, got, full)
				case "skip":
					if got.Content != "" {
						t.Error("skip must not include file content")
					}
				}
				if n := len(requests()); n != 1 {
					t.Errorf("got %d SystemOne calls, want 1", n)
				}
			})
		}
	}
}

func TestRelevanceGateSingleBatchRequestAndOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paths := []string{"z.txt", "a.txt", "z.txt"}
	relevanceGateFile(t, dir, "z.txt", "z-preview-content")
	relevanceGateFile(t, dir, "a.txt", "a-preview-content")
	// Response order is deliberately different from input order, including a
	// repeated path that must retain its own original-index answer.
	client, requests := relevanceGateServer(t, http.StatusOK, `{"answers":{"file_2":{"type":"choice","choice":"include","confidence":0.99},"file_0":{"type":"choice","choice":"skip","confidence":0.99},"file_1":{"type":"choice","choice":"include","confidence":0.99}}}`)
	const task = "Repair parser handling of quoted identifiers"
	verdicts := relevanceGateExecute(t, NewRelevanceGateTool(dir, client), map[string]any{"paths": paths, "task_context": task})
	relevanceGateAssertOrder(t, verdicts, paths)
	if verdicts[0].Action != "skip" || verdicts[0].Content != "" || verdicts[1].Action != "include" || verdicts[1].Content != "a-preview-content" || verdicts[2].Action != "include" || verdicts[2].Content != "z-preview-content" {
		t.Fatalf("answers must be mapped by index, not path or map order: %+v", verdicts)
	}
	captured := requests()
	if len(captured) != 1 {
		t.Fatalf("got %d requests, want one batch", len(captured))
	}
	request := captured[0].Request
	if len(request.Questions) != len(paths) {
		t.Fatalf("questions=%+v, want one per input index", request.Questions)
	}
	for i := range paths {
		name := fmt.Sprintf("file_%d", i)
		q, ok := request.Questions[name]
		if !ok || q.Type != "choice" || strings.TrimSpace(q.InstructionsString()) == "" {
			t.Errorf("question %s missing or invalid: %+v", name, q)
		}
		criteria, ok := q.Criteria.(map[string]any)
		if !ok || len(criteria) != 3 {
			t.Errorf("question %s criteria=%+v, want include/summarize/skip", name, q.Criteria)
			continue
		}
		for _, choice := range []string{"include", "summarize", "skip"} {
			if description, ok := criteria[choice].(string); !ok || strings.TrimSpace(description) == "" {
				t.Errorf("question %s has missing criterion %s", name, choice)
			}
		}
	}
	for _, want := range []string{task, "z.txt", "a.txt", "z-preview-content", "a-preview-content"} {
		if !strings.Contains(request.StateString(), want) {
			t.Errorf("shared state missing %q", want)
		}
	}
}

func TestRelevanceGateInputShapes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paths := []string{"second.txt", "first.txt"}
	relevanceGateFile(t, dir, paths[0], "second-full-content")
	relevanceGateFile(t, dir, paths[1], "first-full-content")
	objects := []map[string]any{{"path": paths[0], "line": 7, "content": "search snippet"}, {"path": paths[1], "matches": []string{"another snippet"}}}
	encodedPaths, _ := json.Marshal(paths)
	encodedObjects, _ := json.Marshal(objects)
	for _, tc := range []struct {
		name   string
		params map[string]any
	}{
		{"paths", map[string]any{"paths": paths}},
		{"input strings", map[string]any{"input": paths}},
		{"input objects", map[string]any{"input": objects}},
		{"pipeline encoded strings", map[string]any{"input": string(encodedPaths)}},
		{"pipeline encoded objects", map[string]any{"input": string(encodedObjects)}},
		{"input mixed", map[string]any{"input": []any{paths[0], objects[1]}}},
		{"absolute paths", map[string]any{"paths": []string{filepath.Join(dir, paths[0]), filepath.Join(dir, paths[1])}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.params["task_context"] = "Find parser inputs"
			client, requests := relevanceGateServer(t, http.StatusOK, `{"answers":{"file_1":{"type":"choice","choice":"include","confidence":0.9},"file_0":{"type":"choice","choice":"include","confidence":0.9}}}`)
			verdicts := relevanceGateExecute(t, NewRelevanceGateTool(dir, client), tc.params)
			wantPaths := paths
			if tc.name == "absolute paths" {
				wantPaths = tc.params["paths"].([]string)
			}
			relevanceGateAssertOrder(t, verdicts, wantPaths)
			for i, full := range []string{"second-full-content", "first-full-content"} {
				if verdicts[i].Action != "include" || verdicts[i].Content != full || verdicts[i].Error != "" {
					t.Errorf("input %d must read actual file, not snippets: %+v", i, verdicts[i])
				}
			}
			if len(requests()) != 1 {
				t.Errorf("got %d calls, want one batch", len(requests()))
			}
		})
	}
}

func TestRelevanceGateFallbacks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"HTTP failure", "upstream unavailable", http.StatusServiceUnavailable},
		{"authentication failure", `{"error":"unauthorized"}`, http.StatusUnauthorized},
		{"invalid response JSON", "not JSON", http.StatusOK},
		{"missing answer", `{"answers":{}}`, http.StatusOK},
		{"wrong index", `{"answers":{"file_7":{"type":"choice","choice":"skip","confidence":1}}}`, http.StatusOK},
		{"null answer", `{"answers":{"file_0":null}}`, http.StatusOK},
		{"missing choice", `{"answers":{"file_0":{"type":"choice","confidence":1}}}`, http.StatusOK},
		{"invalid choice", `{"answers":{"file_0":{"type":"choice","choice":"discard","confidence":1}}}`, http.StatusOK},
		{"wrong answer type", `{"answers":{"file_0":{"type":"score","score":1,"confidence":1}}}`, http.StatusOK},
		{"missing confidence", `{"answers":{"file_0":{"type":"choice","choice":"skip"}}}`, http.StatusOK},
		{"negative confidence", `{"answers":{"file_0":{"type":"choice","choice":"skip","confidence":-0.01}}}`, http.StatusOK},
		{"confidence above one", `{"answers":{"file_0":{"type":"choice","choice":"skip","confidence":1.01}}}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			full := "fallback head\n" + strings.Repeat("0123456789", 20000) + "\nfallback tail"
			relevanceGateFile(t, dir, "large.txt", full)
			client, requests := relevanceGateServer(t, tc.status, tc.body)
			verdicts := relevanceGateExecute(t, NewRelevanceGateTool(dir, client), map[string]any{"paths": []string{"large.txt"}, "task_context": "Inspect parser"})
			relevanceGateAssertOrder(t, verdicts, []string{"large.txt"})
			got := verdicts[0]
			if got.Action != "include" || got.Content != full || !strings.Contains(strings.ToLower(got.Reason), "fallback") {
				t.Errorf("must fall back to FULL include with reason: action=%q bytes=%d reason=%q", got.Action, len(got.Content), got.Reason)
			}
			if len(requests()) != 1 {
				t.Errorf("got %d requests, want 1", len(requests()))
			}
		})
	}
}

func TestRelevanceGatePartialMissingAnswer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	relevanceGateFile(t, dir, "first.txt", "first full content")
	relevanceGateFile(t, dir, "second.txt", "second full content")
	client, requests := relevanceGateServer(t, http.StatusOK, `{"answers":{"file_0":{"type":"choice","choice":"skip","confidence":0.9}}}`)
	paths := []string{"first.txt", "second.txt"}
	verdicts := relevanceGateExecute(t, NewRelevanceGateTool(dir, client), map[string]any{"paths": paths, "task_context": "Repair parser"})
	relevanceGateAssertOrder(t, verdicts, paths)
	if verdicts[0].Action != "skip" || verdicts[0].Content != "" {
		t.Errorf("valid answer must survive a missing sibling answer: %+v", verdicts[0])
	}
	if verdicts[1].Action != "include" || verdicts[1].Content != "second full content" || !strings.Contains(strings.ToLower(verdicts[1].Reason), "fallback") {
		t.Errorf("missing sibling must fall back individually: %+v", verdicts[1])
	}
	if len(requests()) != 1 {
		t.Errorf("got %d calls, want 1", len(requests()))
	}
}

func TestRelevanceGateNilAndUnavailableClientFullFallback(t *testing.T) {
	t.Parallel()
	closedServer := httptest.NewServer(http.NotFoundHandler())
	closedURL := closedServer.URL
	closedServer.Close()
	for _, tc := range []struct {
		name   string
		client *jev.Client
	}{
		{"nil client", nil},
		{"transport failure", jev.NewClient("key", "", closedURL)},
		{"missing API key", jev.NewClient(" ", "", closedURL)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			full := "HEAD\n" + strings.Repeat("all original bytes\n", 12000) + "TAIL"
			paths := []string{"large.txt", "empty.txt"}
			relevanceGateFile(t, dir, paths[0], full)
			relevanceGateFile(t, dir, paths[1], "")
			verdicts := relevanceGateExecute(t, NewRelevanceGateTool(dir, tc.client), map[string]any{"paths": paths, "task_context": "Inspect files"})
			relevanceGateAssertOrder(t, verdicts, paths)
			for i, want := range []string{full, ""} {
				got := verdicts[i]
				if got.Action != "include" || got.Content != want || got.Error != "" || !strings.Contains(strings.ToLower(got.Reason), "fallback") {
					t.Errorf("file %s: action=%q bytes=%d error=%q reason=%q; want full fallback (%d bytes)", got.Path, got.Action, len(got.Content), got.Error, got.Reason, len(want))
				}
			}
		})
	}
}

func TestRelevanceGateUnreadablePaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	relevanceGateFile(t, dir, "readable.txt", "readable content")
	client, _ := relevanceGateServer(t, http.StatusOK, `{"answers":{"file_2":{"type":"choice","choice":"include","confidence":1}}}`)
	paths := []string{"missing.txt", "directory", "readable.txt"}
	verdicts := relevanceGateExecute(t, NewRelevanceGateTool(dir, client), map[string]any{"paths": paths, "task_context": "Inspect inputs"})
	relevanceGateAssertOrder(t, verdicts, paths)
	for _, got := range verdicts[:2] {
		if got.Action != "include" || strings.TrimSpace(got.Error) == "" || got.Content != "" {
			t.Errorf("unreadable path must report include+error, not disappear: %+v", got)
		}
	}
	if verdicts[2].Action != "include" || verdicts[2].Content != "readable content" || verdicts[2].Error != "" {
		t.Errorf("unreadable siblings must not break readable files: %+v", verdicts[2])
	}
}

func TestRelevanceGateSecretsNeverReachAPI(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	secretPaths := []string{".env", ".env.local", ".env.example", "nested/.env.production", "certificate.pem", "private.key", "id_rsa", "nested/id_rsa", "keys/config.txt", ".ssh/config", "nested/keys/anything.txt", "nested/.ssh/known_hosts"}
	for i, path := range secretPaths {
		relevanceGateFile(t, dir, path, fmt.Sprintf("SECRET-CONTENT-%d-never-send", i))
	}
	if err := os.Symlink(filepath.Join(dir, ".env.local"), filepath.Join(dir, "innocent-alias.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "keys"), filepath.Join(dir, "ordinary-dir")); err != nil {
		t.Fatal(err)
	}
	secretPaths = append(secretPaths, "innocent-alias.txt", "ordinary-dir/config.txt", "missing.key")
	// A normal symlink must still work; don't solve secret exclusion by
	// rejecting every symlink unconditionally.
	relevanceGateFile(t, dir, "public.txt", "PUBLIC-PREVIEW-CONTENT")
	if err := os.Symlink(filepath.Join(dir, "public.txt"), filepath.Join(dir, "public-alias.txt")); err != nil {
		t.Fatal(err)
	}
	paths := append([]string{secretPaths[0], "public.txt"}, secretPaths[1:]...)
	paths = append(paths, "public-alias.txt")
	objects := make([]map[string]any, len(paths))
	for i, path := range paths {
		objects[i] = map[string]any{"path": path, "line": i + 1, "content": fmt.Sprintf("SECRET-SNIPPET-%d-never-send", i), "matches": []any{map[string]any{"text": "SECRET-NESTED-SNIPPET-never-send"}}}
		if path == "public.txt" || path == "public-alias.txt" {
			objects[i] = map[string]any{"path": path}
		}
	}
	for _, encoded := range []bool{false, true} {
		t.Run(fmt.Sprintf("encoded=%t", encoded), func(t *testing.T) {
			lastIndex := len(paths) - 1
			body := fmt.Sprintf(`{"answers":{"file_1":{"type":"choice","choice":"include","confidence":1},"file_%d":{"type":"choice","choice":"include","confidence":1}}}`, lastIndex)
			client, requests := relevanceGateServer(t, http.StatusOK, body)
			var input any = objects
			if encoded {
				raw, _ := json.Marshal(objects)
				input = string(raw)
			}
			verdicts := relevanceGateExecute(t, NewRelevanceGateTool(dir, client), map[string]any{"input": input, "task_context": "Repair public parser"})
			relevanceGateAssertOrder(t, verdicts, paths)
			for i, got := range verdicts {
				if i == 1 || i == lastIndex {
					if got.Action != "include" || got.Content != "PUBLIC-PREVIEW-CONTENT" || got.Error != "" {
						t.Errorf("public file %d: %+v", i, got)
					}
					continue
				}
				if got.Action != "skip" || !strings.Contains(strings.ToLower(got.Reason), "secret") || got.Content != "" {
					t.Errorf("secret must be skipped with secret reason and no content: %+v", got)
				}
			}
			captured := requests()
			if len(captured) != 1 {
				t.Fatalf("mixed inputs must issue one public-only batch, got %d", len(captured))
			}
			request := captured[0]
			wantNames := map[string]bool{"file_1": true, fmt.Sprintf("file_%d", lastIndex): true}
			if len(request.Request.Questions) != 2 {
				t.Errorf("secret questions leaked: %+v", request.Request.Questions)
			}
			for name := range request.Request.Questions {
				if !wantNames[name] {
					t.Errorf("question %q must use original public input index", name)
				}
			}
			for _, forbidden := range append(append([]string(nil), secretPaths...), "SECRET-CONTENT-", "SECRET-SNIPPET-", "SECRET-NESTED-SNIPPET-") {
				if strings.Contains(request.Raw, forbidden) {
					t.Errorf("secret path/content/snippet %q reached API", forbidden)
				}
			}
		})
	}
}

func TestRelevanceGateSecretOnlyMakesNoRequest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	paths := []string{".env", "keys/secret.txt", "id_rsa", "missing.pem"}
	for _, path := range paths[:3] {
		relevanceGateFile(t, dir, path, "PRIVATE-MATERIAL")
	}
	client, requests := relevanceGateServer(t, http.StatusOK, `{"answers":{}}`)
	for _, client := range []*jev.Client{client, nil} {
		verdicts := relevanceGateExecute(t, NewRelevanceGateTool(dir, client), map[string]any{"paths": paths, "task_context": "Inspect inputs"})
		relevanceGateAssertOrder(t, verdicts, paths)
		for _, got := range verdicts {
			if got.Action != "skip" || got.Content != "" || !strings.Contains(strings.ToLower(got.Reason), "secret") {
				t.Errorf("secret policy must precede nil-client or read-error fallback: %+v", got)
			}
		}
	}
	if len(requests()) != 0 {
		t.Errorf("secret-only input made %d API requests", len(requests()))
	}
}

func TestRelevanceGatePreviewBudget(t *testing.T) {
	t.Parallel()
	for _, count := range []int{1, 96} {
		t.Run(fmt.Sprintf("files=%d", count), func(t *testing.T) {
			dir := t.TempDir()
			paths := make([]string, count)
			for i := range paths {
				paths[i] = fmt.Sprintf("file-%03d.txt", i)
				relevanceGateFile(t, dir, paths[i], fmt.Sprintf("FILE-PREVIEW-%03d\n", i)+strings.Repeat("界", 50000)+fmt.Sprintf("\nFILE-TAIL-%03d", i))
			}
			// No answers forces full-content fallback while exercising request
			// construction. A preview cap must never become an output cap.
			client, requests := relevanceGateServer(t, http.StatusOK, `{"answers":{}}`)
			const task = "Investigate Unicode parser regression"
			verdicts := relevanceGateExecute(t, NewRelevanceGateTool(dir, client), map[string]any{"paths": paths, "task_context": task})
			relevanceGateAssertOrder(t, verdicts, paths)
			seen := make(map[string]int)
			captured := requests()
			if len(captured) == 0 || (count == 1 && len(captured) != 1) {
				t.Fatalf("unexpected batch count %d for %d files", len(captured), count)
			}
			for _, captured := range captured {
				request := captured.Request
				if len(request.StateString()) > 64*1024 || !utf8.ValidString(request.StateString()) {
					t.Errorf("shared API state exceeds 64KiB or is invalid UTF-8: bytes=%d", len(request.StateString()))
				}
				if !strings.Contains(request.StateString(), task) {
					t.Error("bounded batch dropped task context")
				}
				for name := range request.Questions {
					seen[name]++
					var i int
					if _, err := fmt.Sscanf(name, "file_%d", &i); err != nil || i < 0 || i >= count {
						t.Errorf("invalid original-index question %q", name)
						continue
					}
					if !strings.Contains(request.StateString(), paths[i]) || !strings.Contains(request.StateString(), fmt.Sprintf("FILE-PREVIEW-%03d", i)) {
						t.Errorf("question %s has no matching path/file preview in shared state", name)
					}
				}
			}
			for i, got := range verdicts {
				if seen[fmt.Sprintf("file_%d", i)] != 1 {
					t.Errorf("input %d was classified %d times, want once", i, seen[fmt.Sprintf("file_%d", i)])
				}
				full, err := os.ReadFile(filepath.Join(dir, paths[i]))
				if err != nil {
					t.Fatal(err)
				}
				if got.Action != "include" || got.Content != string(full) || !strings.Contains(strings.ToLower(got.Reason), "fallback") {
					t.Errorf("preview cap corrupted full fallback for %s: action=%s bytes=%d want=%d reason=%q", got.Path, got.Action, len(got.Content), len(full), got.Reason)
				}
			}
		})
	}
}

func TestRelevanceGateEmptyInput(t *testing.T) {
	t.Parallel()
	client, requests := relevanceGateServer(t, http.StatusOK, `{"answers":{}}`)
	for _, params := range []map[string]any{{"paths": []string{}}, {"input": []string{}}, {"input": "[]"}} {
		params["task_context"] = "Inspect files"
		verdicts := relevanceGateExecute(t, NewRelevanceGateTool(t.TempDir(), client), params)
		if !reflect.DeepEqual(verdicts, []relevanceGateVerdict{}) {
			t.Errorf("empty input must return [], got %+v", verdicts)
		}
	}
	if len(requests()) != 0 {
		t.Errorf("empty input made %d API calls", len(requests()))
	}
}
