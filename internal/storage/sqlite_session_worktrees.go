package storage

import (
	"database/sql"
	"errors"
	"time"
)

// SessionWorktree is the git worktree a session works in. Slot is a small per-project
// number (1..max) the project's hooks use to pick non-clashing ports, databases, etc.
type SessionWorktree struct {
	SessionID string
	ProjectID string
	Path      string
	Branch    string
	BaseRef   string
	Slot      int
	CreatedAt time.Time
}

var ErrNoFreeWorktreeSlot = errors.New("all worktree slots of the project are taken")

// CreateSessionWorktree stores wt in the lowest free slot of its project and sets wt.Slot.
// WHY one INSERT ... SELECT: SQLite serializes writes, so two sessions created at the
// same time can never pick the same slot; the unique (project_id, slot) index backs it up.
func (s *SQLiteStore) CreateSessionWorktree(wt *SessionWorktree, maxSlots int) error {
	err := s.db.QueryRow(`
		WITH RECURSIVE slots(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM slots WHERE n < ?)
		INSERT INTO session_worktrees (session_id, project_id, path, branch, base_ref, slot, created_at)
		SELECT ?, ?, ?, ?, ?, n, ? FROM slots
		WHERE n NOT IN (SELECT slot FROM session_worktrees WHERE project_id = ?)
		ORDER BY n LIMIT 1
		RETURNING slot
	`, maxSlots, wt.SessionID, wt.ProjectID, wt.Path, wt.Branch, wt.BaseRef, wt.CreatedAt, wt.ProjectID).Scan(&wt.Slot)
	if err == sql.ErrNoRows {
		return ErrNoFreeWorktreeSlot
	}
	return err
}

// GetSessionWorktree returns nil without error when the session has no worktree.
func (s *SQLiteStore) GetSessionWorktree(sessionID string) (*SessionWorktree, error) {
	var wt SessionWorktree
	err := s.db.QueryRow(`
		SELECT session_id, project_id, path, branch, base_ref, slot, created_at
		FROM session_worktrees WHERE session_id = ?
	`, sessionID).Scan(&wt.SessionID, &wt.ProjectID, &wt.Path, &wt.Branch, &wt.BaseRef, &wt.Slot, &wt.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &wt, nil
}

func (s *SQLiteStore) DeleteSessionWorktree(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM session_worktrees WHERE session_id = ?`, sessionID)
	return err
}
