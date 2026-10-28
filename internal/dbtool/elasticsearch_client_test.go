package dbtool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestElasticsearchServer fakes the handful of endpoints the explorer uses.
func newTestElasticsearchServer(t *testing.T, indices map[string][]map[string]any) string {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"cluster_name":"test-cluster","version":{"number":"8.13.0"}}`)
	})
	mux.HandleFunc("/_alias", func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]any{}
		for index := range indices {
			payload[index] = map[string]any{"aliases": map[string]any{}}
		}
		payload[".kibana"] = map[string]any{"aliases": map[string]any{}}
		_ = json.NewEncoder(w).Encode(payload)
	})
	for index, documents := range indices {
		index := index
		documents := documents
		mux.HandleFunc("/"+index+"/_search", func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			from := int(numberOrZero(body["from"]))
			size := len(documents)
			if value, ok := body["size"].(float64); ok {
				size = int(value)
			}
			hits := make([]any, 0, len(documents))
			for position, document := range documents {
				if position < from || position >= from+size {
					continue
				}
				hits = append(hits, map[string]any{
					"_index":  index,
					"_id":     document["id"],
					"_source": document,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"hits": map[string]any{"total": map[string]any{"value": len(documents)}, "hits": hits},
			})
		})
	}

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL
}

func numberOrZero(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	}
	return 0
}

func TestElasticsearchListsIndicesAndHidesSystemOnes(t *testing.T) {
	dsn := newTestElasticsearchServer(t, map[string][]map[string]any{
		"logs-2026": {{"id": "1", "message": "started"}},
		"users":     {{"id": "2", "name": "Alice"}},
	})

	names, err := GetTables(context.Background(), Config{Engine: "elasticsearch", DSN: dsn})
	if err != nil {
		t.Fatalf("GetTables returned error: %v", err)
	}
	if got, want := strings.Join(names, ","), "logs-2026,users"; got != want {
		t.Fatalf("unexpected indices: got %q want %q", got, want)
	}
}

func TestElasticsearchFallsBackToCatIndicesWhenAliasApiIsForbidden(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"cluster_name":"f","version":{"number":"8.13.0"}}`)
	})
	mux.HandleFunc("/_alias", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"reason":"action [indices:admin/aliases/get] is unauthorized"}}`)
	})
	mux.HandleFunc("/_cat/indices", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"index":".kibana_1"},{"index":"orders"},{"index":"audit"}]`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	names, err := GetTables(context.Background(), Config{Engine: "elasticsearch", DSN: server.URL})
	if err != nil {
		t.Fatalf("GetTables returned error: %v", err)
	}
	if got, want := strings.Join(names, ","), "audit,orders"; got != want {
		t.Fatalf("unexpected fallback indices: got %q want %q", got, want)
	}
}

func TestElasticsearchQueryFlattensHitsAndPaginates(t *testing.T) {
	dsn := newTestElasticsearchServer(t, map[string][]map[string]any{
		"users": {
			{"id": "1", "name": "Alice", "address": map[string]any{"city": "Tallinn"}},
			{"id": "2", "name": "Bob"},
		},
	})

	output, err := ExecuteQuery(
		context.Background(),
		Config{Engine: "elasticsearch", DSN: dsn, IsReadOnly: true},
		`POST users/_search {"query":{"match_all":{}}}`,
		1,
		1,
		"json",
	)
	if err != nil {
		t.Fatalf("ExecuteQuery returned error: %v", err)
	}

	var rows []map[string]any
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		t.Fatalf("result is not valid JSON: %v\n%s", err, output)
	}
	if len(rows) != 1 || rows[0]["name"] != "Bob" || rows[0]["_id"] != "2" {
		t.Fatalf("unexpected paginated rows: %#v", rows)
	}
}

func TestElasticsearchQueryFlattensNestedObjectsIntoColumns(t *testing.T) {
	dsn := newTestElasticsearchServer(t, map[string][]map[string]any{
		"users": {{"id": "1", "address": map[string]any{"city": "Tallinn", "zip": "10111"}}},
	})

	output, err := ExecuteQuery(context.Background(), Config{Engine: "elasticsearch", DSN: dsn}, `GET users/_search`, 10, 0, "csv")
	if err != nil {
		t.Fatalf("ExecuteQuery returned error: %v", err)
	}
	if !strings.Contains(output, "address.city") || !strings.Contains(output, "Tallinn") {
		t.Fatalf("expected flattened columns in CSV, got:\n%s", output)
	}
}

func TestElasticsearchReadOnlyRejectsEncodedAndFragmentBypasses(t *testing.T) {
	// WHY: Elasticsearch decodes path segments before routing and browsers strip the
	// fragment, so `%5Fbulk`, `_bulk#x` and `_doc%2F1` all reach a write endpoint.
	cfg := Config{Engine: "elasticsearch", DSN: "http://127.0.0.1:1", IsReadOnly: true}
	for _, query := range []string{
		`POST _bulk#x {}`,
		`POST %5Fbulk {}`,
		`POST logs/%5Fdoc/1 {"a":1}`,
		`POST logs/_doc%2F1 {"a":1}`,
		`POST _aliases {"actions":[{"remove_index":{"index":"logs"}}]}`,
		`POST _cluster/settings {"persistent":{"x":1}}`,
		`POST _scripts/my-script {"script":{"lang":"painless","source":"1"}}`,
		`POST _snapshot/repo/snap/_restore`,
		`POST logs/_mapping {"properties":{}}`,
		`POST _ilm/policy/p {"policy":{}}`,
	} {
		if _, err := ExecuteQuery(context.Background(), cfg, query, 10, 0, "json"); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("expected read-only error for %q, got %v", query, err)
		}
	}
}

func TestElasticsearchReadOnlyAllowsReadEndpoints(t *testing.T) {
	cfg := Config{Engine: "elasticsearch", DSN: "http://127.0.0.1:1", IsReadOnly: true}
	for _, query := range []string{
		`POST logs/_search {}`,
		`POST _msearch`,
		`POST logs/_mget {"ids":["1"]}`,
		`GET logs/_mapping`,
		`GET logs/_stats`,
	} {
		_, err := ExecuteQuery(context.Background(), cfg, query, 10, 0, "json")
		// The connection is refused afterwards; only the guard must pass.
		if err != nil && strings.Contains(err.Error(), "read-only") {
			t.Fatalf("read endpoint %q must be allowed, got %v", query, err)
		}
	}
}

func TestElasticsearchRejectsNonObjectBodies(t *testing.T) {
	// WHY: `null` is valid JSON but unmarshals to a nil map, which panicked when paging
	// fields were injected. Non-object bodies must be rejected before reaching that code.
	cfg := Config{Engine: "elasticsearch", DSN: "http://127.0.0.1:1"}
	for _, query := range []string{
		`POST logs/_search null`,
		`POST logs/_search [1,2]`,
	} {
		if _, err := ExecuteQuery(context.Background(), cfg, query, 10, 0, "json"); err == nil || !strings.Contains(err.Error(), "JSON object") {
			t.Fatalf("expected a JSON object error for %q, got %v", query, err)
		}
	}
}

func TestElasticsearchAggregationResultsAreKeptAlongsideHits(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"hits":{"hits":[]},"aggregations":{"by_level":{"buckets":[{"key":"info","doc_count":3}]}}}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	output, err := ExecuteQuery(
		context.Background(),
		Config{Engine: "elasticsearch", DSN: server.URL},
		`POST logs/_search {"size":0,"aggs":{"by_level":{"terms":{"field":"level"}}}}`,
		10,
		0,
		"json",
	)
	if err != nil {
		t.Fatalf("ExecuteQuery returned error: %v", err)
	}
	// A _search response always has hits, so aggregations must survive as an extra row.
	if !strings.Contains(output, "by_level.buckets") || !strings.Contains(output, "doc_count") {
		t.Fatalf("aggregation result was dropped:\n%s", output)
	}
}

func TestElasticsearchKeepsLargeNumbersExact(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// Echo back the re-encoded request so the test sees what the client sent.
		if len(body) > 0 {
			_, _ = io.WriteString(w, `{"hits":{"hits":[{"_id":"1","_source":`+string(body)+`}]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"hits":{"hits":[]}}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	// WHY: decoding the body into map[string]any rounded this id to ...917000.
	output, err := ExecuteQuery(
		context.Background(),
		Config{Engine: "elasticsearch", DSN: server.URL},
		`POST logs/_search {"query":{"term":{"big":12345678901234567890}}}`,
		10,
		0,
		"csv",
	)
	if err != nil {
		t.Fatalf("ExecuteQuery returned error: %v", err)
	}
	if !strings.Contains(output, "12345678901234567890") {
		t.Fatalf("large number lost precision:\n%s", output)
	}
}

func TestElasticsearchReadOnlyAllowsSearchOnIndicesWithEndpointLikeNames(t *testing.T) {
	dsn := newTestElasticsearchServer(t, map[string][]map[string]any{
		"my_docs": {{"id": "1", "message": "kept"}},
	})
	// WHY: the read-only guard matches whole path segments, so index names that merely
	// contain "_doc"/"_bulk" must not be mistaken for write endpoints.
	output, err := ExecuteQuery(
		context.Background(),
		Config{Engine: "elasticsearch", DSN: dsn, IsReadOnly: true},
		`POST my_docs/_search {"query":{"match_all":{}}}`,
		10,
		0,
		"json",
	)
	if err != nil {
		t.Fatalf("ExecuteQuery returned error: %v", err)
	}
	if !strings.Contains(output, "kept") {
		t.Fatalf("unexpected rows: %s", output)
	}
}

func TestElasticsearchReadOnlyRejectsWriteRequestsBeforeConnecting(t *testing.T) {
	cfg := Config{Engine: "elasticsearch", DSN: "http://127.0.0.1:1", IsReadOnly: true}
	for _, query := range []string{
		`POST logs/_doc {"message":"new"}`,
		`POST logs/_doc/1 {"message":"new"}`,
		`DELETE logs/_doc/1`,
		`POST _bulk {"index":{}}`,
		`POST logs/_update_by_query {"query":{"match_all":{}}}`,
	} {
		if _, err := ExecuteQuery(context.Background(), cfg, query, 10, 0, "json"); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("expected read-only error for %q, got %v", query, err)
		}
	}
}

func TestElasticsearchCountResponseKeepsTheCountColumn(t *testing.T) {
	rows := elasticsearchResponseRows([]byte(`{"count":42,"_shards":{"total":1,"successful":1,"skipped":0,"failed":0}}`))
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %#v", rows)
	}
	if got, want := len(rows[0]), 1; got != want {
		t.Fatalf("expected only the count column, got %#v", rows[0])
	}
	if value, ok := rows[0]["count"]; !ok || numberOrZero(value) != 42 {
		t.Fatalf("unexpected count value: %#v", rows[0])
	}
}

func TestElasticsearchAggregationResponseBecomesSingleRow(t *testing.T) {
	dsn := newTestElasticsearchServer(t, map[string][]map[string]any{"users": {}})

	output, err := ExecuteQuery(
		context.Background(),
		Config{Engine: "elasticsearch", DSN: dsn},
		`POST users/_search {"size":0,"aggs":{"by_city":{"terms":{"field":"city"}}}}`,
		10,
		0,
		"json",
	)
	if err != nil {
		t.Fatalf("ExecuteQuery returned error: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		t.Fatalf("result is not valid JSON: %v\n%s", err, output)
	}
	// The fake server only answers hits, so an empty result set is expected; this
	// guards the hit-shaping path against panics on aggregation-shaped bodies.
	if len(rows) != 0 {
		t.Fatalf("expected no rows, got %#v", rows)
	}
}

func TestElasticsearchPaginationSkipsNonPlainSearchEndpoints(t *testing.T) {
	cases := []struct {
		path       string
		wantPaging bool
	}{
		{path: "logs/_search", wantPaging: true},
		{path: "logs/_count", wantPaging: false},
		{path: "_search", wantPaging: true},
		// Scroll and template endpoints take their own body shapes; injecting size/from breaks them.
		{path: "_search/scroll", wantPaging: false},
		{path: "_search/template", wantPaging: false},
		{path: "logs/_search?scroll=1m", wantPaging: false},
	}

	for _, testCase := range cases {
		request, err := withElasticsearchPagination(elasticsearchRequest{method: http.MethodGet, path: testCase.path, body: []byte(`{}`)}, 10, 20)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", testCase.path, err)
		}
		var body map[string]any
		if err := json.Unmarshal(request.body, &body); err != nil {
			t.Fatalf("body is not JSON for %q: %v", testCase.path, err)
		}
		_, hasSize := body["size"]
		if hasSize != testCase.wantPaging {
			t.Fatalf("%q: paging injected = %v, want %v (%s)", testCase.path, hasSize, testCase.wantPaging, request.body)
		}
	}
}

func TestElasticsearchWorksWhenRootEndpointIsForbidden(t *testing.T) {
	// WHY: index-scoped API keys usually cannot read GET / (cluster monitor). Probing the
	// root would turn a working connection into "unauthorized" in the explorer.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"reason":"missing authentication credentials"}}`)
	})
	mux.HandleFunc("/_alias", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"orders":{"aliases":{}}}`)
	})
	mux.HandleFunc("/orders/_search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"hits":{"hits":[{"_index":"orders","_id":"1","_source":{"total":10}}]}}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	cfg := Config{Engine: "elasticsearch", DSN: server.URL}

	names, err := GetTables(context.Background(), cfg)
	if err != nil {
		t.Fatalf("GetTables returned error: %v", err)
	}
	if got, want := strings.Join(names, ","), "orders"; got != want {
		t.Fatalf("unexpected indices: got %q want %q", got, want)
	}

	output, err := ExecuteQuery(context.Background(), cfg, `GET orders/_search`, 10, 0, "json")
	if err != nil {
		t.Fatalf("ExecuteQuery returned error: %v", err)
	}
	if !strings.Contains(output, `"total": 10`) {
		t.Fatalf("unexpected rows: %s", output)
	}
}

func TestConnectElasticsearchReportsReachabilityWithoutMonitorRights(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"reason":"action [cluster:monitor/main] is unauthorized"}}`)
	})
	mux.HandleFunc("/_alias", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"orders":{"aliases":{}}}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	_, detail, err := connectElasticsearch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("expected the alias fallback to succeed, got %v", err)
	}
	if !strings.Contains(detail, "reachable") {
		t.Fatalf("unexpected detail: %q", detail)
	}

	// A cluster that answers neither endpoint must still fail the connection test.
	deadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"reason":"missing authentication credentials"}}`)
	}))
	t.Cleanup(deadServer.Close)
	if _, _, err := connectElasticsearch(context.Background(), deadServer.URL); err == nil {
		t.Fatal("expected an authentication error")
	}
}

func TestParseElasticsearchRequestVariants(t *testing.T) {
	cases := []struct {
		query      string
		method     string
		path       string
		wantBody   bool
		wantErrSub string
	}{
		{query: `GET logs/_search {"size":1}`, method: http.MethodGet, path: "logs/_search", wantBody: true},
		{query: `{"query":{"match_all":{}}}`, method: http.MethodGet, path: "_search", wantBody: true},
		{query: `logs/_stats`, method: http.MethodGet, path: "logs/_stats"},
		{query: `get`, method: "", path: "", wantErrSub: "path"},
		{query: `GET logs/_search {broken}`, wantErrSub: "JSON"},
	}

	for _, testCase := range cases {
		request, err := parseElasticsearchRequest(testCase.query)
		if testCase.wantErrSub != "" {
			if err == nil || !strings.Contains(err.Error(), testCase.wantErrSub) {
				t.Fatalf("expected error containing %q for %q, got %v", testCase.wantErrSub, testCase.query, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", testCase.query, err)
		}
		if request.method != testCase.method || request.path != testCase.path {
			t.Fatalf("unexpected parse of %q: %+v", testCase.query, request)
		}
		if testCase.wantBody != (len(request.body) > 0) {
			t.Fatalf("unexpected body for %q: %q", testCase.query, string(request.body))
		}
	}
}

func TestParseElasticsearchDSN(t *testing.T) {
	conn, err := parseElasticsearchDSN("elasticsearch://user:pass@es.example.com:9200/proxy?api_key=abc")
	if err != nil {
		t.Fatalf("expected elasticsearch:// scheme to be accepted, got %v", err)
	}
	if conn.baseURL != "http://es.example.com:9200/proxy" {
		t.Fatalf("elasticsearch:// should normalise to http, got %q", conn.baseURL)
	}
	if conn.username != "user" || conn.password != "pass" || conn.apiKey != "abc" {
		t.Fatalf("unexpected credentials: %+v", conn)
	}
}

func TestParseElasticsearchDSNDefaults(t *testing.T) {
	conn, err := parseElasticsearchDSN("localhost")
	if err != nil {
		t.Fatalf("parseElasticsearchDSN returned error: %v", err)
	}
	if conn.baseURL != "http://localhost:9200" {
		t.Fatalf("unexpected base URL: %q", conn.baseURL)
	}
	if conn.username != "" || conn.apiKey != "" {
		t.Fatalf("expected no credentials, got %+v", conn)
	}

	secure, err := parseElasticsearchDSN("https://elastic:s3cret@es.example.com?insecure=true")
	if err != nil {
		t.Fatalf("parseElasticsearchDSN returned error: %v", err)
	}
	if secure.baseURL != "https://es.example.com:9200" || secure.username != "elastic" || !secure.insecure {
		t.Fatalf("unexpected secure connection: %+v", secure)
	}

	if _, err := parseElasticsearchDSN(""); err == nil {
		t.Fatal("expected empty DSN error")
	}
}
