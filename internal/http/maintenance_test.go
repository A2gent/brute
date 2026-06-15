// maintenance_test.go covers high-risk local operations exposed to Caesar: rebuilding
// the host binary and restarting warm Docker containers after an image rebuild.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestHandleRebuildBruteRunsConfiguredBuildCommand(t *testing.T) {
	oldRunCommand := runCommand
	var gotCommand string
	var gotArgs []string
	runCommand = func(ctx context.Context, command string, args ...string) (string, error) {
		gotCommand = command
		gotArgs = append([]string(nil), args...)
		return "build ok", nil
	}
	t.Cleanup(func() { runCommand = oldRunCommand })
	t.Setenv("A2GENT_BRUTE_REBUILD_COMMAND", "go build -o /tmp/brute-test ./cmd/aagent")

	req := httptest.NewRequest(http.MethodPost, "/system/rebuild", strings.NewReader(`{"timeout_seconds":1}`))
	rec := httptest.NewRecorder()

	server := &Server{}
	server.handleRebuildBrute(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotCommand != "go" || !reflect.DeepEqual(gotArgs, []string{"build", "-o", "/tmp/brute-test", "./cmd/aagent"}) {
		t.Fatalf("unexpected command: %q %#v", gotCommand, gotArgs)
	}
	var body systemCommandResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body.Status != "rebuilt" || body.Output != "build ok" {
		t.Fatalf("unexpected response: %#v", body)
	}
}

func TestHandleRestartRunningSubAgentContainersOnlyRestartsWarmManagedContainers(t *testing.T) {
	oldRunCommand := runCommand
	var restarted []string
	runCommand = func(ctx context.Context, command string, args ...string) (string, error) {
		if command != "docker" {
			return "", nil
		}
		if len(args) > 0 && args[0] == "ps" {
			rows := []dockerPSRow{
				{
					ID:     "running-managed-id",
					Image:  "a2gent-brute:latest",
					State:  "running",
					Status: "Up",
					Names:  "agent-code-reviewer__project-demo",
					Ports:  "0.0.0.0:18080->8080/tcp",
					Labels: localAgentManagerLabelKey + "=" + localAgentManagerLabelValue + "," + dockerRuntimeManagedLabelKey + "=true," + dockerRuntimeAgentDefLabelKey + "=code-reviewer",
				},
				{
					ID:     "stopped-managed-id",
					Image:  "a2gent-brute:latest",
					State:  "exited",
					Status: "Exited",
					Names:  "agent-planner__project-demo",
					Labels: localAgentManagerLabelKey + "=" + localAgentManagerLabelValue + "," + dockerRuntimeManagedLabelKey + "=true," + dockerRuntimeAgentDefLabelKey + "=planner",
				},
				{
					ID:     "manual-id",
					Image:  "a2gent-brute:latest",
					State:  "running",
					Status: "Up",
					Names:  "manual-brute",
					Labels: localAgentManagerLabelKey + "=" + localAgentManagerLabelValue,
				},
			}
			encoded := make([]string, 0, len(rows))
			for _, row := range rows {
				raw, _ := json.Marshal(row)
				encoded = append(encoded, string(raw))
			}
			return strings.Join(encoded, "\n"), nil
		}
		if len(args) == 2 && args[0] == "restart" {
			restarted = append(restarted, args[1])
			return "restart ok", nil
		}
		return "", nil
	}
	t.Cleanup(func() { runCommand = oldRunCommand })

	req := httptest.NewRequest(http.MethodPost, "/system/sub-agent-containers/restart", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	server := &Server{}
	server.handleRestartRunningSubAgentContainers(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(restarted, []string{"running-managed-id"}) {
		t.Fatalf("expected only running managed sub-agent container to restart, got %#v", restarted)
	}
	var body restartSubAgentContainersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body.RestartedCount != 1 || body.SkippedCount != 2 || len(body.Restarted) != 1 {
		t.Fatalf("unexpected response: %#v", body)
	}
	if body.Restarted[0].ContainerID != "running-managed-id" || body.Restarted[0].AgentDefinitionID != "code-reviewer" {
		t.Fatalf("unexpected restarted item: %#v", body.Restarted[0])
	}
}
