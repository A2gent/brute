package http

import (
	"strings"

	"github.com/A2gent/brute/internal/filesearch"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func (s *Server) resolveSessionFileIndexingEnabled(sess *session.Session) bool {
	if sess == nil || sess.ProjectID == nil {
		return filesearch.IndexingEnabled()
	}
	projectID := strings.TrimSpace(*sess.ProjectID)
	if projectID == "" {
		return filesearch.IndexingEnabled()
	}
	project, err := s.store.GetProject(projectID)
	if err != nil {
		return filesearch.IndexingEnabled()
	}
	return s.resolveProjectFileIndexingEnabled(project)
}

func (s *Server) resolveProjectFileIndexingEnabled(project *storage.Project) bool {
	if project == nil {
		return false
	}
	return filesearch.IsIndexingEnabledForProject(project.Settings, s.globalSettingsOrEmpty())
}

func (s *Server) warmProjectSearchIndex(project *storage.Project, resolvedRoot string) {
	if project == nil || !s.resolveProjectFileIndexingEnabled(project) {
		return
	}
	filesearch.DefaultManager().Warm(resolvedRoot, true)
}

func invalidateProjectSearchIndex(resolvedRoot string) {
	filesearch.DefaultManager().Invalidate(resolvedRoot)
}

func (s *Server) globalSettingsOrEmpty() map[string]string {
	if s == nil {
		return map[string]string{}
	}
	settings, err := s.store.GetSettings()
	if err != nil {
		return map[string]string{}
	}
	return settings
}

func (s *Server) syncProjectSearchIndexAfterSettingsChange(project *storage.Project, previousSettings map[string]string) {
	if s == nil || project == nil {
		return
	}
	resolvedRoot, err := s.resolveProjectRootFolder(project.ID)
	if err != nil {
		return
	}
	globalSettings := s.globalSettingsOrEmpty()
	wasEnabled := filesearch.IsIndexingEnabledForProject(previousSettings, globalSettings)
	isEnabled := filesearch.IsIndexingEnabledForProject(project.Settings, globalSettings)
	if wasEnabled == isEnabled {
		return
	}
	if isEnabled {
		filesearch.DefaultManager().Warm(resolvedRoot, true)
		return
	}
	invalidateProjectSearchIndex(resolvedRoot)
}

func copyProjectSettings(settings map[string]string) map[string]string {
	if len(settings) == 0 {
		return map[string]string{}
	}
	copied := make(map[string]string, len(settings))
	for key, value := range settings {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		copied[trimmedKey] = strings.TrimSpace(value)
	}
	return copied
}
