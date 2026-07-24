package http

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestHandleProjectDatabaseTableSchemaSQLite(t *testing.T) {
	server, projectID, _ := newProjectFileTestServer(t)
	dbPath := t.TempDir() + "/table-schema.db"
	fixture, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite fixture: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE customers (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE invoices (id INTEGER PRIMARY KEY, customer_id INTEGER NOT NULL, paid INTEGER NOT NULL DEFAULT 0, note TEXT, FOREIGN KEY (customer_id) REFERENCES customers(id))`,
	} {
		if _, err := fixture.Exec(statement); err != nil {
			fixture.Close()
			t.Fatalf("execute fixture statement %q: %v", statement, err)
		}
	}
	if err := fixture.Close(); err != nil {
		t.Fatalf("close sqlite fixture: %v", err)
	}

	dbResp := createProjectDatabaseFixture(t, server, projectID, CreateProjectDatabaseRequest{
		Name:        "Schema DB",
		Engine:      "sqlite",
		DSN:         dbPath,
		Environment: "Local",
		IsReadOnly:  true,
	})

	target := "/projects/" + projectID + "/databases/" + dbResp.ID + "/tables/" + url.PathEscape("invoices") + "/schema"
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("table schema: %d %s", rec.Code, rec.Body.String())
	}

	var response []ProjectDatabaseTableColumnResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode schema response: %v", err)
	}
	if len(response) != 4 || response[0].Name != "id" || !response[0].IsPrimaryKey {
		t.Fatalf("unexpected schema response: %+v", response)
	}
	customerIDColumn := response[1]
	if customerIDColumn.Name != "customer_id" || len(customerIDColumn.ForeignKeys) != 1 {
		t.Fatalf("unexpected customer_id foreign keys: %+v", customerIDColumn)
	}
	if customerIDColumn.ForeignKeys[0].ReferencedTable != "customers" || customerIDColumn.ForeignKeys[0].ReferencedColumn != "id" {
		t.Fatalf("unexpected foreign key target: %+v", customerIDColumn.ForeignKeys[0])
	}
}

func TestHandleProjectDatabaseUpdateCellSQLiteRejected(t *testing.T) {
	server, projectID, _ := newProjectFileTestServer(t)
	dbResp := createProjectDatabaseFixture(t, server, projectID, CreateProjectDatabaseRequest{
		Name:        "Write DB",
		Engine:      "sqlite",
		DSN:         ":memory:",
		Environment: "Local",
		IsReadOnly:  false,
	})

	body, _ := json.Marshal(ProjectDatabaseUpdateCellRequest{
		Column:     "name",
		Value:      ptrString("next"),
		PrimaryKey: map[string]string{"id": "1"},
	})
	target := "/projects/" + projectID + "/databases/" + dbResp.ID + "/tables/" + url.PathEscape("users") + "/cells"
	req := httptest.NewRequest(http.MethodPatch, target, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for sqlite update, got %d: %s", rec.Code, rec.Body.String())
	}
}

func createProjectDatabaseFixture(t *testing.T, server *Server, projectID string, dbReq CreateProjectDatabaseRequest) ProjectDatabaseResponse {
	t.Helper()
	body, _ := json.Marshal(dbReq)
	createReq := httptest.NewRequest(http.MethodPost, "/projects/"+projectID+"/databases", bytes.NewBuffer(body))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.router.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create project database: %d %s", createRec.Code, createRec.Body.String())
	}
	var dbResp ProjectDatabaseResponse
	if err := json.NewDecoder(createRec.Body).Decode(&dbResp); err != nil {
		t.Fatalf("decode database response: %v", err)
	}
	return dbResp
}

func ptrString(value string) *string {
	return &value
}
