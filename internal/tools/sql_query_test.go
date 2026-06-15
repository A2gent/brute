package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSQLQueryToolExecuteRawSQLiteAppliesPagination(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "items.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite fixture: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatalf("create fixture table: %v", err)
	}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if _, err := db.Exec(`INSERT INTO items (name) VALUES (?)`, name); err != nil {
			t.Fatalf("insert fixture row %q: %v", name, err)
		}
	}

	tool := NewSQLQueryTool(nil)
	result := executeSQLQueryTool(t, tool, map[string]interface{}{
		"dsn":    dbPath,
		"engine": "sqlite",
		"query":  "SELECT name FROM items ORDER BY id",
		"limit":  1,
		"offset": 1,
	})

	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if got, want := result.Output, "name\nbeta\n"; got != want {
		t.Fatalf("unexpected CSV output:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestSQLQueryToolExecuteRejectsInvalidInputsBeforeConnecting(t *testing.T) {
	badDSN := filepath.Join(t.TempDir(), "missing", "db.sqlite")
	tool := NewSQLQueryTool(nil)

	tests := []struct {
		name       string
		params     map[string]interface{}
		wantErrSub string
	}{
		{
			name: "blank query",
			params: map[string]interface{}{
				"dsn":    badDSN,
				"engine": "sqlite",
				"query":  "   ",
			},
			wantErrSub: "query",
		},
		{
			name: "zero limit disables unsafe unbounded queries",
			params: map[string]interface{}{
				"dsn":    badDSN,
				"engine": "sqlite",
				"query":  "SELECT 1",
				"limit":  0,
			},
			wantErrSub: "limit",
		},
		{
			name: "negative offset",
			params: map[string]interface{}{
				"dsn":    badDSN,
				"engine": "sqlite",
				"query":  "SELECT 1",
				"offset": -1,
			},
			wantErrSub: "offset",
		},
		{
			name: "too large limit",
			params: map[string]interface{}{
				"dsn":    badDSN,
				"engine": "sqlite",
				"query":  "SELECT 1",
				"limit":  10000,
			},
			wantErrSub: "limit",
		},
		{
			name: "unsupported output format",
			params: map[string]interface{}{
				"dsn":    badDSN,
				"engine": "sqlite",
				"query":  "SELECT 1",
				"format": "yaml",
			},
			wantErrSub: "format",
		},
		{
			name: "unsupported engine",
			params: map[string]interface{}{
				"dsn":    badDSN,
				"engine": "oracle",
				"query":  "SELECT 1",
			},
			wantErrSub: "engine",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := executeSQLQueryTool(t, tool, tt.params)
			if result.Success {
				t.Fatalf("expected validation failure, got success: %s", result.Output)
			}
			if !strings.Contains(strings.ToLower(result.Error), tt.wantErrSub) {
				t.Fatalf("expected error containing %q, got %q", tt.wantErrSub, result.Error)
			}
		})
	}
}

func TestSQLQueryToolExecuteAllowsRawRedisEngine(t *testing.T) {
	tool := NewSQLQueryTool(nil)
	result := executeSQLQueryTool(t, tool, map[string]interface{}{
		"dsn":    "redis://127.0.0.1:1/0",
		"engine": "redis",
		"query":  "GET session:1",
		"limit":  1,
	})

	if result.Success {
		t.Fatalf("expected connection failure for unavailable redis fixture, got success: %s", result.Output)
	}
	if strings.Contains(strings.ToLower(result.Error), "engine") {
		t.Fatalf("redis should pass engine validation before connecting, got %q", result.Error)
	}
}

func TestSQLQueryToolSchemaAdvertisesValidatedPagination(t *testing.T) {
	tool := NewSQLQueryTool(nil)

	if tool.Name() != "sql_query" {
		t.Fatalf("unexpected tool name: %q", tool.Name())
	}
	if !strings.Contains(tool.Description(), "limit") {
		t.Fatalf("description should mention pagination limit: %q", tool.Description())
	}

	properties, ok := tool.Schema()["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("schema properties missing or wrong type: %#v", tool.Schema()["properties"])
	}
	engineSchema, ok := properties["engine"].(map[string]interface{})
	if !ok {
		t.Fatalf("engine schema missing or wrong type: %#v", properties["engine"])
	}
	engineEnums, ok := engineSchema["enum"].([]string)
	if !ok {
		t.Fatalf("engine enum missing or wrong type: %#v", engineSchema["enum"])
	}
	if !containsString(engineEnums, "redis") {
		t.Fatalf("engine schema should advertise redis support, got %#v", engineEnums)
	}
	limitSchema, ok := properties["limit"].(map[string]interface{})
	if !ok {
		t.Fatalf("limit schema missing or wrong type: %#v", properties["limit"])
	}
	if limitSchema["minimum"] != 1 || limitSchema["maximum"] != 1000 {
		t.Fatalf("limit schema should advertise enforced bounds, got %#v", limitSchema)
	}
	offsetSchema, ok := properties["offset"].(map[string]interface{})
	if !ok {
		t.Fatalf("offset schema missing or wrong type: %#v", properties["offset"])
	}
	if offsetSchema["minimum"] != 0 {
		t.Fatalf("offset schema should advertise non-negative bound, got %#v", offsetSchema)
	}
}

func executeSQLQueryTool(t *testing.T, tool *SQLQueryTool, params map[string]interface{}) *Result {
	t.Helper()

	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Execute returned nil result")
	}
	return result
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
