package storage

import (
	"testing"
	"time"
)

func TestSQLiteTeamRunAndMembersRoundTrip(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	started := time.Now().UTC().Truncate(time.Second)
	run := &TeamRun{
		ID:         "run-1",
		TeamID:     "team-1",
		SessionID:  "parent-1",
		Status:     TeamRunStatusRunning,
		PolicyJSON: `{"max_messages":20}`,
		StartedAt:  started,
	}
	if err := store.SaveTeamRun(run); err != nil {
		t.Fatalf("SaveTeamRun() error = %v", err)
	}
	members := []*TeamRunMember{
		{TeamRunID: run.ID, Role: "developer", AgentRef: "agent-dev", SessionID: "session-dev"},
		{TeamRunID: run.ID, Role: "critic", AgentRef: "agent-critic", SessionID: "session-critic"},
	}
	for _, member := range members {
		if err := store.SaveTeamRunMember(member); err != nil {
			t.Fatalf("SaveTeamRunMember(%s) error = %v", member.Role, err)
		}
	}

	got, err := store.GetTeamRun(run.ID)
	if err != nil {
		t.Fatalf("GetTeamRun() error = %v", err)
	}
	if got.TeamID != run.TeamID || got.SessionID != run.SessionID || got.Status != TeamRunStatusRunning || !got.StartedAt.Equal(started) {
		t.Fatalf("GetTeamRun() = %#v, want %#v", got, run)
	}
	bySession, err := store.GetTeamRunBySession(run.SessionID)
	if err != nil || bySession.ID != run.ID {
		t.Fatalf("GetTeamRunBySession() = %#v, %v", bySession, err)
	}
	listed, err := store.ListTeamRunMembers(run.ID)
	if err != nil {
		t.Fatalf("ListTeamRunMembers() error = %v", err)
	}
	if len(listed) != 2 || listed[0].Role != "critic" || listed[1].Role != "developer" {
		t.Fatalf("ListTeamRunMembers() = %#v", listed)
	}

	ended := started.Add(time.Minute)
	run.Status = TeamRunStatusDone
	run.StopReason = "done"
	run.EndedAt = &ended
	if err := store.SaveTeamRun(run); err != nil {
		t.Fatalf("SaveTeamRun(update) error = %v", err)
	}
	got, err = store.GetTeamRun(run.ID)
	if err != nil || got.StopReason != "done" || got.EndedAt == nil || !got.EndedAt.Equal(ended) {
		t.Fatalf("updated team run = %#v, %v", got, err)
	}
}
