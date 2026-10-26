package http

import (
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/agentdef"
)

// Raw container aliases must not bypass the configured-ID project gate. Check
// both the authoritative definition and the workspace actually mounted by this
// runtime, since a global definition can have a warm instance in another project.
func (s *Server) authorizeDockerDelegationTarget(agent *LocalDockerAgent, projectID string) error {
	id := strings.TrimSpace(agent.Labels[dockerRuntimeAgentDefLabelKey])
	if id == "" {
		return fmt.Errorf("container %q has no configured definition; use a configured agent ID for delegation", agent.Name)
	}
	def, scope, err := s.definitionForUnifiedAgent(id, projectID)
	if err != nil || !agentVisibleInProjectSession(scope, projectID) {
		return fmt.Errorf("agent %q is not available in this project session", id)
	}
	if def.Runtime.Type != agentdef.RuntimeDocker {
		return fmt.Errorf("container %q does not match the configured runtime for agent %q", agent.Name, id)
	}
	binding, err := s.resolveDockerWorkspaceBinding(def, projectID)
	if err != nil {
		return err
	}
	// Multi-project runtimes share an empty project label with no-workspace
	// agents. A raw alias cannot prove its mounts still match the definition.
	if len(binding.ProjectMounts) > 0 || strings.TrimSpace(agent.Labels["a2gent.workspace_scope"]) != "" {
		return fmt.Errorf("multi-project container %q requires configured agent ID %q", agent.Name, id)
	}
	if strings.TrimSpace(agent.Labels["a2gent.project_id"]) != binding.ProjectID {
		return fmt.Errorf("container %q belongs to another project workspace; use configured agent ID %q", agent.Name, id)
	}
	return nil
}
