package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/storage"
)

func TestDelegateToAgentRejectsCrossProjectContainerAliases(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	projectID, otherID := "project-a", "project-b"
	now := time.Now()
	if err := store.SaveAgentDefinition(&storage.AgentDefinitionRecord{
		ID: "private-reviewer", Name: "Private Reviewer", Runtime: agentdef.RuntimeDocker, ProjectID: &otherID,
		DefinitionYAML: "version: \"1\"\nagent:\n  id: private-reviewer\n  name: Private Reviewer\nruntime:\n  type: docker\n",
		CreatedAt:      now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	sess.ProjectID = &projectID
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	row := dockerPSRow{
		ID: "abcdef1234567890", Names: "agent-private-reviewer__project-project-b", Image: "a2gent-brute:latest", State: "running",
		Labels: localAgentManagerLabelKey + "=" + localAgentManagerLabelValue + "," + dockerRuntimeAgentDefLabelKey + "=private-reviewer,a2gent.project_id=" + otherID,
	}
	raw, _ := json.Marshal(row)
	oldRunCommand := runCommand
	runCommand = func(_ context.Context, command string, args ...string) (string, error) {
		if command == "docker" && len(args) > 0 && args[0] == "ps" {
			return string(raw), nil
		}
		t.Errorf("authorization must precede any startup or execution: %s %v", command, args)
		return "", fmt.Errorf("unexpected runtime command")
	}
	t.Cleanup(func() { runCommand = oldRunCommand })
	ctx := context.WithValue(t.Context(), "session_id", sess.ID)
	for _, id := range []string{"private-reviewer", row.Names, row.ID, row.ID[:8]} {
		params, _ := json.Marshal(delegateToAgentParams{AgentID: id, Task: "review"})
		result, err := newDelegateToAgentTool(server).Execute(ctx, params)
		if err != nil || result == nil || result.Success || (!strings.Contains(result.Error, "another project") && !strings.Contains(result.Error, "not available")) {
			t.Fatalf("cross-project target %s must be denied: %+v %v", id, result, err)
		}
	}
}

func TestAuthorizeDockerDelegationTargetChecksMountedWorkspace(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	now := time.Now()
	for _, scope := range []string{agentdef.WorkspaceScopeNone, agentdef.WorkspaceScopeCurrentProject} {
		if err := store.SaveAgentDefinition(&storage.AgentDefinitionRecord{
			ID: scope, Name: scope, Runtime: agentdef.RuntimeDocker,
			DefinitionYAML: fmt.Sprintf("version: \"1\"\nagent:\n  id: %s\n  name: Reviewer\nruntime:\n  type: docker\nworkspace:\n  scope: %s\n", scope, scope),
			CreatedAt:      now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		agent := &LocalDockerAgent{Name: "warm-reviewer", Labels: map[string]string{dockerRuntimeAgentDefLabelKey: scope}}
		if scope == agentdef.WorkspaceScopeCurrentProject {
			agent.Labels["a2gent.project_id"] = "project-a"
		}
		if err := server.authorizeDockerDelegationTarget(agent, "project-a"); err != nil {
			t.Fatalf("authorized %s alias rejected: %v", scope, err)
		}
		agent.Labels["a2gent.workspace_scope"] = agentdef.WorkspaceScopeAllProjects
		if err := server.authorizeDockerDelegationTarget(agent, "project-a"); err == nil {
			t.Fatalf("stale all-projects alias must not match a current %s definition", scope)
		}
		delete(agent.Labels, "a2gent.workspace_scope")
		agent.Labels["a2gent.project_id"] = "project-b"
		if err := server.authorizeDockerDelegationTarget(agent, "project-a"); err == nil {
			t.Fatalf("%s alias must not target another mounted project", scope)
		}
	}
	if err := server.authorizeDockerDelegationTarget(&LocalDockerAgent{Name: "unclassified"}, "project-a"); err == nil {
		t.Fatal("unclassified container must not bypass configured agent scope")
	}
}
