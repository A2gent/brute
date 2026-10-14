package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/jev"
)

type traceTestTool struct {
	client *jev.Client
	fail   bool
}

func (*traceTestTool) Name() string                   { return "trace_test" }
func (*traceTestTool) Description() string            { return "test" }
func (*traceTestTool) Schema() map[string]interface{} { return nil }
func (t *traceTestTool) Execute(ctx context.Context, _ json.RawMessage) (*Result, error) {
	_, err := t.client.SystemOne(ctx, jev.SystemOneRequest{State: "private", Questions: map[string]jev.Question{"ok": {Type: "noul"}}})
	if err != nil {
		return nil, err
	}
	if t.fail {
		return nil, errors.New("after Jev")
	}
	return &Result{Success: true, Output: "result", Metadata: map[string]interface{}{"existing": true}}, nil
}
func TestManagerPersistsJevTraceIncludingToolFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"ok":{"type":"noul","noul":0.8}}}`))
	}))
	defer server.Close()
	for _, fail := range []bool{false, true} {
		manager := &Manager{tools: map[string]Tool{}}
		manager.Register(&traceTestTool{client: jev.NewClient("key", "", server.URL), fail: fail})
		got := manager.ExecuteParallel(context.Background(), []llm.ToolCall{{ID: "1", Name: "trace_test", Input: `{}`}})[0]
		requests, ok := got.Metadata[jev.TraceMetadataKey].([]jev.RequestTrace)
		if !ok || len(requests) != 1 || got.IsError != fail {
			t.Fatalf("result=%+v", got)
		}
	}
}
