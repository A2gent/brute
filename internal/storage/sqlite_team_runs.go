package storage

import (
	"database/sql"
	"errors"
	"fmt"
)

var ErrTeamRunNotFound = errors.New("team run not found")

func (s *SQLiteStore) SaveTeamRun(run *TeamRun) error {
	if run == nil {
		return fmt.Errorf("team run is required")
	}
	_, err := s.db.Exec(`
		INSERT INTO team_runs (id, team_id, session_id, status, stop_reason, policy_json, started_at, ended_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			stop_reason = excluded.stop_reason,
			ended_at = excluded.ended_at
	`, run.ID, run.TeamID, run.SessionID, run.Status, run.StopReason, run.PolicyJSON, run.StartedAt, run.EndedAt)
	if err != nil {
		return fmt.Errorf("failed to save team run: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetTeamRun(id string) (*TeamRun, error) {
	return s.scanTeamRun(s.db.QueryRow(`
		SELECT id, team_id, session_id, status, stop_reason, policy_json, started_at, ended_at
		FROM team_runs WHERE id = ?
	`, id), id)
}

func (s *SQLiteStore) GetTeamRunBySession(sessionID string) (*TeamRun, error) {
	return s.scanTeamRun(s.db.QueryRow(`
		SELECT id, team_id, session_id, status, stop_reason, policy_json, started_at, ended_at
		FROM team_runs WHERE session_id = ?
	`, sessionID), "session "+sessionID)
}

type teamRunScanner interface {
	Scan(dest ...interface{}) error
}

func (s *SQLiteStore) scanTeamRun(row teamRunScanner, label string) (*TeamRun, error) {
	var run TeamRun
	var endedAt sql.NullTime
	err := row.Scan(&run.ID, &run.TeamID, &run.SessionID, &run.Status, &run.StopReason, &run.PolicyJSON, &run.StartedAt, &endedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrTeamRunNotFound, label)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get team run: %w", err)
	}
	if endedAt.Valid {
		run.EndedAt = &endedAt.Time
	}
	return &run, nil
}

func (s *SQLiteStore) SaveTeamRunMember(member *TeamRunMember) error {
	if member == nil {
		return fmt.Errorf("team run member is required")
	}
	_, err := s.db.Exec(`
		INSERT INTO team_run_members (team_run_id, role, agent_ref, session_id)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(team_run_id, role) DO UPDATE SET
			agent_ref = excluded.agent_ref,
			session_id = excluded.session_id
	`, member.TeamRunID, member.Role, member.AgentRef, member.SessionID)
	if err != nil {
		return fmt.Errorf("failed to save team run member: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ListTeamRunMembers(runID string) ([]*TeamRunMember, error) {
	rows, err := s.db.Query(`
		SELECT team_run_id, role, agent_ref, session_id
		FROM team_run_members WHERE team_run_id = ? ORDER BY role COLLATE NOCASE ASC
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to list team run members: %w", err)
	}
	defer rows.Close()
	members := []*TeamRunMember{}
	for rows.Next() {
		var member TeamRunMember
		if err := rows.Scan(&member.TeamRunID, &member.Role, &member.AgentRef, &member.SessionID); err != nil {
			return nil, err
		}
		members = append(members, &member)
	}
	return members, rows.Err()
}
