package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

// SaveProjectGitReviewOverlayCache stores generated review overlay notes for one changed file.
func (s *SQLiteStore) SaveProjectGitReviewOverlayCache(cache *ProjectGitReviewOverlayCache) error {
	if cache == nil {
		return fmt.Errorf("project git review overlay cache is required")
	}
	projectID := strings.TrimSpace(cache.ProjectID)
	branch := strings.TrimSpace(cache.Branch)
	filePath := normalizeProjectGitReviewOverlayCacheFilePath(cache.FilePath)
	diffHash := strings.TrimSpace(cache.DiffHash)
	if projectID == "" {
		return fmt.Errorf("project ID is required")
	}
	if branch == "" {
		return fmt.Errorf("branch is required")
	}
	if filePath == "" {
		return fmt.Errorf("file path is required")
	}
	if diffHash == "" {
		return fmt.Errorf("diff hash is required")
	}

	_, err := s.db.Exec(`
		INSERT INTO project_git_review_overlay_cache (
			project_id,
			repo_path,
			branch,
			base_branch,
			file_path,
			diff_hash,
			annotations_json,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, repo_path, branch, base_branch, file_path) DO UPDATE SET
			diff_hash = excluded.diff_hash,
			annotations_json = excluded.annotations_json,
			updated_at = excluded.updated_at
	`, projectID,
		normalizeProjectGitReviewOverlayCacheRepoPath(cache.RepoPath),
		branch,
		strings.TrimSpace(cache.BaseBranch),
		filePath,
		diffHash,
		cache.AnnotationsJSON,
		cache.CreatedAt,
		cache.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to save project git review overlay cache: %w", err)
	}

	return nil
}

// ListProjectGitReviewOverlayCache retrieves all cached overlay notes for one branch comparison.
func (s *SQLiteStore) ListProjectGitReviewOverlayCache(projectID string, repoPath string, branch string, baseBranch string) ([]*ProjectGitReviewOverlayCache, error) {
	rows, err := s.db.Query(`
		SELECT project_id, repo_path, branch, base_branch, file_path, diff_hash, annotations_json, created_at, updated_at
		FROM project_git_review_overlay_cache
		WHERE project_id = ? AND repo_path = ? AND branch = ? AND base_branch = ?
		ORDER BY file_path ASC
	`, strings.TrimSpace(projectID),
		normalizeProjectGitReviewOverlayCacheRepoPath(repoPath),
		strings.TrimSpace(branch),
		strings.TrimSpace(baseBranch),
	)
	if err == sql.ErrNoRows {
		return []*ProjectGitReviewOverlayCache{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list project git review overlay cache: %w", err)
	}
	defer rows.Close()

	items := []*ProjectGitReviewOverlayCache{}
	for rows.Next() {
		var cache ProjectGitReviewOverlayCache
		if err := rows.Scan(
			&cache.ProjectID,
			&cache.RepoPath,
			&cache.Branch,
			&cache.BaseBranch,
			&cache.FilePath,
			&cache.DiffHash,
			&cache.AnnotationsJSON,
			&cache.CreatedAt,
			&cache.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan project git review overlay cache: %w", err)
		}
		items = append(items, &cache)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate project git review overlay cache: %w", err)
	}

	return items, nil
}

func normalizeProjectGitReviewOverlayCacheRepoPath(repoPath string) string {
	trimmed := strings.TrimSpace(repoPath)
	if trimmed == "." {
		return ""
	}
	return strings.Trim(strings.ReplaceAll(trimmed, "\\", "/"), "/")
}

func normalizeProjectGitReviewOverlayCacheFilePath(filePath string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(filePath, "\\", "/"))
	trimmed = strings.TrimPrefix(trimmed, "a/")
	trimmed = strings.TrimPrefix(trimmed, "b/")
	return strings.Trim(trimmed, "/")
}
