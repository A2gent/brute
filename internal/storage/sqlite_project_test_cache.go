package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

// SaveProjectTestCache saves branch-scoped test and coverage results for a project.
func (s *SQLiteStore) SaveProjectTestCache(cache *ProjectTestCache) error {
	if cache == nil {
		return fmt.Errorf("project test cache is required")
	}
	projectID := strings.TrimSpace(cache.ProjectID)
	branch := strings.TrimSpace(cache.Branch)
	scopeHash := strings.TrimSpace(cache.ScopeHash)
	if projectID == "" {
		return fmt.Errorf("project ID is required")
	}
	if branch == "" {
		return fmt.Errorf("branch is required")
	}
	if scopeHash == "" {
		return fmt.Errorf("scope hash is required")
	}

	_, err := s.db.Exec(`
		INSERT INTO project_test_cache (
			project_id,
			repo_path,
			branch,
			base_branch,
			scope_hash,
			test_response,
			coverage_response,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, repo_path, branch, base_branch, scope_hash) DO UPDATE SET
			test_response = excluded.test_response,
			coverage_response = excluded.coverage_response,
			updated_at = excluded.updated_at
	`, projectID,
		normalizeProjectTestCacheRepoPath(cache.RepoPath),
		branch,
		strings.TrimSpace(cache.BaseBranch),
		scopeHash,
		cache.TestResponseJSON,
		cache.CoverageResponseJSON,
		cache.CreatedAt,
		cache.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to save project test cache: %w", err)
	}

	return nil
}

// GetProjectTestCache retrieves branch-scoped cached test and coverage results.
func (s *SQLiteStore) GetProjectTestCache(projectID string, repoPath string, branch string, baseBranch string, scopeHash string) (*ProjectTestCache, error) {
	var cache ProjectTestCache
	err := s.db.QueryRow(`
		SELECT project_id, repo_path, branch, base_branch, scope_hash, test_response, coverage_response, created_at, updated_at
		FROM project_test_cache
		WHERE project_id = ? AND repo_path = ? AND branch = ? AND base_branch = ? AND scope_hash = ?
	`, strings.TrimSpace(projectID),
		normalizeProjectTestCacheRepoPath(repoPath),
		strings.TrimSpace(branch),
		strings.TrimSpace(baseBranch),
		strings.TrimSpace(scopeHash),
	).Scan(
		&cache.ProjectID,
		&cache.RepoPath,
		&cache.Branch,
		&cache.BaseBranch,
		&cache.ScopeHash,
		&cache.TestResponseJSON,
		&cache.CoverageResponseJSON,
		&cache.CreatedAt,
		&cache.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get project test cache: %w", err)
	}

	return &cache, nil
}

func normalizeProjectTestCacheRepoPath(repoPath string) string {
	trimmed := strings.TrimSpace(repoPath)
	if trimmed == "." {
		return ""
	}
	return strings.Trim(strings.ReplaceAll(trimmed, "\\", "/"), "/")
}
