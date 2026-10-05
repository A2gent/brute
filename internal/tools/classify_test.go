package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/llm/jev"
)

func TestClassifyRequestAndOutput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind, answer, response string
		criteria               map[string]string
		wantCriteria           any
	}{
		{"choice", `"code"`, `{"type":"choice","choice":"code","confidence":0.9,"probabilities":{"code":0.95,"docs":0.05}}`, map[string]string{"code": "Code", "docs": "Docs"}, map[string]any{"code": "Code", "docs": "Docs"}},
		{"score", `0`, `{"type":"score","score":0,"confidence":1,"probabilities":{"0":1,"1":0}}`, map[string]string{"0": "Low", "1": "High"}, []any{"Low", "High"}},
		{"noul", `0`, `{"type":"noul","noul":0}`, nil, nil},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					State     string
					Questions map[string]struct {
						Type, Instructions string
						Criteria           any
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				q := request.Questions["answer"]
				if request.State != "a report" || q.Type != tc.kind || q.Instructions != "Question?" || !reflect.DeepEqual(q.Criteria, tc.wantCriteria) {
					t.Errorf("request = %+v, question = %+v", request, q)
				}
				_, _ = w.Write([]byte(`{"answers":{"answer":` + tc.response + `}}`))
			}))
			defer server.Close()
			tool := NewClassifyTool(t.TempDir(), jev.NewClient("key", "", server.URL+"/v1"))
			params, _ := json.Marshal(map[string]any{"input": "a report", "question": "Question?", "type": tc.kind, "criteria": tc.criteria})
			result, err := tool.Execute(context.Background(), params)
			if err != nil || !result.Success {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			var output map[string]json.RawMessage
			if err := json.Unmarshal([]byte(result.Output), &output); err != nil {
				t.Fatal(err)
			}
			if string(output["answer"]) != tc.answer || len(output) != 3 {
				t.Fatalf("output = %s", result.Output)
			}
			if tc.kind == "noul" && (string(output["confidence"]) != "null" || string(output["probabilities"]) != "null") {
				t.Fatalf("noul must not fabricate confidence: %s", result.Output)
			}
		})
	}
}

func TestClassifyTruncatesInputAndPath(t *testing.T) {
	t.Parallel()
	for _, fromFile := range []bool{false, true} {
		t.Run(map[bool]string{false: "input", true: "path"}[fromFile], func(t *testing.T) {
			state := "HEAD:" + strings.Repeat("界", classifyMaxStateBytes) + ":TAIL"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request jev.SystemOneRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if len(request.StateString()) > classifyMaxStateBytes || !utf8.ValidString(request.StateString()) || !strings.HasPrefix(request.StateString(), "HEAD:") || !strings.HasSuffix(request.StateString(), ":TAIL") || !strings.Contains(request.StateString(), "[truncated]") {
					t.Errorf("invalid truncation: bytes=%d", len(request.StateString()))
				}
				_, _ = w.Write([]byte(`{"answers":{"answer":{"type":"noul","noul":0.7}}}`))
			}))
			defer server.Close()
			dir := t.TempDir()
			params := map[string]any{"question": "Relevant?", "type": "noul", "input": state}
			if fromFile {
				if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte(state), 0600); err != nil {
					t.Fatal(err)
				}
				delete(params, "input")
				params["path"] = "report.txt"
			}
			raw, _ := json.Marshal(params)
			result, err := NewClassifyTool(dir, jev.NewClient("key", "", server.URL)).Execute(context.Background(), raw)
			if err != nil || !result.Success {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestClassifyErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, key, params, body, want string
		status                        int
	}{
		{"missing key", " ", `{"input":"text","question":"Q?","type":"noul"}`, "", "API key", 200},
		{"non-2xx", "key", `{"input":"text","question":"Q?","type":"noul"}`, "failure", "HTTP 503", 503},
		{"no answer", "key", `{"input":"text","question":"Q?","type":"noul"}`, `{"answers":{}}`, "no answer", 200},
		{"no value", "key", `{"input":"text","question":"Q?","type":"score","criteria":{"0":"low","1":"high"}}`, `{"answers":{"answer":{"type":"score"}}}`, "no score", 200},
		{"invalid JSON", "key", `{`, "", "parameters", 200},
		{"missing question", "key", `{"input":"text","type":"noul"}`, "", "question", 200},
		{"invalid type", "key", `{"input":"text","question":"Q?","type":"other"}`, "", "type", 200},
		{"missing input", "key", `{"question":"Q?","type":"noul"}`, "", "input or path", 200},
		{"both sources", "key", `{"input":"text","path":"report","question":"Q?","type":"noul"}`, "", "input or path", 200},
		{"missing criteria", "key", `{"input":"text","question":"Q?","type":"choice"}`, "", "criteria", 200},
		{"invalid score criteria", "key", `{"input":"text","question":"Q?","type":"score","criteria":{"1":"low","3":"high"}}`, "", "0", 200},
		{"path error", "key", `{"path":"missing.txt","question":"Q?","type":"noul"}`, "", "read", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			result, err := NewClassifyTool(t.TempDir(), jev.NewClient(tc.key, "", server.URL)).Execute(context.Background(), json.RawMessage(tc.params))
			if err != nil || result.Success || !strings.Contains(result.Error, tc.want) {
				t.Fatalf("result=%+v err=%v, want %q", result, err, tc.want)
			}
		})
	}
}

func TestClassifyTimeout(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	tool := NewClassifyTool(t.TempDir(), jev.NewClient("key", "", server.URL))
	tool.timeout = 10 * time.Millisecond
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"input":"text","question":"Q?","type":"noul"}`))
	if err != nil || result.Success || !strings.Contains(result.Error, "deadline exceeded") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
