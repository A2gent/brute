package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

// SaveProjectPRDescription saves the editable PR description for one project branch comparison.
func (s *SQLiteStore) SaveProjectPRDescription(description *ProjectPRDescription) error {
	if description == nil {
		return fmt.Errorf("project PR description is required")
	}
	projectID := strings.TrimSpace(description.ProjectID)
	branch := strings.TrimSpace(description.Branch)
	baseBranch := strings.TrimSpace(description.BaseBranch)
	if projectID == "" {
		return fmt.Errorf("project ID is required")
	}
	if branch == "" {
		return fmt.Errorf("branch is required")
	}
	if baseBranch == "" {
		return fmt.Errorf("base branch is required")
	}

	_, err := s.db.Exec(`
		INSERT INTO project_pr_descriptions (project_id, repo_path, branch, base_branch, content, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, repo_path, branch, base_branch) DO UPDATE SET
			content = excluded.content,
			updated_at = excluded.updated_at
	`, projectID, normalizeProjectPRDescriptionRepoPath(description.RepoPath), branch, baseBranch, description.Content, description.CreatedAt, description.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to save project PR description: %w", err)
	}

	return nil
}

// GetProjectPRDescription retrieves the saved PR description for one project branch comparison.
func (s *SQLiteStore) GetProjectPRDescription(projectID string, repoPath string, branch string, baseBranch string) (*ProjectPRDescription, error) {
	var description ProjectPRDescription
	err := s.db.QueryRow(`
		SELECT project_id, repo_path, branch, base_branch, content, created_at, updated_at
		FROM project_pr_descriptions
		WHERE project_id = ? AND repo_path = ? AND branch = ? AND base_branch = ?
	`, strings.TrimSpace(projectID), normalizeProjectPRDescriptionRepoPath(repoPath), strings.TrimSpace(branch), strings.TrimSpace(baseBranch)).Scan(
		&description.ProjectID,
		&description.RepoPath,
		&description.Branch,
		&description.BaseBranch,
		&description.Content,
		&description.CreatedAt,
		&description.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get project PR description: %w", err)
	}

	return &description, nil
}

func normalizeProjectPRDescriptionRepoPath(repoPath string) string {
	trimmed := strings.TrimSpace(repoPath)
	if trimmed == "." {
		return ""
	}
	return strings.Trim(strings.ReplaceAll(trimmed, "\\", "/"), "/")
}
