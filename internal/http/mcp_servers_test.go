package http

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func TestMCPServersGlobalAndProjectScope(t *testing.T) {
	server, store := newProjectsAPITestServer(t)
	defer store.Close()

	now := time.Now()
	projectA := &storage.Project{ID: "project-a", Name: "Project A", CreatedAt: now, UpdatedAt: now}
	projectB := &storage.Project{ID: "project-b", Name: "Project B", CreatedAt: now, UpdatedAt: now}
	if err := store.SaveProject(projectA); err != nil {
		t.Fatalf("SaveProject A: %v", err)
	}
	if err := store.SaveProject(projectB); err != nil {
		t.Fatalf("SaveProject B: %v", err)
	}

	createMCPServerForTest(t, server, MCPServerRequest{
		Name:      "global-fetch",
		Transport: mcpTransportStdio,
		Command:   "uvx",
	})
	createMCPServerForTest(t, server, MCPServerRequest{
		ProjectID: &projectA.ID,
		Name:      "project-a-docs",
		Transport: mcpTransportHTTP,
		URL:       "http://127.0.0.1:9001/mcp",
	})
	createMCPServerForTest(t, server, MCPServerRequest{
		ProjectID: &projectB.ID,
		Name:      "project-b-docs",
		Transport: mcpTransportHTTP,
		URL:       "http://127.0.0.1:9002/mcp",
	})

	global := listMCPServersForTest(t, server, "/mcp/servers/")
	assertMCPServerNames(t, global, []string{"global-fetch"})

	projectVisible := listMCPServersForTest(t, server, "/mcp/servers/?project_id=project-a")
	assertMCPServerNames(t, projectVisible, []string{"global-fetch", "project-a-docs"})

	section, _, resolveErr := server.resolveMCPServersSection(1, projectA.ID)
	if resolveErr != "" {
		t.Fatalf("resolveMCPServersSection error: %s", resolveErr)
	}
	if !strings.Contains(section, "global-fetch") || !strings.Contains(section, "project-a-docs") {
		t.Fatalf("expected global and project A MCP servers in prompt section, got:\n%s", section)
	}
	if strings.Contains(section, "project-b-docs") {
		t.Fatalf("did not expect project B MCP server in project A prompt section, got:\n%s", section)
	}
}

func createMCPServerForTest(t *testing.T, server *Server, payload MCPServerRequest) MCPServerResponse {
	t.Helper()
	rec := requestProjectJSON(t, server, stdhttp.MethodPost, "/mcp/servers/", payload)
	if rec.Code != stdhttp.StatusCreated {
		t.Fatalf("create MCP server status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp MCPServerResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode MCP server response: %v", err)
	}
	return resp
}

func listMCPServersForTest(t *testing.T, server *Server, path string) []MCPServerResponse {
	t.Helper()
	req := httptest.NewRequest(stdhttp.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("list MCP servers status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp []MCPServerResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode MCP server list response: %v", err)
	}
	return resp
}

func assertMCPServerNames(t *testing.T, servers []MCPServerResponse, want []string) {
	t.Helper()
	got := make(map[string]bool, len(servers))
	for _, server := range servers {
		got[server.Name] = true
	}
	if len(got) != len(want) {
		t.Fatalf("server names = %#v, want %v", got, want)
	}
	for _, name := range want {
		if !got[name] {
			t.Fatalf("server names = %#v, missing %q", got, name)
		}
	}
}
