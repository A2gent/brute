package http

import (
	"strings"

	"github.com/A2gent/brute/internal/storage"
)

// Publish only after persistence succeeds. Empty project IDs invalidate global
// resources; moves invalidate both projects so the old view drops the resource.
func (s *Server) publishProjectResourcesChanged(resource string, projectIDs ...string) {
	seen := make(map[string]bool)
	for _, projectID := range projectIDs {
		projectID = strings.TrimSpace(projectID)
		if !seen[projectID] {
			seen[projectID] = true
			s.publishSessionCatalogEvent(SessionCatalogEvent{
				Type: "project_resources_changed", Resource: resource, ProjectID: projectID,
			})
		}
	}
}

func resourceProjectID(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

// These shared write boundaries cover HTTP handlers and agent tools alike.
func (s *Server) saveJob(job *storage.RecurringJob) error {
	previous, _ := s.store.GetJob(job.ID)
	if err := s.store.SaveJob(job); err != nil {
		return err
	}
	ids := []string{resourceProjectID(job.ProjectID)}
	if previous != nil {
		ids = append(ids, resourceProjectID(previous.ProjectID))
	}
	s.publishProjectResourcesChanged("jobs", ids...)
	return nil
}

func (s *Server) deleteJob(id string) error {
	job, _ := s.store.GetJob(id)
	if err := s.store.DeleteJob(id); err != nil {
		return err
	}
	projectID := ""
	if job != nil {
		projectID = resourceProjectID(job.ProjectID)
	}
	s.publishProjectResourcesChanged("jobs", projectID)
	return nil
}

func (s *Server) saveJobExecution(exec *storage.JobExecution) error {
	if err := s.store.SaveJobExecution(exec); err != nil {
		return err
	}
	// Scheduler starts, failures and skipped runs have no HTTP mutation. Publish
	// here rather than polling, including runs which never create a session.
	if job, err := s.store.GetJob(exec.JobID); err == nil {
		s.publishProjectResourcesChanged("jobs", resourceProjectID(job.ProjectID))
	}
	return nil
}

func (s *Server) saveMCPServer(server *storage.MCPServer) error {
	previous, _ := s.store.GetMCPServer(server.ID)
	if err := s.store.SaveMCPServer(server); err != nil {
		return err
	}
	ids := []string{mcpServerProjectID(server)}
	if previous != nil {
		ids = append(ids, mcpServerProjectID(previous))
	}
	s.publishProjectResourcesChanged("mcp", ids...)
	return nil
}

func (s *Server) deleteMCPServer(id string) error {
	server, _ := s.store.GetMCPServer(id)
	if err := s.store.DeleteMCPServer(id); err != nil {
		return err
	}
	s.publishProjectResourcesChanged("mcp", mcpServerProjectID(server))
	return nil
}

// NotifyJobChanged bridges scheduler-owned rescheduling writes to the catalog.
func (s *Server) NotifyJobChanged(job *storage.RecurringJob) {
	if job != nil {
		s.publishProjectResourcesChanged("jobs", resourceProjectID(job.ProjectID))
	}
}
