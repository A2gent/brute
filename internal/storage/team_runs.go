package storage

import "time"

const (
	TeamRunStatusRunning = "running"
	TeamRunStatusDone    = "done"
	TeamRunStatusFailed  = "failed"
	TeamRunStatusCapped  = "capped"
)

// TeamRun is a persisted execution header with an immutable policy snapshot.
type TeamRun struct {
	ID         string
	TeamID     string
	SessionID  string
	Status     string
	StopReason string
	PolicyJSON string
	StartedAt  time.Time
	EndedAt    *time.Time
}

// TeamRunMember maps one roster address to its long-lived child session.
type TeamRunMember struct {
	TeamRunID string
	Role      string
	AgentRef  string
	SessionID string
}

// TeamMessage is one append-only mailbox envelope. Thread identity is persisted
// here so no mutable thread table is needed.
type TeamMessage struct {
	ID           string
	TeamRunID    string
	ThreadID     string
	FromRole     string
	ToRoles      []string
	CCRoles      []string
	Kind         string
	Subject      string
	Body         string
	ExpectsReply bool
	CreatedAt    time.Time
	Delivered    map[string]time.Time
}
