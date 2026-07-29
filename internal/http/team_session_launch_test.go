package http

import (
	"encoding/json"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

func TestCreateSessionAcceptsTeamIDAndCreatesRun(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	defer store.Close()

	now := time.Now().UTC()
	if err := store.SaveTeam(&storage.TeamRecord{
		ID:             "squad-launch",
		Name:           "Launch squad",
		DefinitionYAML: teamsAPIValidYAML,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("SaveTeam() error = %v", err)
	}
	// teamsAPIValidYAML uses id api-squad; overwrite with matching id for this launch test.
	updated := strings.Replace(teamsAPIValidYAML, "id: api-squad", "id: squad-launch", 1)
	updated = strings.Replace(updated, "name: API squad", "name: Launch squad", 1)
	if err := store.SaveTeam(&storage.TeamRecord{
		ID:             "squad-launch",
		Name:           "Launch squad",
		DefinitionYAML: updated,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("SaveTeam overwrite error = %v", err)
	}

	create := requestTeamJSON(t, server, stdhttp.MethodPost, "/sessions", map[string]interface{}{
		"agent_id": "build",
		"team_id":  "squad-launch",
		"task":     "Ship it",
		"queued":   true,
	})
	if create.Code != stdhttp.StatusCreated {
		t.Fatalf("POST /sessions status = %d body = %s", create.Code, create.Body.String())
	}
	var created map[string]interface{}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	sessionID, _ := created["id"].(string)
	if sessionID == "" {
		t.Fatalf("missing session id: %#v", created)
	}
	run := requestTeamJSON(t, server, stdhttp.MethodGet, "/sessions/"+sessionID+"/team-run", nil)
	if run.Code != stdhttp.StatusOK {
		t.Fatalf("GET session team-run status = %d body = %s", run.Code, run.Body.String())
	}
	if !strings.Contains(run.Body.String(), `"team_id":"squad-launch"`) {
		t.Fatalf("unexpected team run body: %s", run.Body.String())
	}
}
