package http

import (
	"bytes"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
)

func TestApplyLeadingSessionQueueDirective(t *testing.T) {
	req := CreateSessionRequest{
		Task: "  -q fix focused state",
	}

	applyLeadingSessionQueueDirective(&req)

	if req.Task != "fix focused state" {
		t.Fatalf("task = %q, want %q", req.Task, "fix focused state")
	}
	if !req.Queued {
		t.Fatalf("queued = false, want true")
	}
	if req.QueueMode != sessionQueueModeSerial {
		t.Fatalf("queue mode = %q, want %q", req.QueueMode, sessionQueueModeSerial)
	}
}

func TestCreateSessionKeepsEmptyLMStudioModel(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	server.config.ActiveProvider = string(config.ProviderKimi)
	server.config.DefaultModel = "kimi-k2.5"

	body, err := json.Marshal(CreateSessionRequest{
		AgentID:  "build",
		Provider: string(config.ProviderLMStudio),
		Model:    "",
	})
	if err != nil {
		t.Fatalf("failed to encode request: %v", err)
	}
	req := httptest.NewRequest(stdhttp.MethodPost, "/sessions", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	server.handleCreateSession(rec, req)

	if rec.Code != stdhttp.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp CreateSessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Provider != string(config.ProviderLMStudio) {
		t.Fatalf("provider = %q, want %q", resp.Provider, config.ProviderLMStudio)
	}
	if resp.Model != "" {
		t.Fatalf("model = %q, want empty", resp.Model)
	}
}

func TestCreateSessionNormalizesLegacyOpenAICodexReasoningSuffix(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	body, err := json.Marshal(CreateSessionRequest{
		AgentID:  "build",
		Provider: string(config.ProviderOpenAICodex),
		Model:    "gpt-5.6-sol-medium",
	})
	if err != nil {
		t.Fatalf("failed to encode request: %v", err)
	}
	req := httptest.NewRequest(stdhttp.MethodPost, "/sessions", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	server.handleCreateSession(rec, req)

	if rec.Code != stdhttp.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp CreateSessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got, want := resp.Model, "gpt-5.6-sol"; got != want {
		t.Fatalf("model = %q, want %q", got, want)
	}
}

func TestResolveSessionModelNormalizesLegacyOpenAICodexReasoningSuffix(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess := session.New("build")
	sess.Metadata["provider"] = string(config.ProviderOpenAICodex)
	sess.Metadata["model"] = "gpt-5.6-terra-high"

	if got, want := server.resolveSessionModel(sess, config.ProviderOpenAICodex), "gpt-5.6-terra"; got != want {
		t.Fatalf("model = %q, want %q", got, want)
	}
}

func TestCreateSessionRespectsActiveProviderWhenAutomaticRouterIsConfigured(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	server.config.ActiveProvider = string(config.ProviderCursor)
	server.config.Providers[string(config.ProviderCursor)] = config.Provider{
		Name:  string(config.ProviderCursor),
		Model: "composer-2.5",
	}
	server.config.Providers[string(config.ProviderAutoRouter)] = config.Provider{
		Name:           string(config.ProviderAutoRouter),
		RouterProvider: string(config.ProviderLMStudio),
		RouterRules: []config.RouterRule{
			{Match: "coding", Provider: string(config.ProviderLMStudio)},
		},
	}

	body, err := json.Marshal(CreateSessionRequest{AgentID: "build", Queued: true})
	if err != nil {
		t.Fatalf("failed to encode request: %v", err)
	}
	req := httptest.NewRequest(stdhttp.MethodPost, "/sessions", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	server.handleCreateSession(rec, req)

	if rec.Code != stdhttp.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp CreateSessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Provider != string(config.ProviderCursor) || resp.Model != "composer-2.5" {
		t.Fatalf("provider/model = %q/%q, want cursor/composer-2.5", resp.Provider, resp.Model)
	}
}

func TestStripLeadingSessionQueueDirective(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "dash flag", raw: "-q build wishlist flow", want: "build wishlist flow", ok: true},
		{name: "long flag", raw: "--queue build wishlist flow", want: "build wishlist flow", ok: true},
		{name: "slash directive", raw: "/queue build wishlist flow", want: "build wishlist flow", ok: true},
		{name: "slash short directive", raw: "/q build wishlist flow", want: "build wishlist flow", ok: true},
		{name: "colon separator", raw: "/queue: build wishlist flow", want: "build wishlist flow", ok: true},
		{name: "mode selection", raw: "/queue serial", ok: false},
		{name: "empty directive", raw: "-q", ok: false},
		{name: "normal prompt", raw: "fix -q handling", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := stripLeadingSessionQueueDirective(tt.raw)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("prompt = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionRunDurationSecondsUsesUpdatedAtForCompletedSessions(t *testing.T) {
	createdAt := time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(90 * time.Second)

	if got, want := sessionRunDurationSeconds(createdAt, updatedAt, "completed"), int64(90); got != want {
		t.Fatalf("duration = %d, want %d", got, want)
	}
}

func TestSetSessionRoutedProviderAndModelPersistsRouterDecision(t *testing.T) {
	sess := session.New("build")

	changed := setSessionRoutedProviderAndModel(
		sess,
		config.ProviderAutoRouter,
		config.ProviderCursor,
		"composer-2.5",
		"coding, mid complexity",
		"The primary action is editing and testing source code.",
	)
	if !changed {
		t.Fatal("expected routing metadata to change")
	}

	provider, model := sessionRoutedProviderAndModel(sess)
	if provider != "cursor" || model != "composer-2.5" {
		t.Fatalf("routed target = %q/%q, want cursor/composer-2.5", provider, model)
	}
	rule, reason := sessionRoutingRuleAndReason(sess)
	if rule != "coding, mid complexity" {
		t.Fatalf("routed rule = %q", rule)
	}
	if reason != "The primary action is editing and testing source code." {
		t.Fatalf("routed reason = %q", reason)
	}
}

func TestSetSessionRoutedProviderAndModelClearsDecisionForDirectProvider(t *testing.T) {
	sess := session.New("build")
	setSessionRoutedProviderAndModel(sess, config.ProviderAutoRouter, config.ProviderCursor, "composer-2.5", "coding", "source edits")

	changed := setSessionRoutedProviderAndModel(sess, config.ProviderCursor, config.ProviderCursor, "composer-2.5", "", "")
	if !changed {
		t.Fatal("expected direct provider to clear automatic-router metadata")
	}
	for _, key := range []string{"routed_provider", "routed_model", "routed_rule", "routed_reason"} {
		if _, exists := sess.Metadata[key]; exists {
			t.Fatalf("metadata %q was not cleared", key)
		}
	}
}

func TestApplyProviderTraceToSessionPersistsFallbackActiveNode(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess := session.New("build")

	server.applyProviderTraceToSession(sess, config.ProviderFallback, &agent.ProviderTraceEvent{
		Phase:      "provider_selected",
		Provider:   "kimi",
		Model:      "kimi-k2.5",
		NodeIndex:  0,
		TotalNodes: 2,
	})

	provider, model := sessionFallbackActiveProviderAndModel(sess)
	if provider != "kimi" || model != "kimi-k2.5" {
		t.Fatalf("fallback active node after provider_selected = %q/%q, want kimi/kimi-k2.5", provider, model)
	}

	server.applyProviderTraceToSession(sess, config.ProviderFallback, &agent.ProviderTraceEvent{
		Phase:      "completed",
		Provider:   "openai",
		Model:      "gpt-5.5",
		NodeIndex:  1,
		TotalNodes: 2,
	})

	provider, model = sessionFallbackActiveProviderAndModel(sess)
	if provider != "openai" || model != "gpt-5.5" {
		t.Fatalf("fallback active node after completed = %q/%q, want openai/gpt-5.5", provider, model)
	}
}

// The automatic router resolves a rule targeting a fallback chain into an aggregate ref
// (fallback:<id>), so the active node must still be tracked for that target provider.
func TestApplyProviderTraceToSessionPersistsFallbackActiveNodeForAggregateRef(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess := session.New("build")
	sess.Metadata["provider"] = string(config.ProviderAutoRouter)

	server.applyProviderTraceToSession(sess, config.ProviderType(config.FallbackAggregateRefFromID("coding")), &agent.ProviderTraceEvent{
		Phase:      "provider_selected",
		Provider:   "cursor",
		Model:      "composer-2.5",
		NodeIndex:  0,
		TotalNodes: 2,
	})

	provider, model := sessionFallbackActiveProviderAndModel(sess)
	if provider != "cursor" || model != "composer-2.5" {
		t.Fatalf("fallback active node = %q/%q, want cursor/composer-2.5", provider, model)
	}
}

func TestApplyProviderTraceToSessionIgnoresNonFallbackTargetProvider(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess := session.New("build")

	server.applyProviderTraceToSession(sess, config.ProviderKimi, &agent.ProviderTraceEvent{
		Phase:    "provider_selected",
		Provider: "kimi",
		Model:    "kimi-k2.5",
	})
	server.applyProviderTraceToSession(sess, config.ProviderKimi, &agent.ProviderTraceEvent{
		Phase:    "completed",
		Provider: "kimi",
		Model:    "kimi-k2.5",
	})

	for _, key := range []string{fallbackActiveProviderMetadataKey, fallbackActiveModelMetadataKey} {
		if _, exists := sess.Metadata[key]; exists {
			t.Fatalf("metadata %q must not be set for non-fallback provider", key)
		}
	}
}

func TestSessionToResponseIncludesFallbackActiveNode(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)
	sess := session.New("build")
	sess.Metadata["provider"] = string(config.ProviderFallback)
	sess.Metadata[fallbackActiveProviderMetadataKey] = "kimi"
	sess.Metadata[fallbackActiveModelMetadataKey] = "kimi-k2.5"

	resp := server.sessionToResponse(sess)
	if resp.FallbackActiveProvider != "kimi" || resp.FallbackActiveModel != "kimi-k2.5" {
		t.Fatalf("fallback active node = %q/%q, want kimi/kimi-k2.5", resp.FallbackActiveProvider, resp.FallbackActiveModel)
	}

	snapshot := server.sessionSnapshotStreamEvent(sess)
	if snapshot.FallbackActiveProvider != "kimi" || snapshot.FallbackActiveModel != "kimi-k2.5" {
		t.Fatalf("snapshot fallback active node = %q/%q, want kimi/kimi-k2.5", snapshot.FallbackActiveProvider, snapshot.FallbackActiveModel)
	}
}
