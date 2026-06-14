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
		projectID, mount, err = resolveWorkspaceBinding(broad, "")
		if err != nil || projectID != dockerRuntimeAllProjectsBinding || mount != agentdef.WorkspaceMountRO {
			t.Fatalf("all_projects should use a stable broad-workspace binding: %q %q %v", projectID, mount, err)
		}

		selected := &agentdef.Definition{
			Agent:     agentdef.AgentMeta{ID: "x"},
			Runtime:   agentdef.Runtime{Type: agentdef.RuntimeDocker},
			Workspace: agentdef.Workspace{Scope: agentdef.WorkspaceScopeSelectedProjects},
			Local: agentdef.Local{
				ProjectBindings: map[string]string{agentdef.WorkspaceScopeSelectedProjects: "proj-a, proj-b"},
			},
		}
		projectID, mount, err = resolveWorkspaceBinding(selected, "")
		if err != nil || projectID != dockerRuntimeSelectedProjectsBinding || mount != agentdef.WorkspaceMountRO {
			t.Fatalf("selected_projects should use a stable broad-workspace binding: %q %q %v", projectID, mount, err)
		}
		selected.Local.ProjectBindings = nil
		if _, _, err = resolveWorkspaceBinding(selected, ""); err == nil {
			t.Fatal("selected_projects without binding must fail")
		}
}


func TestResolveDockerWorkspaceBindingAllAndSelectedProjects(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	appRoot := t.TempDir()
	apiRoot := t.TempDir()
	for _, project := range []*storage.Project{
		{ID: "proj-app", Name: "App", Folder: &appRoot, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "proj-api", Name: "API", Folder: &apiRoot, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	} {
		if err := store.SaveProject(project); err != nil {
			t.Fatalf("failed to save project %s: %v", project.ID, err)
		}
	}

	allDef := &agentdef.Definition{
		Agent:     agentdef.AgentMeta{ID: "planner"},
		Runtime:   agentdef.Runtime{Type: agentdef.RuntimeDocker},
		Workspace: agentdef.Workspace{Scope: agentdef.WorkspaceScopeAllProjects, Mount: agentdef.WorkspaceMountRO},
	}
	binding, err := server.resolveDockerWorkspaceBinding(allDef, "")
	if err != nil {
		t.Fatalf("resolve all_projects failed: %v", err)
	}
	if binding.ContainerNameBinding != dockerRuntimeAllProjectsBinding || len(binding.ProjectMounts) != 2 {
		t.Fatalf("unexpected all_projects binding: %+v", binding)
	}
	for _, mount := range binding.ProjectMounts {
		if mount.Mode != agentdef.WorkspaceMountRO || !strings.HasPrefix(mount.ContainerPath, dockerRuntimeWorkspaceRoot+"/") {
			t.Fatalf("project mount should be read-only under /workspace: %+v", mount)
		}
	}

	selectedDef := &agentdef.Definition{
		Agent:     agentdef.AgentMeta{ID: "planner"},
		Runtime:   agentdef.Runtime{Type: agentdef.RuntimeDocker},
		Workspace: agentdef.Workspace{Scope: agentdef.WorkspaceScopeSelectedProjects},
		Local: agentdef.Local{
			ProjectBindings: map[string]string{agentdef.WorkspaceScopeSelectedProjects: "proj-api"},
		},
	}
	binding, err = server.resolveDockerWorkspaceBinding(selectedDef, "")
	if err != nil {
		t.Fatalf("resolve selected_projects failed: %v", err)
	}
	if binding.ContainerNameBinding != dockerRuntimeSelectedProjectsBinding || len(binding.ProjectMounts) != 1 || binding.ProjectMounts[0].ProjectID != "proj-api" {
		t.Fatalf("unexpected selected_projects binding: %+v", binding)
	}
}

func TestCreateAgentContainerMountsAllProjectsReadOnly(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	appRoot := t.TempDir()
	apiRoot := t.TempDir()
	for _, project := range []*storage.Project{
		{ID: "proj-app", Name: "App", Folder: &appRoot, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "proj-api", Name: "API", Folder: &apiRoot, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	} {
		if err := store.SaveProject(project); err != nil {
			t.Fatalf("failed to save project %s: %v", project.ID, err)
		}
	}
	def := &agentdef.Definition{
		Agent:     agentdef.AgentMeta{ID: "planner", Name: "Planner"},
		Runtime:   agentdef.Runtime{Type: agentdef.RuntimeDocker},
		Workspace: agentdef.Workspace{Scope: agentdef.WorkspaceScopeAllProjects, Mount: agentdef.WorkspaceMountRO},
		Tools:     agentdef.Tools{Mode: agentdef.ToolsModeAllow, Enabled: []string{"read", "grep"}},
	}
	binding, err := server.resolveDockerWorkspaceBinding(def, "")
	if err != nil {
		t.Fatalf("resolve all_projects failed: %v", err)
	}

	oldRunCommand := runCommand
	var dockerRunArgs []string
	runCommand = func(ctx context.Context, command string, args ...string) (string, error) {
		if command != "docker" {
			return "", nil
		}
		if len(args) > 0 && args[0] == "run" {
			dockerRunArgs = append([]string(nil), args...)
			return "container-id", nil
		}
		if len(args) > 0 && args[0] == "ps" {
			if len(dockerRunArgs) == 0 {
				return "", nil
			}
			row := dockerPSRow{
				ID:     "container-id",
				Image:  "a2gent-brute:latest",
				State:  "running",
				Status: "Up",
				Names:  "agent-planner__project-all-projects",
				Ports:  "0.0.0.0:18080->8080/tcp",
				Labels: localAgentManagerLabelKey + "=" + localAgentManagerLabelValue + "," + dockerRuntimeAgentDefLabelKey + "=planner",
			}
			encoded, _ := json.Marshal(row)
			return string(encoded), nil
		}
		return "", nil
	}
	t.Cleanup(func() { runCommand = oldRunCommand })

	agent, err := server.dockerRuntime.createAgentContainer(context.Background(), def, "agent-planner__project-all-projects", binding)
	if err != nil {
		t.Fatalf("createAgentContainer failed: %v", err)
	}
	if agent == nil || agent.Name != "agent-planner__project-all-projects" {
		t.Fatalf("unexpected created agent: %+v", agent)
	}
	joined := strings.Join(dockerRunArgs, "\n")
	for _, want := range []string{
		appRoot + ":/workspace/proj-app:ro",
		apiRoot + ":/workspace/proj-api:ro",
		"a2gent.workspace_scope=all_projects",
		"AAGENT_SYSTEM_PROMPT=",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected docker run args to contain %q, got:\n%s", want, joined)
		}
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
