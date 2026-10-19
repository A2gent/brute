// hook_jobs.go runs event-triggered loops ("hooks"): webhooks relayed by Square over
// the tunnel (internal_event "webhook_inbound") or POSTed locally to /jobs/{id}/trigger
// are normalized, filtered and turned into a regular loop execution.
package http

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/hooks"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/storage"
	"github.com/go-chi/chi/v5"
)

const (
	jobTriggerSchedule          = "schedule"
	jobTriggerWebhook           = "webhook"
	webhookInboundInternalEvent = "webhook_inbound"
	hookDedupWindow             = 30 * time.Minute
	hookMaxLocalBodyBytes       = 2 << 20
)

func normalizeJobTriggerType(value string) string {
	if strings.TrimSpace(value) == jobTriggerWebhook {
		return jobTriggerWebhook
	}
	return jobTriggerSchedule
}

func normalizeHookSource(value string) (string, error) {
	switch v := strings.TrimSpace(value); v {
	case "", hooks.SourceGitHub, hooks.SourceBitbucket, hooks.SourceGeneric:
		return v, nil
	default:
		return "", fmt.Errorf("unsupported trigger source: %s", v)
	}
}

func applyJobTrigger(job *storage.RecurringJob, f JobTriggerFields) error {
	source, err := normalizeHookSource(f.TriggerSource)
	if err != nil {
		return err
	}
	job.TriggerType = normalizeJobTriggerType(f.TriggerType)
	job.TriggerSource = source
	job.TriggerEvents = strings.TrimSpace(f.TriggerEvents)
	job.TriggerBranches = strings.TrimSpace(f.TriggerBranches)
	job.TriggerCondition = strings.TrimSpace(f.TriggerCondition)
	return nil
}

func applyJobTriggerUpdate(job *storage.RecurringJob, f JobTriggerUpdateFields) error {
	merged := jobTriggerFieldsFromJob(job)
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	set(&merged.TriggerType, f.TriggerType)
	set(&merged.TriggerSource, f.TriggerSource)
	set(&merged.TriggerEvents, f.TriggerEvents)
	set(&merged.TriggerBranches, f.TriggerBranches)
	set(&merged.TriggerCondition, f.TriggerCondition)
	return applyJobTrigger(job, merged)
}

func jobTriggerFieldsFromJob(job *storage.RecurringJob) JobTriggerFields {
	return JobTriggerFields{
		TriggerType:      normalizeJobTriggerType(job.TriggerType),
		TriggerSource:    job.TriggerSource,
		TriggerEvents:    job.TriggerEvents,
		TriggerBranches:  job.TriggerBranches,
		TriggerCondition: job.TriggerCondition,
	}
}

// hookDedup remembers recent event keys per job so provider retries and GitHub's
// parallel workflow_run/check_suite/status deliveries start only one session.
var hookDedup = struct {
	sync.Mutex
	seen map[string]time.Time
}{seen: map[string]time.Time{}}

func hookSeenRecently(jobID, key string, now time.Time) bool {
	if strings.TrimSpace(key) == "" {
		return false
	}
	hookDedup.Lock()
	defer hookDedup.Unlock()
	for k, at := range hookDedup.seen {
		if now.Sub(at) > hookDedupWindow {
			delete(hookDedup.seen, k)
		}
	}
	full := jobID + "|" + key
	if _, ok := hookDedup.seen[full]; ok {
		return true
	}
	hookDedup.seen[full] = now
	return false
}

type hookDispatchResult struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"` // accepted | ignored | rejected
	Reason string `json:"reason,omitempty"`
}

// dispatchHookEvent applies the cheap deterministic checks synchronously and starts the
// (possibly slow) Jev condition + agent run in the background so the webhook returns fast.
func (s *Server) dispatchHookEvent(job *storage.RecurringJob, headers http.Header, body []byte) hookDispatchResult {
	res := hookDispatchResult{JobID: job.ID}
	if !job.Enabled || job.TriggerType != jobTriggerWebhook {
		res.Status, res.Reason = "ignored", "hook is disabled or not webhook-triggered"
		return res
	}
	if err := hooks.VerifySignature(headers, body, job.WebhookSigningSecret); err != nil {
		res.Status, res.Reason = "rejected", err.Error()
		return res
	}
	ev := hooks.ParseEvent(headers, body)
	filter := hooks.Filter{Source: job.TriggerSource, Events: job.TriggerEvents, Branches: job.TriggerBranches}
	if ok, reason := filter.Matches(ev); !ok {
		res.Status, res.Reason = "ignored", reason
		return res
	}
	if hookSeenRecently(job.ID, ev.DedupKey(), time.Now()) {
		res.Status, res.Reason = "ignored", "duplicate event"
		return res
	}
	go s.runHookEvent(s.sessionRunParentContext(), job, ev)
	res.Status = "accepted"
	return res
}

func (s *Server) runHookEvent(ctx context.Context, job *storage.RecurringJob, ev hooks.Event) {
	if run, reason := s.evaluateHookCondition(ctx, job, ev); !run {
		if _, err := s.recordSkippedJobExecution(job, reason); err != nil {
			logging.Warn("Failed to record skipped hook execution for %s: %v", job.ID, err)
		}
		return
	}
	prepared, err := s.prepareJobExecution(job, ev.PromptContext())
	if err != nil {
		logging.Error("Failed to start hook %s: %v", job.ID, err)
		return
	}
	s.finishJobExecution(ctx, job, prepared)
}

// evaluateHookCondition asks Jev whether the event satisfies the plain-text condition.
// Like the loop gate it fails open, so a classifier outage never drops a CI failure.
func (s *Server) evaluateHookCondition(ctx context.Context, job *storage.RecurringJob, ev hooks.Event) (bool, string) {
	condition := strings.TrimSpace(job.TriggerCondition)
	if condition == "" {
		return true, ""
	}
	client, _ := s.systemOneClient()
	if client == nil {
		logging.Warn("Hook %s condition skipped: no System One classifier configured", job.ID)
		return true, ""
	}
	state := ev.Summary() + "\n\n" + string(ev.Body)
	if len(state) > jobGateMaxStateChars {
		state = state[:jobGateMaxStateChars]
	}
	gateCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := client.SystemOne(gateCtx, jev.SystemOneRequest{
		State:     state,
		Questions: map[string]jev.Question{"match": {Type: "noul", Instructions: "Does this webhook event satisfy the condition? Condition: " + condition}},
	})
	if err != nil {
		logging.Warn("Hook %s condition check failed, running anyway: %v", job.ID, err)
		return true, ""
	}
	answer, ok := resp.Answers["match"]
	if !ok || answer.Noul == nil || *answer.Noul >= jobGateRunThreshold {
		return true, ""
	}
	return false, fmt.Sprintf("hook: %s event did not match condition (noul=%.2f)", ev.Type, *answer.Noul)
}

// handleWebhookInboundInternalEvent receives provider webhooks relayed by Square.
func (s *Server) handleWebhookInboundInternalEvent(payload json.RawMessage) ([]byte, error) {
	var envelope struct {
		Metadata map[string]interface{} `json:"metadata"`
		HTTP     bruteHTTPProxyRequest  `json:"http"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("failed to decode webhook envelope: %w", err)
	}
	endpointID, _ := envelope.Metadata["webhook_endpoint_id"].(string)
	endpointID = strings.TrimSpace(endpointID)
	body, err := base64.StdEncoding.DecodeString(envelope.HTTP.BodyBase64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode webhook body: %w", err)
	}
	headers := http.Header{}
	for k, values := range envelope.HTTP.Headers {
		for _, v := range values {
			headers.Add(k, v)
		}
	}
	jobsList, err := s.store.ListJobs()
	if err != nil {
		return nil, err
	}
	results := []hookDispatchResult{}
	for _, job := range jobsList {
		if endpointID != "" && job.WebhookEndpointID == endpointID {
			results = append(results, s.dispatchHookEvent(job, headers, body))
		}
	}
	if len(results) == 0 {
		logging.Info("Webhook from Square endpoint %s matched no hook", endpointID)
	}
	return json.Marshal(map[string]interface{}{"hooks": results})
}

// handleTriggerJob lets local scripts (git hooks, CI on the LAN, cron) and the UI
// "send test event" button fire a webhook-triggered loop without Square.
func (s *Server) handleTriggerJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.GetJob(chi.URLParam(r, "jobID"))
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Job not found: "+err.Error())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, hookMaxLocalBodyBytes))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read body: "+err.Error())
		return
	}
	res := s.dispatchHookEvent(job, r.Header, body)
	status := http.StatusAccepted
	if res.Status == "rejected" {
		status = http.StatusUnauthorized
	}
	s.jsonResponse(w, status, res)
}

func newHookSigningSecret() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
