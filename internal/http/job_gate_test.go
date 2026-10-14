package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

type fakeGateFetchTool struct {
	output string
	params json.RawMessage
}

func (f *fakeGateFetchTool) Name() string                   { return "fake_fetch" }
func (f *fakeGateFetchTool) Description() string            { return "fake" }
func (f *fakeGateFetchTool) Schema() map[string]interface{} { return map[string]interface{}{} }
func (f *fakeGateFetchTool) Execute(_ context.Context, p json.RawMessage) (*tools.Result, error) {
	f.params = p
	return &tools.Result{Success: true, Output: f.output}, nil
}

// newGateTestServer wires a fake Jev answering with the given noul value.
func newGateTestServer(t *testing.T, noul float64, fetched string) (*Server, *fakeGateFetchTool, *int) {
	t.Helper()
	calls := 0
	jevSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			State     string                    `json:"state"`
			Questions map[string]map[string]any `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !strings.Contains(req.State, "fetched-items") || req.Questions["actionable"]["type"] != "noul" {
			t.Errorf("unexpected jev request: %+v", req)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{"actionable": map[string]any{"type": "noul", "noul": noul}}})
	}))
	t.Cleanup(jevSrv.Close)

	server, _ := newClassifyRegistrationTestServer(t, config.Provider{APIKey: "k", BaseURL: jevSrv.URL}, nil)
	fetch := &fakeGateFetchTool{output: fetched}
	server.toolManager.Register(fetch)
	return server, fetch, &calls
}

func gateJob() *storage.RecurringJob {
	return &storage.RecurringJob{
		ID: "job-1", Name: "monitor", TaskPrompt: "triage inbox", TaskPromptSource: "text",
		GateTool: "fake_fetch", GateParams: `{"q":"x"}`, ScheduleCron: "* * * * *", Enabled: true,
	}
}

func TestEvaluateJobGateSkipsWhenNothingActionable(t *testing.T) {
	server, fetch, calls := newGateTestServer(t, 0.05, "fetched-items: newsletter")
	decision := server.evaluateJobGate(context.Background(), gateJob())
	if decision.Run || *calls != 1 || !strings.Contains(decision.Reason, "0.05") {
		t.Fatalf("want skip with reason, got %+v calls=%d", decision, *calls)
	}
	if string(fetch.params) != `{"q":"x"}` {
		t.Fatalf("fetch params = %s", fetch.params)
	}
}

func TestEvaluateJobGateRunsWhenActionable(t *testing.T) {
	server, _, _ := newGateTestServer(t, 0.9, "fetched-items: outage alert")
	if decision := server.evaluateJobGate(context.Background(), gateJob()); !decision.Run {
		t.Fatalf("want run, got %+v", decision)
	}
}

func TestEvaluateJobGateSkipsEmptyFetchWithoutJev(t *testing.T) {
	server, _, calls := newGateTestServer(t, 0.9, "  ")
	decision := server.evaluateJobGate(context.Background(), gateJob())
	if decision.Run || *calls != 0 {
		t.Fatalf("empty fetch must skip without calling jev, got %+v calls=%d", decision, *calls)
	}
}

func TestEvaluateJobGateFailsOpen(t *testing.T) {
	server, _, _ := newGateTestServer(t, 0.9, "x")
	job := gateJob()
	job.GateTool = "missing_tool"
	if decision := server.evaluateJobGate(context.Background(), job); !decision.Run {
		t.Fatalf("gate errors must not block the job, got %+v", decision)
	}
	job.GateTool = ""
	if decision := server.evaluateJobGate(context.Background(), job); !decision.Run {
		t.Fatalf("job without gate must run, got %+v", decision)
	}
}

func TestExecuteJobRecordsSkippedExecution(t *testing.T) {
	server, _, _ := newGateTestServer(t, 0.01, "fetched-items: spam")
	job := gateJob()
	job.CreatedAt, job.UpdatedAt = time.Now(), time.Now()
	if err := server.store.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	exec, err := server.ExecuteJob(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if exec.Status != "skipped" || exec.SessionID != "" || exec.Output == "" || exec.FinishedAt == nil {
		t.Fatalf("unexpected execution: %+v", exec)
	}
	stored, err := server.store.GetJobExecution(exec.ID)
	if err != nil || stored.Status != "skipped" {
		t.Fatalf("stored execution = %+v err=%v", stored, err)
	}
	got, _ := server.store.GetJob(job.ID)
	if got.GateTool != "fake_fetch" || got.GateParams != `{"q":"x"}` {
		t.Fatalf("gate config not persisted: %+v", got)
	}
}
