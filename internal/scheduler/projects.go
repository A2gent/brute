package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func (s *Scheduler) resolveJobWorkDir(job *storage.RecurringJob) string {
	defaultDir := strings.TrimSpace(s.config.WorkDir)
	if defaultDir == "" {
		defaultDir = "."
	}
	if job == nil || job.ProjectID == nil {
		return defaultDir
	}

	projectID := strings.TrimSpace(*job.ProjectID)
	if projectID == "" {
		return defaultDir
	}
	project, err := s.store.GetProject(projectID)
	if err != nil || project == nil || project.Folder == nil {
		if err != nil {
			logging.Warn("Failed to load recurring job project workdir: job=%s project=%s error=%v", job.ID, projectID, err)
		}
		return defaultDir
	}

	candidate := strings.TrimSpace(*project.Folder)
	if candidate == "" {
		return defaultDir
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(defaultDir, candidate)
	}
	candidate = filepath.Clean(candidate)
	info, statErr := os.Stat(candidate)
	if statErr != nil || !info.IsDir() {
		logging.Warn("Skipping invalid recurring job project folder: job=%s folder=%s", job.ID, candidate)
		return defaultDir
	}
	return candidate
}

func (s *Scheduler) assignSessionToJobProject(sess *session.Session, job *storage.RecurringJob) error {
	if sess == nil || job == nil || job.ProjectID == nil {
		return nil
	}
	projectID := strings.TrimSpace(*job.ProjectID)
	if projectID == "" {
		return nil
	}
	if _, err := s.store.GetProject(projectID); err != nil {
		return fmt.Errorf("job project not found: %w", err)
	}
	sess.ProjectID = &projectID
	return s.sessionManager.Save(sess)
}
