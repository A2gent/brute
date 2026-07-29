package http

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

const teamsAPIValidYAML = `id: api-squad
name: API squad
description: Build and review APIs.
policy:
  lead: developer
  termination: lead_done
  max_messages: 20
  max_minutes: 30
  broadcast_allowed: true
members:
  - role: developer
    agent_id: developer-agent
    charter: Implements the requested change.
  - role: critic
    agent_id: critic-agent
    charter: Reviews correctness and tests.
`

func TestTeamsAPICRUDAndCanonicalProjectYAML(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	defer store.Close()

	projectDir := t.TempDir()
	projectID := "project-teams"
	now := time.Now()
	if err := store.SaveProject(&storage.Project{ID: projectID, Name: "Teams project", Folder: &projectDir, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("SaveProject() error = %v", err)
	}

	create := requestTeamJSON(t, server, stdhttp.MethodPost, "/teams", map[string]interface{}{
		"project_id": projectID,
		"yaml":       teamsAPIValidYAML,
	})
	if create.Code != stdhttp.StatusCreated {
		t.Fatalf("POST /teams status = %d, body = %s", create.Code, create.Body.String())
	}

	yamlPath := filepath.Join(projectDir, "teams", "api-squad.yaml")
	raw, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read canonical YAML: %v", err)
	}
	if !strings.Contains(string(raw), "id: api-squad") || !strings.Contains(string(raw), "role: critic") {
		t.Fatalf("canonical YAML = %s", raw)
	}

	get := requestTeamJSON(t, server, stdhttp.MethodGet, "/teams/api-squad", nil)
	if get.Code != stdhttp.StatusOK || !strings.Contains(get.Body.String(), `"project_id":"project-teams"`) {
		t.Fatalf("GET /teams/api-squad status = %d, body = %s", get.Code, get.Body.String())
	}
	list := requestTeamJSON(t, server, stdhttp.MethodGet, "/teams?project_id="+projectID, nil)
	if list.Code != stdhttp.StatusOK || !strings.Contains(list.Body.String(), `"id":"api-squad"`) {
		t.Fatalf("GET /teams status = %d, body = %s", list.Code, list.Body.String())
	}
	exported := requestTeamJSON(t, server, stdhttp.MethodGet, "/teams/api-squad/yaml", nil)
	if exported.Code != stdhttp.StatusOK || !strings.Contains(exported.Body.String(), `"yaml":"id: api-squad`) {
		t.Fatalf("GET /teams/api-squad/yaml status = %d, body = %s", exported.Code, exported.Body.String())
	}

	updatedYAML := strings.Replace(teamsAPIValidYAML, "name: API squad", "name: Updated API squad", 1)
	update := requestTeamJSON(t, server, stdhttp.MethodPut, "/teams/api-squad", map[string]interface{}{
		"project_id": projectID,
		"yaml":       updatedYAML,
	})
	if update.Code != stdhttp.StatusOK {
		t.Fatalf("PUT /teams/api-squad status = %d, body = %s", update.Code, update.Body.String())
	}
	stored, err := store.GetTeam("api-squad")
	if err != nil || stored.Name != "Updated API squad" {
		t.Fatalf("updated SQLite team = %#v, err = %v", stored, err)
	}

	deleted := requestTeamJSON(t, server, stdhttp.MethodDelete, "/teams/api-squad", nil)
	if deleted.Code != stdhttp.StatusNoContent {
		t.Fatalf("DELETE /teams/api-squad status = %d, body = %s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(yamlPath); !os.IsNotExist(err) {
		t.Fatalf("canonical YAML still exists, stat error = %v", err)
	}
}

func TestTeamsAPIImportsYAMLAndRejectsPathIDMismatch(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	defer store.Close()

	imported := requestTeamJSON(t, server, stdhttp.MethodPost, "/teams/import-yaml", map[string]interface{}{"config_yaml": teamsAPIValidYAML})
	if imported.Code != stdhttp.StatusCreated {
		t.Fatalf("POST /teams/import-yaml status = %d, body = %s", imported.Code, imported.Body.String())
	}

	mismatch := requestTeamJSON(t, server, stdhttp.MethodPut, "/teams/other-id", map[string]interface{}{"yaml": teamsAPIValidYAML})
	if mismatch.Code != stdhttp.StatusBadRequest || !strings.Contains(mismatch.Body.String(), "must match") {
		t.Fatalf("PUT mismatched team status = %d, body = %s", mismatch.Code, mismatch.Body.String())
	}
}

func requestTeamJSON(t *testing.T, server *Server, method, path string, payload interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if payload == nil {
		body = strings.NewReader("")
	} else {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		body = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	return rec
}
