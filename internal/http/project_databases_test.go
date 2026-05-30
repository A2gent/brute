package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleProjectDatabase(t *testing.T) {
	server, projectID, _ := newProjectFileTestServer(t)

	// 2. Create a database
	dbReq := CreateProjectDatabaseRequest{
		Name:        "Test DB",
		Engine:      "sqlite",
		DSN:         ":memory:",
		Environment: "Local",
		IsReadOnly:  false,
	}
	body, _ := json.Marshal(dbReq)
	req := httptest.NewRequest(http.MethodPost, "/projects/"+projectID+"/databases", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("Failed to create db: %d %s", rr.Code, rr.Body.String())
	}

	var dbResp ProjectDatabaseResponse
	json.NewDecoder(rr.Body).Decode(&dbResp)
	
	if dbResp.Name != "Test DB" || dbResp.Engine != "sqlite" {
		t.Errorf("Unexpected db properties: %+v", dbResp)
	}

	// 3. List databases
	req = httptest.NewRequest(http.MethodGet, "/projects/"+projectID+"/databases", nil)
	rr = httptest.NewRecorder()
	server.router.ServeHTTP(rr, req)

	var dbs []ProjectDatabaseResponse
	json.NewDecoder(rr.Body).Decode(&dbs)

	if len(dbs) != 1 || dbs[0].ID != dbResp.ID {
		t.Errorf("Unexpected databases list: %+v", dbs)
	}
}
