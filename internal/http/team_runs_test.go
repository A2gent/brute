package http

import (
	"encoding/json"
	stdhttp "net/http"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func TestTeamRunReadAPI(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	defer store.Close()

	started := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	if err := store.SaveTeamRun(&storage.TeamRun{
		ID:         "run-1",
		TeamID:     "squad-1",
		SessionID:  "session-1",
		Status:     storage.TeamRunStatusRunning,
		PolicyJSON: `{"lead":"architect","termination":"lead_done","max_messages":20,"max_minutes":30,"broadcast_allowed":true}`,
		StartedAt:  started,
	}); err != nil {
		t.Fatalf("SaveTeamRun() error = %v", err)
	}
	if err := store.SaveTeamRunMember(&storage.TeamRunMember{
		TeamRunID: "run-1",
		Role:      "architect",
		AgentRef:  "agent-1",
		SessionID: "member-session-1",
	}); err != nil {
		t.Fatalf("SaveTeamRunMember() error = %v", err)
	}
	if err := store.AppendTeamMessage(&storage.TeamMessage{
		ID:        "msg-1",
		TeamRunID: "run-1",
		ThreadID:  "thr-1",
		FromRole:  "architect",
		ToRoles:   []string{"developer"},
		Kind:      "request",
		Subject:   "Add retry",
		Body:      "Please implement retry.",
		CreatedAt: started.Add(time.Minute),
		Delivered: map[string]time.Time{},
	}); err != nil {
		t.Fatalf("AppendTeamMessage() error = %v", err)
	}

	getRun := requestTeamJSON(t, server, stdhttp.MethodGet, "/team-runs/run-1", nil)
	if getRun.Code != stdhttp.StatusOK {
		t.Fatalf("GET /team-runs/run-1 status = %d body = %s", getRun.Code, getRun.Body.String())
	}
	var runPayload map[string]interface{}
	if err := json.Unmarshal(getRun.Body.Bytes(), &runPayload); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if runPayload["team_id"] != "squad-1" || runPayload["session_id"] != "session-1" {
		t.Fatalf("unexpected run payload: %#v", runPayload)
	}

	bySession := requestTeamJSON(t, server, stdhttp.MethodGet, "/sessions/session-1/team-run", nil)
	if bySession.Code != stdhttp.StatusOK {
		t.Fatalf("GET session team-run status = %d body = %s", bySession.Code, bySession.Body.String())
	}

	messages := requestTeamJSON(t, server, stdhttp.MethodGet, "/team-runs/run-1/messages", nil)
	if messages.Code != stdhttp.StatusOK {
		t.Fatalf("GET messages status = %d body = %s", messages.Code, messages.Body.String())
	}
	var messagePayload []map[string]interface{}
	if err := json.Unmarshal(messages.Body.Bytes(), &messagePayload); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(messagePayload) != 1 || messagePayload[0]["from"] != "architect" || messagePayload[0]["id"] != "msg-1" {
		t.Fatalf("unexpected messages payload: %#v", messagePayload)
	}
}
