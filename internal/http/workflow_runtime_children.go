package http

import (
	"fmt"

	"strings"

	"github.com/A2gent/brute/internal/config"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func (s *Server) syncWorkflowChildSessionStatus(childSessionID string, nodeStatus string) {
	childSessionID = strings.TrimSpace(childSessionID)
	if childSessionID == "" {
		return
	}
	child, err := s.sessionManager.Get(childSessionID)
	if err != nil || child == nil {
		return
	}
	next := session.StatusCompleted
	switch strings.ToLower(strings.TrimSpace(nodeStatus)) {
	case "failed":
		next = session.StatusFailed
	case "blocked", "in_progress", "running":
		next = session.StatusPaused
	}
	if child.Status == next {
		return
	}
	child.SetStatus(next)
	_ = s.sessionManager.Save(child)
}

func (s *Server) workflowNodeChildSession(
	parent *session.Session,
	def *workflowDefinitionRuntime,
	node workflowNodeRuntime,
	st *workflowRuntimeNodeState,
) (*session.Session, error) {
	if st != nil {
		childSessionID := strings.TrimSpace(st.ChildSessionID)
		if childSessionID != "" {
			child, err := s.sessionManager.Get(childSessionID)
			if err == nil && child != nil {
				return child, nil
			}
			logging.Warn("Workflow node %s child session %s could not be loaded; creating replacement: %v", node.ID, childSessionID, err)
		}
	}
	return s.createWorkflowNodeChildSession(parent, def, node)
}

func (s *Server) buildSystemPromptForWorkflowNode(child *session.Session, node workflowNodeRuntime) string {
	if strings.EqualFold(strings.TrimSpace(node.Kind), "subagent") {
		if sa, err := s.resolveWorkflowSubAgent(node); err == nil && sa != nil {
			if snapshot := s.composeSubAgentSystemPromptSnapshot(sa, child); snapshot != nil && strings.TrimSpace(snapshot.CombinedPrompt) != "" {
				attachSessionSystemPromptSnapshot(child, snapshot)
				if saveErr := s.sessionManager.Save(child); saveErr != nil {
					return strings.TrimSpace(snapshot.CombinedPrompt)
				}
				return strings.TrimSpace(snapshot.CombinedPrompt)
			}
		}
	}
	return s.buildSystemPromptForSession(child)
}

func (s *Server) toolManagerForWorkflowNode(child *session.Session, node workflowNodeRuntime) *tools.Manager {
	manager := s.toolManagerForSession(child)
	if manager == nil {
		return nil
	}
	kind := strings.ToLower(strings.TrimSpace(node.Kind))
	if kind != "main" {
		return manager
	}

	// Workflow main nodes are orchestrators inside an already-defined graph.
	// Let the workflow runtime launch downstream nodes instead of allowing
	// recursive delegation that blocks parent tool completion.
	scoped := manager.Clone()
	scoped.Unregister("delegate_to_subagent")
	scoped.Unregister("delegate_to_agent")
	return scoped
}

func (s *Server) createWorkflowNodeChildSession(
	parent *session.Session,
	def *workflowDefinitionRuntime,
	node workflowNodeRuntime,
) (*session.Session, error) {
	child, err := s.sessionManager.CreateWithParent(parent.AgentID, parent.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to create child session: %w", err)
	}
	if parent.ProjectID != nil {
		projectID := strings.TrimSpace(*parent.ProjectID)
		if projectID != "" {
			child.ProjectID = &projectID
		}
	}
	if child.Metadata == nil {
		child.Metadata = make(map[string]interface{})
	}
	child.Metadata["workflow_child"] = true
	child.Metadata["workflow_parent_id"] = parent.ID
	child.Metadata["workflow_node_id"] = node.ID
	child.Metadata["workflow_node_label"] = node.Label
	child.Metadata["workflow_name"] = def.Name

	if err := s.applyNodeRoutingMetadata(child, parent, node); err != nil {
		return child, err
	}
	if err := s.sessionManager.Save(child); err != nil {
		return child, fmt.Errorf("failed to save child session: %w", err)
	}
	return child, nil
}

func (s *Server) applyNodeRoutingMetadata(child *session.Session, parent *session.Session, node workflowNodeRuntime) error {
	parentProvider, parentModel := sessionProviderAndModel(parent)
	if parentProvider == "" {
		parentProvider = string(s.resolveSessionProviderType(parent))
	}
	if parentModel == "" {
		parentModel = s.resolveSessionModel(parent, config.ProviderType(parentProvider))
	}
	child.Metadata["provider"] = parentProvider
	if parentModel != "" {
		child.Metadata["model"] = parentModel
	}

	if strings.EqualFold(node.Kind, "subagent") {
		sa, err := s.resolveWorkflowSubAgent(node)
		if err != nil {
			return err
		}
		child.Metadata["sub_agent_id"] = sa.ID
		child.Metadata["sub_agent_name"] = sa.Name
		if strings.TrimSpace(sa.Provider) != "" {
			child.Metadata["provider"] = strings.TrimSpace(sa.Provider)
		}
		if strings.TrimSpace(sa.Model) != "" {
			child.Metadata["model"] = strings.TrimSpace(sa.Model)
		}
		if child.ProjectID == nil && sa.ProjectID != nil {
			projectID := strings.TrimSpace(*sa.ProjectID)
			if projectID != "" {
				child.ProjectID = &projectID
			}
		}
	}
	return nil
}

func (s *Server) resolveWorkflowSubAgent(node workflowNodeRuntime) (*storage.SubAgent, error) {
	idCandidates := []string{
		strings.TrimSpace(node.SubAgentID),
		strings.TrimSpace(node.Ref),
	}
	for _, candidate := range idCandidates {
		if candidate == "" {
			continue
		}
		if sa, err := s.store.GetSubAgent(candidate); err == nil && sa != nil {
			return sa, nil
		}
	}
	search := strings.ToLower(strings.TrimSpace(node.Label))
	if search == "" {
		search = strings.ToLower(strings.TrimSpace(node.Ref))
	}
	if search == "" {
		return nil, fmt.Errorf("sub-agent is missing for node %q", node.ID)
	}
	all, err := s.store.ListSubAgents()
	if err != nil {
		return nil, fmt.Errorf("failed to list sub-agents: %w", err)
	}
	for _, sa := range all {
		if strings.ToLower(strings.TrimSpace(sa.Name)) == search {
			return sa, nil
		}
	}
	return nil, fmt.Errorf("sub-agent not found for node %q", node.ID)
}
