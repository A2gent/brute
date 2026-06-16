package http

import (
	"context"
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"testing"
	"time"
)

func TestHandleGetSessionProxiesDockerChildSession(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	child := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Path != "/sessions/docker-child" {
			t.Fatalf("unexpected child path %q", r.URL.Path)
		}
		if r.URL.Query().Get("include_metadata") != "true" {
			t.Fatalf("expected parent proxy to request metadata, got query %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":         "docker-child",
			"agent_id":   "build",
			"title":      "Docker child transcript",
			"status":     "completed",
			"created_at": now,
			"updated_at": now,
			"messages": []map[string]interface{}{
				{"role": "user", "content": "inspect this", "timestamp": now},
				{"role": "assistant", "content": "inspection complete", "timestamp": now},
			},
			"metadata": map[string]interface{}{
				"parent_session_id": "parent-session",
			},
		})
	}))
	defer child.Close()
	stubDockerPS(t, child.URL, "agent-dev-architecture-planner")

	req := httptest.NewRequest(stdhttp.MethodGet, "/sessions/docker-child", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp SessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.ID != "docker-child" || resp.ParentID != "parent-session" {
		t.Fatalf("unexpected session identity: id=%q parent=%q", resp.ID, resp.ParentID)
	}
	if len(resp.Messages) != 2 || resp.Messages[1].Content != "inspection complete" {
		t.Fatalf("expected proxied transcript, got %+v", resp.Messages)
	}
	if resp.Metadata["proxied_from_docker_agent"] != true {
		t.Fatalf("expected docker proxy metadata, got %+v", resp.Metadata)
	}
	if resp.Metadata["docker_agent_name"] != "agent-dev-architecture-planner" {
		t.Fatalf("expected docker agent name metadata, got %+v", resp.Metadata)
	}
}

func TestHandleGetSessionDockerProxyPreservesParentIDForSummary(t *testing.T) {
	server, _ := newUnifiedAgentsTestServer(t)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	child := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Query().Get("include_messages") != "false" {
			t.Fatalf("expected summary proxy request, got query %q", r.URL.RawQuery)
		}
		if r.URL.Query().Get("include_metadata") != "true" {
			t.Fatalf("expected internal metadata request, got query %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":         "docker-child",
			"agent_id":   "build",
			"title":      "Docker child transcript",
			"status":     "running",
			"created_at": now,
			"updated_at": now,
			"metadata": map[string]interface{}{
				"parent_session_id": "parent-session",
			},
		})
	}))
	defer child.Close()
	stubDockerPS(t, child.URL, "agent-dev-architecture-planner")

	req := httptest.NewRequest(stdhttp.MethodGet, "/sessions/docker-child?include_messages=false&include_metadata=false", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp SessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.ParentID != "parent-session" {
		t.Fatalf("expected parent id to survive metadata stripping, got %q", resp.ParentID)
	}
	if resp.Messages != nil {
		t.Fatalf("expected messages to be stripped for summary response, got %+v", resp.Messages)
	}
	if resp.Metadata != nil {
		t.Fatalf("expected metadata to be stripped for summary response, got %+v", resp.Metadata)
	}
}

func stubDockerPS(t *testing.T, apiURL string, containerName string) {
	t.Helper()
	parsed, err := neturl.Parse(apiURL)
	if err != nil {
		t.Fatalf("failed to parse api url: %v", err)
	}
	port := parsed.Port()
	if port == "" {
		t.Fatalf("test api url has no port: %s", apiURL)
	}

	oldRunCommand := runCommand
	runCommand = func(ctx context.Context, command string, args ...string) (string, error) {
		if command != "docker" || len(args) < 1 || args[0] != "ps" {
			return "", fmt.Errorf("unexpected command: %s %s", command, strings.Join(args, " "))
		}
		return fmt.Sprintf(
			`{"ID":"container-1","Image":"a2gent-brute:latest","Command":"","CreatedAt":"","RunningFor":"","Ports":"0.0.0.0:%s->8080/tcp","Status":"Up 1 minute","State":"running","Names":%q,"Labels":"%s=%s"}`,
			port,
			containerName,
			localAgentManagerLabelKey,
			localAgentManagerLabelValue,
		), nil
	}
	t.Cleanup(func() {
		runCommand = oldRunCommand
	})
}
