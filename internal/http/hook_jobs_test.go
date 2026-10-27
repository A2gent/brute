package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
)

func TestCreateWebhookJobHasNoScheduleAndIsNeverDue(t *testing.T) {
	server, store := newBruteHTTPProxyTestServer(t)
	body := `{"name":"CI watcher","task_prompt":"fix it","enabled":true,"trigger_type":"webhook","trigger_source":"bitbucket","trigger_events":"ci_failed"}`
	rec := httptest.NewRecorder()
	server.handleCreateJob(rec, httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp JobResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.TriggerType != jobTriggerWebhook || resp.ScheduleCron != "" || resp.NextRunAt != nil || resp.TriggerSource != "bitbucket" {
		t.Fatalf("unexpected webhook job response: %+v", resp)
	}
	due, err := store.GetDueJobs(time.Now().Add(time.Hour))
	if err != nil || len(due) != 0 {
		t.Fatalf("webhook job must never be due: %v %+v", err, due)
	}
}

// newHookTestServer answers every Jev condition check with "no", so accepted events end
// as skipped executions without starting an LLM session.
func newHookTestServer(t *testing.T) (*Server, *storage.SQLiteStore) {
	t.Helper()
	jevSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{"match": map[string]any{"type": "noul", "noul": 0.01}}})
	}))
	t.Cleanup(jevSrv.Close)
	return newClassifyRegistrationTestServer(t, config.Provider{APIKey: "k", BaseURL: jevSrv.URL}, nil)
}

func webhookEnvelope(endpointID, eventKey, body string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{"internal_event": webhookInboundInternalEvent, "source": squareWebhookSource, "webhook_endpoint_id": endpointID},
		"http": map[string]any{
			"headers":     map[string][]string{"X-Event-Key": {eventKey}},
			"body_base64": base64.StdEncoding.EncodeToString([]byte(body)),
		},
	})
	return raw
}

func TestWebhookInboundRoutesByEndpointAndFilters(t *testing.T) {
	server, store := newHookTestServer(t)
	now := time.Now()
	job := &storage.RecurringJob{
		ID: "hook-1", Name: "CI", TaskPrompt: "fix", TaskPromptSource: "text", Enabled: true,
		TriggerType: jobTriggerWebhook, TriggerEvents: "ci_failed", TriggerBranches: "main",
		TriggerCondition: "only real failures", WebhookEndpointID: "ep-1", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	dispatch := func(endpoint, key, body string) []hookDispatchResult {
		out, err := server.handleA2AInternalEvent(t.Context(), webhookEnvelope(endpoint, key, body))
		if err != nil {
			t.Fatalf("internal event: %v", err)
		}
		var decoded struct{ Hooks []hookDispatchResult }
		_ = json.Unmarshal(out.Payload, &decoded)
		return decoded.Hooks
	}
	failed := `{"commit_status":{"state":"FAILED","refname":"main","commit":{"hash":"c1"}},"repository":{"full_name":"ws/r"}}`

	if got := dispatch("other-endpoint", "repo:commit_status_updated", failed); len(got) != 0 {
		t.Fatalf("foreign endpoint must not reach the hook: %+v", got)
	}
	if got := dispatch("ep-1", "repo:push", `{}`); len(got) != 1 || got[0].Status != "ignored" {
		t.Fatalf("push should be filtered out: %+v", got)
	}
	if got := dispatch("ep-1", "repo:commit_status_updated", failed); len(got) != 1 || got[0].Status != "accepted" {
		t.Fatalf("CI failure on main should be accepted: %+v", got)
	}
	if got := dispatch("ep-1", "repo:commit_status_updated", failed); got[0].Status != "ignored" || got[0].Reason != "duplicate event" {
		t.Fatalf("repeated delivery should be deduplicated: %+v", got)
	}

	// The Jev condition answers "no", so the accepted event becomes a skipped execution.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		execs, _ := store.ListJobExecutions(job.ID, 5)
		if len(execs) == 1 && execs[0].Status == "skipped" && strings.Contains(execs[0].Output, "did not match condition") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected a skipped execution from the Jev condition")
}

func TestTriggerJobRejectsBadSignature(t *testing.T) {
	server, store := newBruteHTTPProxyTestServer(t)
	now := time.Now()
	job := &storage.RecurringJob{ID: "hook-sig", Name: "x", TaskPrompt: "x", Enabled: true, TriggerType: jobTriggerWebhook,
		WebhookSigningSecret: "secret", CreatedAt: now, UpdatedAt: now}
	if err := store.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	res := server.dispatchHookEvent(job, http.Header{"X-Hub-Signature-256": {"sha256=deadbeef"}}, []byte(`{}`))
	if res.Status != "rejected" {
		t.Fatalf("bad signature must be rejected: %+v", res)
	}
}

func TestBitbucketWebhookEventsMapping(t *testing.T) {
	got := strings.Join(bitbucketWebhookEvents("ci_failed, pr_comment, repo:push"), ",")
	if got != "repo:commit_status_updated,repo:commit_status_created,pullrequest:comment_created,repo:push" {
		t.Fatalf("unexpected events %s", got)
	}
}

func TestDetectRepository(t *testing.T) {
	if r := detectRepository("git@github.com:acme/app.git"); r == nil || r.Provider != "github" || r.Owner != "acme" || r.Name != "app" {
		t.Fatalf("github remote: %+v", r)
	}
	if r := detectRepository("https://bitbucket.org/ws/repo.git"); r == nil || r.Provider != "bitbucket" || r.Owner != "ws" {
		t.Fatalf("bitbucket remote: %+v", r)
	}
}

func TestUpdateScheduleLoopToWebhookClearsCron(t *testing.T) {
	server, store := newBruteHTTPProxyTestServer(t)
	now := time.Now()
	next := now.Add(time.Hour)
	job := &storage.RecurringJob{ID: "loop-1", Name: "x", TaskPrompt: "x", TaskPromptSource: "text", Enabled: true,
		ScheduleHuman: "every hour", ScheduleCron: "0 * * * *", NextRunAt: &next, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveJob(job); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/jobs/loop-1", strings.NewReader(`{"trigger_type":"webhook","trigger_source":"github"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("jobID", "loop-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	server.handleUpdateJob(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", rec.Code, rec.Body.String())
	}
	got, _ := store.GetJob("loop-1")
	if got.TriggerType != jobTriggerWebhook || got.ScheduleCron != "" || got.NextRunAt != nil || got.TriggerSource != "github" {
		t.Fatalf("switch to webhook did not clear schedule: %+v", got)
	}
}
