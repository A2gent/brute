package http

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newElasticsearchFixture starts a fake Elasticsearch with one index and returns its URL.
func newElasticsearchFixture(t *testing.T) string {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"cluster_name":"fixture","version":{"number":"8.13.0"}}`)
	})
	mux.HandleFunc("/_alias", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"events-2026":{"aliases":{}}}`)
	})
	mux.HandleFunc("/events-2026/_search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"hits":{"total":{"value":1},"hits":[{"_index":"events-2026","_id":"a1","_source":{"level":"info","message":"started"}}]}}`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL
}

func TestHandleProjectDatabaseElasticsearchListsIndicesAndQueries(t *testing.T) {
	server, projectID, _ := newProjectFileTestServer(t)
	dbResp := createProjectDatabaseFixture(t, server, projectID, CreateProjectDatabaseRequest{
		Name:        "Search",
		Engine:      "elasticsearch",
		DSN:         newElasticsearchFixture(t),
		Environment: "Local",
		IsReadOnly:  true,
	})

	base := "/projects/" + projectID + "/databases/" + dbResp.ID

	tablesRec := httptest.NewRecorder()
	server.router.ServeHTTP(tablesRec, httptest.NewRequest(http.MethodGet, base+"/tables", nil))
	if tablesRec.Code != http.StatusOK {
		t.Fatalf("list tables: %d %s", tablesRec.Code, tablesRec.Body.String())
	}
	var tables []ProjectDatabaseTableResponse
	if err := json.NewDecoder(tablesRec.Body).Decode(&tables); err != nil {
		t.Fatalf("decode tables: %v", err)
	}
	if len(tables) != 1 || tables[0].Name != "events-2026" {
		t.Fatalf("unexpected indices: %+v", tables)
	}

	body := strings.NewReader(`{"query":"POST events-2026/_search {}","limit":10}`)
	queryRec := httptest.NewRecorder()
	queryReq := httptest.NewRequest(http.MethodPost, base+"/query", body)
	queryReq.Header.Set("Content-Type", "application/json")
	server.router.ServeHTTP(queryRec, queryReq)
	if queryRec.Code != http.StatusOK {
		t.Fatalf("query: %d %s", queryRec.Code, queryRec.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(queryRec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode rows: %v\n%s", err, queryRec.Body.String())
	}
	if len(rows) != 1 || rows[0]["message"] != "started" || rows[0]["_id"] != "a1" {
		t.Fatalf("unexpected rows: %#v", rows)
	}
}

func TestHandleProjectDatabaseElasticsearchRejectsSchemaAndAnalytics(t *testing.T) {
	server, projectID, _ := newProjectFileTestServer(t)
	dbResp := createProjectDatabaseFixture(t, server, projectID, CreateProjectDatabaseRequest{
		Name:        "Search",
		Engine:      "elasticsearch",
		DSN:         "http://127.0.0.1:1",
		Environment: "Local",
		IsReadOnly:  true,
	})
	base := "/projects/" + projectID + "/databases/" + dbResp.ID

	for _, target := range []string{
		base + "/tables/events-2026/schema",
		base + "/tables/events-2026/columns/message/analytics",
	} {
		rec := httptest.NewRecorder()
		server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected bad request, got %d: %s", target, rec.Code, rec.Body.String())
		}
	}
}
