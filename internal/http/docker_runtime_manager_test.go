package http

import (
	"fmt"
	"testing"

	"github.com/A2gent/brute/internal/agentdef"
)

func TestContainerNameForAgent(t *testing.T) {
	if got := containerNameForAgent("code-reviewer", ""); got != "agent-code-reviewer" {
		t.Fatalf("unexpected global container name: %s", got)
	}
	if got := containerNameForAgent("code-reviewer", "brute"); got != "agent-code-reviewer__project-brute" {
		t.Fatalf("unexpected project container name: %s", got)
	}
	if got := containerNameForAgent("Code Reviewer!", "My Project"); got != "agent-code-reviewer__project-my-project" {
		t.Fatalf("expected slugified names, got: %s", got)
	}
}

func TestResolveWorkspaceBinding(t *testing.T) {
	noScope := &agentdef.Definition{Agent: agentdef.AgentMeta{ID: "x"}, Runtime: agentdef.Runtime{Type: agentdef.RuntimeDocker}}
	projectID, mount, err := resolveWorkspaceBinding(noScope, "proj-1")
	if err != nil || projectID != "" || mount != "" {
		t.Fatalf("no scope should mount nothing: %q %q %v", projectID, mount, err)
	}

	current := &agentdef.Definition{
		Agent:     agentdef.AgentMeta{ID: "x"},
		Runtime:   agentdef.Runtime{Type: agentdef.RuntimeDocker},
		Workspace: agentdef.Workspace{Scope: agentdef.WorkspaceScopeCurrentProject, Mount: agentdef.WorkspaceMountRW},
	}
	projectID, mount, err = resolveWorkspaceBinding(current, "proj-1")
	if err != nil || projectID != "proj-1" || mount != agentdef.WorkspaceMountRW {
		t.Fatalf("current_project should bind parent project: %q %q %v", projectID, mount, err)
	}
	// Without a parent project there is nothing to mount, but delegation proceeds.
	projectID, mount, err = resolveWorkspaceBinding(current, "")
	if err != nil || projectID != "" {
		t.Fatalf("current_project without parent project should run unmounted: %q %q %v", projectID, mount, err)
	}

	configured := &agentdef.Definition{
		Agent:     agentdef.AgentMeta{ID: "x"},
		Runtime:   agentdef.Runtime{Type: agentdef.RuntimeDocker},
		Workspace: agentdef.Workspace{Scope: agentdef.WorkspaceScopeConfiguredProject},
		Local: agentdef.Local{
			ProjectBindings: map[string]string{agentdef.WorkspaceScopeConfiguredProject: "proj-2"},
		},
	}
	projectID, mount, err = resolveWorkspaceBinding(configured, "proj-1")
	if err != nil || projectID != "proj-2" || mount != agentdef.WorkspaceMountRO {
		t.Fatalf("configured_project should use the binding with ro default: %q %q %v", projectID, mount, err)
	}

	configured.Local.ProjectBindings = nil
	if _, _, err = resolveWorkspaceBinding(configured, ""); err == nil {
		t.Fatal("configured_project without binding must fail")
	}

	broad := &agentdef.Definition{
		Agent:     agentdef.AgentMeta{ID: "x"},
		Runtime:   agentdef.Runtime{Type: agentdef.RuntimeDocker},
		Workspace: agentdef.Workspace{Scope: agentdef.WorkspaceScopeAllProjects},
	}
	if _, _, err = resolveWorkspaceBinding(broad, ""); err == nil {
		t.Fatal("all_projects must be rejected until elevated access is implemented")
	}
}

func TestIsDockerPortAllocationError(t *testing.T) {
	cases := []string{
		"failed to start local agent container: exit status 125: docker: Error response from daemon: Bind for 0.0.0.0:18080 failed: port is already allocated",
		"listen tcp 127.0.0.1:18080: bind: address already in use",
	}
	for _, message := range cases {
		if !isDockerPortAllocationError(fmt.Errorf("%s", message)) {
			t.Fatalf("expected port allocation error for %q", message)
		}
	}
	if isDockerPortAllocationError(fmt.Errorf("image not found")) {
		t.Fatal("unrelated docker errors should not be treated as port allocation errors")
	}
}
