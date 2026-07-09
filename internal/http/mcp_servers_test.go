package http

import (
	"context"
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

func TestProjectMCPServerRuntimeCwdDefaultsToProjectFolder(t *testing.T) {
	server, store := newProjectsAPITestServer(t)
	defer store.Close()

	projectFolder := t.TempDir()
	now := time.Now()
	project := &storage.Project{ID: "project-a", Name: "Project A", Folder: &projectFolder, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	resp := createMCPServerForTest(t, server, MCPServerRequest{
		ProjectID: &project.ID,
		Name:      "project-docs",
		Transport: mcpTransportStdio,
		Command:   "npx",
		Cwd:       "/tmp/ignored-for-project-mcp",
	})

	stored, err := store.GetMCPServer(resp.ID)
	if err != nil {
		t.Fatalf("GetMCPServer: %v", err)
	}
	cfg, err := server.resolveMCPServerRuntimeConfig(stored)
	if err != nil {
		t.Fatalf("resolveMCPServerRuntimeConfig: %v", err)
	}
	if cfg.Cwd != projectFolder {
		t.Fatalf("runtime cwd = %q, want project folder %q", cfg.Cwd, projectFolder)
	}
}

func TestMCPListAndCallToolsForHTTPServer(t *testing.T) {
	mcpHTTP := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}

		resp := map[string]interface{}{
			"jsonrpc": "2.0",
		}
		if id, ok := req["id"]; ok {
			resp["id"] = id
		}

		switch req["method"] {
		case "initialize":
			resp["result"] = map[string]interface{}{
				"serverInfo":   map[string]interface{}{"name": "fake-atlassian"},
				"capabilities": map[string]interface{}{"tools": map[string]interface{}{}},
			}
		case "notifications/initialized":
			resp["result"] = map[string]interface{}{}
		case "tools/list":
			resp["result"] = map[string]interface{}{
				"tools": []interface{}{
					map[string]interface{}{
						"name":        "searchJiraIssues",
						"description": "Search Jira issues with JQL.",
						"inputSchema": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"jql": map[string]interface{}{"type": "string"},
							},
							"required": []interface{}{"jql"},
						},
					},
				},
			}
		case "tools/call":
			params := mapFromAny(req["params"])
			if params["name"] != "searchJiraIssues" {
				t.Fatalf("unexpected MCP tool call: %#v", params)
			}
			resp["result"] = map[string]interface{}{
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "ABC-123 Demo Jira issue"},
				},
			}
		default:
			resp["error"] = map[string]interface{}{"code": -32601, "message": "method not found"}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode MCP response: %v", err)
		}
	}))
	defer mcpHTTP.Close()

	server, store := newProjectsAPITestServer(t)
	defer store.Close()

	createMCPServerForTest(t, server, MCPServerRequest{
		Name:      "Atlassian Jira",
		Transport: mcpTransportHTTP,
		URL:       mcpHTTP.URL,
	})

	listResult, err := newMCPListToolsTool(server).Execute(context.Background(), json.RawMessage(`{"server":"jira"}`))
	if err != nil {
		t.Fatalf("mcp_list_tools Execute: %v", err)
	}
	if !listResult.Success || !strings.Contains(listResult.Output, "searchJiraIssues") {
		t.Fatalf("mcp_list_tools result = %#v", listResult)
	}

	callResult, err := newMCPCallTool(server).Execute(context.Background(), json.RawMessage(`{"server":"jira","tool":"searchJiraIssues","arguments":{"jql":"assignee=currentUser()"}}`))
	if err != nil {
		t.Fatalf("mcp_call Execute: %v", err)
	}
	if !callResult.Success || !strings.Contains(callResult.Output, "ABC-123 Demo Jira issue") {
		t.Fatalf("mcp_call result = %#v", callResult)
	}
}

func TestMCPServersImportPlainJSON(t *testing.T) {
	server, store := newProjectsAPITestServer(t)
	defer store.Close()

	payload := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"cloudflare-api": map[string]interface{}{
				"url": "https://mcp.cloudflare.com/mcp",
			},
			"context7": map[string]interface{}{
				"command": "npx",
				"args":    []string{"-y", "@upstash/context7-mcp@latest"},
			},
		},
	}
	rec := requestProjectJSON(t, server, stdhttp.MethodPost, "/mcp/servers/import", payload)
	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("import MCP servers status = %d, body = %s", rec.Code, rec.Body.String())
	}

	servers := listMCPServersForTest(t, server, "/mcp/servers/")
	assertMCPServerNames(t, servers, []string{"cloudflare-api", "context7"})
	for _, srv := range servers {
		if srv.Name == "cloudflare-api" && (srv.Transport != mcpTransportHTTP || srv.URL != "https://mcp.cloudflare.com/mcp") {
			t.Fatalf("cloudflare import = %#v", srv)
		}
		if srv.Status != "unchecked" {
			t.Fatalf("status = %q, want unchecked", srv.Status)
		}
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
