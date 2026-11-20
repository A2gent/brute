package http

import (
	"errors"
	"strings"

	"github.com/A2gent/brute/internal/logging"
)

// A loop may only join a queue declared in the global concurrency settings, otherwise its
// sessions would wait in a scope with no visible limit.
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
	if _, ok := s.loadConcurrencyConfig().queueLimit(name); !ok {
		return "", errors.New("Unknown queue: " + name)
	}
	return name, nil
}

// detachLoopsFromRemovedQueues clears bindings of loops whose queue was deleted, so the
// loop falls back to its project default queue instead of an unlisted scope.
func (s *Server) detachLoopsFromRemovedQueues() {
	if s.store == nil {
		return
	}
	projects, err := s.store.ListProjects()
	if err != nil {
		logging.Warn("Failed to list projects to detach removed queues: %v", err)
		return
	}
	keep := s.loadConcurrencyConfig().queueNames()
	for _, project := range projects {
		if _, err := s.store.DetachQueuesFromJobs(project.ID, keep); err != nil {
			logging.Warn("Failed to detach loops from removed queues: %v", err)
		}
	}
}
