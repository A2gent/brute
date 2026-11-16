package http

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

func TestHandleProjectDatabaseColumnAnalytics(t *testing.T) {
	server, projectID, _ := newProjectFileTestServer(t)
	dbPath := t.TempDir() + "/column-analytics.db"
	fixture, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite fixture: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE airports (id INTEGER PRIMARY KEY, code TEXT NOT NULL)`,
		`CREATE TABLE flights (airport_code TEXT, FOREIGN KEY (airport_code) REFERENCES airports(code))`,
		`INSERT INTO airports (id, code) VALUES (1, 'TLL'), (2, 'RIX')`,
		`INSERT INTO flights (airport_code) VALUES ('TLL'), ('TLL'), ('RIX'), (NULL)`,
	} {
		if _, err := fixture.Exec(statement); err != nil {
			fixture.Close()
			t.Fatalf("execute fixture statement %q: %v", statement, err)
		}
	}
	if err := fixture.Close(); err != nil {
		t.Fatalf("close sqlite fixture: %v", err)
	}

	dbReq := CreateProjectDatabaseRequest{
		Name:        "Analytics DB",
		Engine:      "sqlite",
		DSN:         dbPath,
		Environment: "Local",
		IsReadOnly:  true,
	}
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

	target := "/projects/" + projectID + "/databases/" + dbResp.ID + "/tables/" + url.PathEscape("flights") + "/columns/" + url.PathEscape("airport_code") + "/analytics"
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("column analytics: %d %s", rec.Code, rec.Body.String())
	}
	var response ProjectDatabaseColumnAnalyticsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode analytics response: %v", err)
	}
	if response.TotalRowCount != 4 || response.DistinctCount != 2 || response.NullCount != 1 {
		t.Fatalf("unexpected analytics counts: %+v", response)
	}
	if len(response.TopValues) != 2 || response.TopValues[0].Value != "TLL" || response.TopValues[0].Count != 2 {
		t.Fatalf("unexpected top values: %+v", response.TopValues)
	}
	if len(response.ForeignKeys) != 1 || response.ForeignKeys[0].ReferencedTable != "airports" {
		t.Fatalf("unexpected foreign keys: %+v", response.ForeignKeys)
	}
}

func TestHandleProjectDatabaseColumnAnalyticsRejectsRedis(t *testing.T) {
	server, projectID, _ := newProjectFileTestServer(t)
	dbReq := CreateProjectDatabaseRequest{
		Name:        "Redis",
		Engine:      "redis",
		DSN:         "redis://localhost:6379",
		Environment: "Local",
		IsReadOnly:  true,
	}
	body, _ := json.Marshal(dbReq)
	createReq := httptest.NewRequest(http.MethodPost, "/projects/"+projectID+"/databases", bytes.NewBuffer(body))
	createRec := httptest.NewRecorder()
	server.router.ServeHTTP(createRec, createReq)
	var dbResp ProjectDatabaseResponse
	_ = json.NewDecoder(createRec.Body).Decode(&dbResp)

	req := httptest.NewRequest(http.MethodGet, "/projects/"+projectID+"/databases/"+dbResp.ID+"/tables/key/columns/value/analytics", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected bad request for Redis analytics, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleProjectDatabasePersistsSchema(t *testing.T) {
	server, projectID, _ := newProjectFileTestServer(t)
	body, _ := json.Marshal(CreateProjectDatabaseRequest{
		Name:        "PG",
		Engine:      "postgres",
		DSN:         "postgres://localhost/app",
		Environment: "Local",
		Schema:      " sales ",
	})
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/projects/"+projectID+"/databases", bytes.NewBuffer(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created ProjectDatabaseResponse
	_ = json.NewDecoder(rec.Body).Decode(&created)
	if created.Schema != "sales" {
		t.Fatalf("expected trimmed schema, got %q", created.Schema)
	}

	body, _ = json.Marshal(map[string]string{"name": "PG renamed"})
	rec = httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/projects/"+projectID+"/databases/"+created.ID, bytes.NewBuffer(body)))
	var renamed ProjectDatabaseResponse
	_ = json.NewDecoder(rec.Body).Decode(&renamed)
	if rec.Code != http.StatusOK || renamed.Schema != "sales" {
		t.Fatalf("update without schema should keep it: %d %+v", rec.Code, renamed)
	}

	body, _ = json.Marshal(map[string]string{"schema": "billing"})
	rec = httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/projects/"+projectID+"/databases/"+created.ID, bytes.NewBuffer(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update schema: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/projects/"+projectID+"/databases", nil))
	var listed []ProjectDatabaseResponse
	_ = json.NewDecoder(rec.Body).Decode(&listed)
	if len(listed) != 1 || listed[0].Schema != "billing" {
		t.Fatalf("expected persisted schema billing, got %+v", listed)
	}
}

func TestHandleProjectDatabaseListSchemasSQLiteIsEmpty(t *testing.T) {
	server, projectID, _ := newProjectFileTestServer(t)
	body, _ := json.Marshal(CreateProjectDatabaseRequest{Name: "Lite", Engine: "sqlite", DSN: ":memory:", Environment: "Local"})
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/projects/"+projectID+"/databases", bytes.NewBuffer(body)))
	var created ProjectDatabaseResponse
	_ = json.NewDecoder(rec.Body).Decode(&created)

	rec = httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/projects/"+projectID+"/databases/"+created.ID+"/schemas", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("expected empty schema list, got %d %s", rec.Code, rec.Body.String())
	}
}
