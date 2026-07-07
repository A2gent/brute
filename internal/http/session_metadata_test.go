package http

import (
	"bytes"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
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
