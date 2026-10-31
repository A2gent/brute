package http

import (
	"errors"
	"strings"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/storage"
)

// A loop may only join a queue the project actually declares, otherwise its sessions
// would wait in a scope with no visible limit in settings.
func (s *Server) validateJobQueueName(projectID *string, raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", nil
	}
	if _, err := validateSessionQueueName(name); err != nil {
		return "", err
	}
	if projectID == nil || strings.TrimSpace(*projectID) == "" {
		return "", errors.New("A queue can only be set for a project loop")
	}
	project, err := s.store.GetProject(*projectID)
	if err != nil || project == nil {
		return "", errors.New("Project not found")
	}
	if _, ok := sessionQueueLimit(project.Settings, name); !ok {
		return "", errors.New("Unknown queue: " + name)
	}
	return name, nil
}

func sessionQueueNames(project *storage.Project) []string {
	if project == nil {
		return nil
	}
	queues, err := parseSessionQueues(project.Settings[projectSessionQueuesKey])
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(queues))
	for _, queue := range queues {
		names = append(names, queue.Name)
	}
	return names
}

// detachLoopsFromRemovedQueues clears bindings of loops whose queue was deleted, so the
// loop falls back to the project default queue instead of an unlisted scope.
func (s *Server) detachLoopsFromRemovedQueues(project *storage.Project) {
	if project == nil || s.store == nil {
		return
	}
	if _, err := s.store.DetachQueuesFromJobs(project.ID, sessionQueueNames(project)); err != nil {
		logging.Warn("Failed to detach loops from removed queues: %v", err)
	}
}
