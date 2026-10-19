package storage

import (
	"database/sql"
	"time"
)

// SessionWorktree is the git worktree a session works in. Slot is a small per-project
// number (lowest free, unbounded) the project's hooks use to pick non-clashing ports, databases, etc.
type SessionWorktree struct {
	SessionID string
	ProjectID string
	Path      string
	Branch    string
	BaseRef   string
	Slot      int
	CreatedAt time.Time
}

// CreateSessionWorktree stores wt in the lowest free slot of its project and sets wt.Slot.
// WHY no upper bound: parallelism is limited by running sessions (max_parallel_sessions),
// so finished sessions that still own a worktree must never block new ones.
// WHY one INSERT ... SELECT: SQLite serializes writes, so two sessions created at the
// same time can never pick the same slot; the unique (project_id, slot) index backs it up.
func (s *SQLiteStore) CreateSessionWorktree(wt *SessionWorktree) error {
	// The lowest free slot is either 1 or one past a taken slot.
	return s.db.QueryRow(`
		INSERT INTO session_worktrees (session_id, project_id, path, branch, base_ref, slot, created_at)
		SELECT ?, ?, ?, ?, ?, n, ? FROM (
			SELECT 1 AS n UNION ALL SELECT slot + 1 FROM session_worktrees WHERE project_id = ?
		)
		WHERE n NOT IN (SELECT slot FROM session_worktrees WHERE project_id = ?)
		ORDER BY n LIMIT 1
		RETURNING slot
	`, wt.SessionID, wt.ProjectID, wt.Path, wt.Branch, wt.BaseRef, wt.CreatedAt, wt.ProjectID, wt.ProjectID).Scan(&wt.Slot)
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

// ListSessionWorktrees returns every live worktree keyed by session ID, so session lists
// get them in one query instead of one per row.
func (s *SQLiteStore) ListSessionWorktrees() (map[string]*SessionWorktree, error) {
	rows, err := s.db.Query(`SELECT session_id, project_id, path, branch, base_ref, slot, created_at FROM session_worktrees`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]*SessionWorktree)
	for rows.Next() {
		var wt SessionWorktree
		if err := rows.Scan(&wt.SessionID, &wt.ProjectID, &wt.Path, &wt.Branch, &wt.BaseRef, &wt.Slot, &wt.CreatedAt); err != nil {
			return nil, err
		}
		out[wt.SessionID] = &wt
	}
	return out, rows.Err()
}

func (s *SQLiteStore) DeleteSessionWorktree(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM session_worktrees WHERE session_id = ?`, sessionID)
	return err
}
