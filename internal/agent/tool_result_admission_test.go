package agent

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/contextcompress"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

type admissionTool struct {
	name, output string
	metadata     map[string]interface{}
}

func (t admissionTool) Name() string        { return t.name }
func (t admissionTool) Description() string { return "test" }
func (t admissionTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t admissionTool) Execute(context.Context, json.RawMessage) (*tools.Result, error) {
	return &tools.Result{Success: true, Output: t.output, Metadata: t.metadata}, nil
}

func TestLoopToolResultAdmission(t *testing.T) {
	for _, name := range []string{"browser_chrome", "fetch_url", "chrome_extension", "context_retrieve"} {
		t.Run(name, func(t *testing.T) {
			store, err := storage.NewSQLiteStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			sm := session.NewManager(store)
			sm.SetJSONLFolder(t.TempDir())
			sess, err := sm.Create("agent")
			if err != nil {
				t.Fatal(err)
			}
			large := "HEAD\n" + strings.Repeat("padding\n", 10000) + "hidden target\nTAIL"
			small := "unchanged short output"
			manager := tools.NewManager(t.TempDir())
			manager.Register(admissionTool{name: name, output: large})
			manager.Register(admissionTool{name: "small", output: small})
			mock := &MockLLM{Responses: []*llm.ChatResponse{{ToolCalls: []llm.ToolCall{{ID: "large", Name: name, Input: "{}"}, {ID: "small", Name: "small", Input: "{}"}}}, {Content: "done"}}}
			ag := New(Config{ToolResultMaxTokens: 256, CompressToolResults: false}, mock, manager, sm)
			if _, _, err := ag.Run(context.Background(), sess, ""); err != nil {
				t.Fatal(err)
			}
			persisted, err := sm.Get(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			got := persisted.Messages[1].ToolResults
			if len(got) != 2 || !strings.Contains(got[0].Content, "admission_cap") || utf8.RuneCountInString(got[0].Content) > 1024 {
				t.Fatal("transcript contains uncapped result")
			}
			if got[1].Content != small {
				t.Fatal("small result changed")
			}
			if !reflect.DeepEqual(mock.CapturedRequests[1].Messages[1].ToolResults[0].Content, got[0].Content) {
				t.Fatal("next request changed admitted result")
			}
			if !strings.Contains(mock.CapturedRequests[1].SystemPrompt, "context_retrieve") {
				t.Fatal("missing retrieval instructions")
			}
			hash := strings.Split(strings.Split(got[0].Content, "hash=")[1], " ")[0]
			fresh := contextcompress.NewCompressorWithSessionStore(contextcompress.Config{}, sm)
			full, ok := fresh.Retrieve(sess.ID, hash, "")
			if !ok || full != large {
				t.Fatal("stored original lost after agent Save")
			}
			matches, ok := fresh.Retrieve(sess.ID, hash, "hidden target")
			if !ok || !strings.Contains(matches, "hidden target") {
				t.Fatal("query retrieval failed")
			}
			rawBytes, err := os.ReadFile(sm.JSONLPath(sess.ID))
			raw := string(rawBytes)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, strings.Repeat("padding", 100)) || strings.Contains(raw, strings.Repeat("padding\n", 100)) {
				t.Fatal("JSONL contains original")
			}
		})
	}
}

func TestAdmissionConfig(t *testing.T) {
	for _, tc := range []struct {
		env            string
		explicit, want int
	}{{"", 0, 8000}, {"2048", 0, 2048}, {"invalid", 0, 8000}, {"-1", 0, 8000}, {"1", 0, 128}, {"2048", 1024, 1024}} {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("A2GENT_TOOL_RESULT_MAX_TOKENS", tc.env)
			ag := New(Config{ToolResultMaxTokens: tc.explicit}, nil, tools.NewManager(t.TempDir()), nil)
			if ag.config.ToolResultMaxTokens != tc.want {
				t.Fatalf("got %d want %d", ag.config.ToolResultMaxTokens, tc.want)
			}
		})
	}
}

func TestAdmissionAndRequestCompressionKeepBothOriginals(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sm := session.NewManager(store)
	sess, err := sm.Create("agent")
	if err != nil {
		t.Fatal(err)
	}
	manager := tools.NewManager(t.TempDir())
	ag := New(Config{CompressToolResults: true}, nil, manager, sm)
	originalA := strings.Repeat("admission\n", 10000)
	result := ag.compressor.AdmitToolResult(sess, llm.ToolResult{Name: "browser_chrome", ToolCallID: "a", Content: originalA}, 8000)
	originalB := strings.Repeat("noise\n", 3000) + "FAIL: target\n"
	sess.AddAssistantMessage("", []session.ToolCall{{ID: "a", Name: "browser_chrome", Input: []byte("{}")}, {ID: "b", Name: "bash", Input: []byte("{}")}})
	sess.AddToolResult([]session.ToolResult{{Name: "browser_chrome", ToolCallID: "a", Content: result.Content}, {Name: "bash", ToolCallID: "b", Content: originalB}})
	if err := sm.Save(sess); err != nil {
		t.Fatal(err)
	}
	request := ag.buildRequest(sess)
	if err := sm.Save(sess); err != nil {
		t.Fatal(err)
	}
	fresh := contextcompress.NewCompressorWithSessionStore(contextcompress.Config{}, sm)
	for i, original := range []string{originalA, originalB} {
		text := request.Messages[1].ToolResults[i].Content
		hash := strings.Split(strings.Split(text, "hash=")[1], " ")[0]
		full, ok := fresh.Retrieve(sess.ID, hash, "")
		if !ok || full != original {
			t.Fatalf("original %d lost", i)
		}
	}
}

type admissionParkingTool struct {
	manager *session.Manager
	status  session.Status
}

func (admissionParkingTool) Name() string        { return "park" }
func (admissionParkingTool) Description() string { return "test" }
func (admissionParkingTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t admissionParkingTool) Execute(ctx context.Context, _ json.RawMessage) (*tools.Result, error) {
	sess, err := t.manager.Get(ctx.Value("session_id").(string))
	if err != nil {
		return nil, err
	}
	sess.Status = t.status
	if err := t.manager.Save(sess); err != nil {
		return nil, err
	}
	return &tools.Result{Success: true, Output: strings.Repeat("parked result\n", 10000)}, nil
}

func TestAdmissionSurvivesParking(t *testing.T) {
	for _, status := range []session.Status{session.StatusInputRequired, session.StatusWaitingExternal} {
		t.Run(string(status), func(t *testing.T) {
			store, err := storage.NewSQLiteStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			sm := session.NewManager(store)
			sess, err := sm.Create("agent")
			if err != nil {
				t.Fatal(err)
			}
			manager := tools.NewManager(t.TempDir())
			manager.Register(admissionParkingTool{manager: sm, status: status})
			mock := &MockLLM{Response: &llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "park", Name: "park", Input: "{}"}}}}
			ag := New(Config{ToolResultMaxTokens: 256}, mock, manager, sm)
			if _, _, err := ag.Run(context.Background(), sess, ""); err != nil {
				t.Fatal(err)
			}
			hash := strings.Split(strings.Split(sess.Messages[1].ToolResults[0].Content, "hash=")[1], " ")[0]
			fresh := contextcompress.NewCompressorWithSessionStore(contextcompress.Config{}, sm)
			full, ok := fresh.Retrieve(sess.ID, hash, "")
			if !ok || full != strings.Repeat("parked result\n", 10000) {
				t.Fatal("parked original lost")
			}
			persisted, err := sm.Get(sess.ID)
			if err != nil || persisted.Status != status {
				t.Fatal("parking status overwritten")
			}
		})
	}
}

func TestAdmissionScreenshotUsesSharedImageChannel(t *testing.T) {
	sess := session.New("agent")
	ag := New(Config{}, nil, tools.NewManager(t.TempDir()), nil)
	sess.AddAssistantMessage("", []session.ToolCall{{ID: "shot", Name: "chrome_extension", Input: []byte("{}")}})
	tr := ag.compressor.AdmitToolResult(sess, llm.ToolResult{Name: "chrome_extension", ToolCallID: "shot", Content: `{"data_url":"data:image/png;base64,YQ=="}`}, 8000)
	sess.AddToolResult([]session.ToolResult{{Name: tr.Name, ToolCallID: tr.ToolCallID, Content: tr.Content, Metadata: tr.Metadata}})
	request := ag.buildRequest(sess)
	if len(request.Messages) != 3 || request.Messages[2].Role != "user" || len(request.Messages[2].Images) != 1 || request.Messages[2].Images[0].DataBase64 != "YQ==" {
		t.Fatal("missing shared screenshot image")
	}
	if _, exists := request.Messages[1].ToolResults[0].Metadata["image_inline"]; exists {
		t.Fatal("duplicate provider image")
	}
	if _, exists := sess.Messages[1].ToolResults[0].Metadata["image_inline"]; !exists {
		t.Fatal("session metadata mutated")
	}
}

func TestAdmissionRealScreenshotWrappers(t *testing.T) {
	for _, name := range []string{"parallel", "pipeline"} {
		t.Run(name, func(t *testing.T) {
			manager := tools.NewManager(t.TempDir())
			payload := strings.Repeat("YQ==", 10000)
			manager.Register(admissionTool{name: "chrome_extension", output: `{"data_url":"data:image/png;base64,` + payload + `"}`})
			manager.Register(tools.NewParallelTool(manager))
			manager.Register(tools.NewPipelineTool(manager))
			params := json.RawMessage(`{"steps":[{"tool":"chrome_extension","args":{}}],"max_output_chars":1000}`)
			result, err := manager.Execute(context.Background(), name, params)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(result.Output, "YQ==") {
				t.Fatal("base64 reached wrapper text")
			}
			images, ok := result.Metadata["admission_images"].([]interface{})
			if !ok || len(images) != 1 {
				t.Fatal("wrapper lost image metadata")
			}
			if images[0].(map[string]interface{})["data_base64"] != payload {
				t.Fatal("wrapper truncated image")
			}
		})
	}
}

func TestAdmissionExpandedResults(t *testing.T) {
	sess := session.New("agent")
	original := strings.Repeat("expanded\n", 10000)
	sess.AddToolResult([]session.ToolResult{{ToolCallID: "read", Name: "read", Content: original}})
	ag := New(Config{ToolResultMaxTokens: 256}, nil, tools.NewManager(t.TempDir()), nil)
	request := &llm.ChatRequest{Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{{ToolCallID: "read", Name: "read", Content: original}}}}}
	ag.admitExpandedRequestResults(sess, request)
	got := request.Messages[0].ToolResults[0].Content
	if utf8.RuneCountInString(got) > 1024 || got != sess.Messages[0].ToolResults[0].Content {
		t.Fatal("expanded body bypassed cap")
	}
}
