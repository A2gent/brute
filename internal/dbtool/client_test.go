package dbtool

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
)

func TestSQLExecuteQueryFormatsResultsAndAppliesPagination(t *testing.T) {
	dbPath := createSQLiteFixture(t)

	csvOutput, err := ExecuteQuery(context.Background(), Config{Engine: "sqlite", DSN: dbPath}, "SELECT name, note FROM items ORDER BY id;", 1, 0, "csv")
	if err != nil {
		t.Fatalf("ExecuteQuery CSV returned error: %v", err)
	}
	if got, want := csvOutput, "name,note\nalpha,NULL\n"; got != want {
		t.Fatalf("unexpected CSV output:\ngot:  %q\nwant: %q", got, want)
	}

	jsonOutput, err := ExecuteQuery(context.Background(), Config{Engine: "sqlite", DSN: dbPath}, "SELECT name, note FROM items ORDER BY id", 1, 1, "json")
	if err != nil {
		t.Fatalf("ExecuteQuery JSON returned error: %v", err)
	}
	var parsed []map[string]interface{}
	if err := json.Unmarshal([]byte(jsonOutput), &parsed); err != nil {
		t.Fatalf("JSON output is not valid: %v\noutput: %s", err, jsonOutput)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected one row, got %#v", parsed)
	}
	if got, want := parsed[0]["name"], `beta"quoted`; got != want {
		t.Fatalf("unexpected name value: got %#v want %#v", got, want)
	}
	if got, want := parsed[0]["note"], `line\break`; got != want {
		t.Fatalf("unexpected note value: got %#v want %#v", got, want)
	}
}

func TestSQLExecuteQueryJSONEscapesControlCharacters(t *testing.T) {
	dbPath := createSQLiteFixture(t)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite fixture: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO items (name, note) VALUES (?, ?)`, "control", "line1\nline2\tend"); err != nil {
		t.Fatalf("insert control-character row: %v", err)
	}
	_ = db.Close()

	jsonOutput, err := ExecuteQuery(context.Background(), Config{Engine: "sqlite", DSN: dbPath}, `SELECT name, note FROM items WHERE name = 'control'`, 1, 0, "json")
	if err != nil {
		t.Fatalf("ExecuteQuery JSON returned error: %v", err)
	}

	var parsed []map[string]interface{}
	if err := json.Unmarshal([]byte(jsonOutput), &parsed); err != nil {
		t.Fatalf("JSON output is not valid: %v\noutput: %s", err, jsonOutput)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected one row, got %#v", parsed)
	}
	if got, want := parsed[0]["note"], "line1\nline2\tend"; got != want {
		t.Fatalf("unexpected note value: got %#v want %#v", got, want)
	}
}

func TestSQLMetadataAndValidationErrors(t *testing.T) {
	dbPath := createSQLiteFixture(t)

	tables, err := GetTables(context.Background(), Config{Engine: "sqlite", DSN: dbPath})
	if err != nil {
		t.Fatalf("GetTables returned error: %v", err)
	}
	if got, want := strings.Join(tables, ","), "items"; got != want {
		t.Fatalf("unexpected tables: got %q want %q", got, want)
	}

	if _, err := Connect(Config{Engine: "oracle", DSN: dbPath}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported engine error, got %v", err)
	}
	if _, err := GetTables(context.Background(), Config{Engine: "oracle", DSN: dbPath}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported GetTables error, got %v", err)
	}
	if _, err := ExecuteQuery(context.Background(), Config{Engine: "sqlite", DSN: dbPath}, "SELECT 1; SELECT 2", 10, 0, "csv"); err == nil || !strings.Contains(err.Error(), "multiple statements") {
		t.Fatalf("expected multiple statements error, got %v", err)
	}
	if _, err := ExecuteQuery(context.Background(), Config{Engine: "sqlite", DSN: dbPath, IsReadOnly: true}, "DROP TABLE items", 10, 0, "csv"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected read-only modification error, got %v", err)
	}
	if _, err := ExecuteQuery(context.Background(), Config{Engine: "sqlite", DSN: dbPath}, "SELECT 1", 10, 0, "xml"); err == nil || !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("expected unsupported format error, got %v", err)
	}
}

func TestRedisExecuteQueryPreviewsCompositeKeyTypes(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		keyType  string
		limit    int
		offset   int
		format   string
		want     []string
		forbid   []string
		handlers map[string]func(args []string, writer *bufio.Writer)
	}{
		{
			name:    "hash applies pair pagination",
			key:     "hash:1",
			keyType: "hash",
			limit:   1,
			offset:  1,
			format:  "json",
			want:    []string{`"field": "second"`, `"value": "two"`},
			forbid:  []string{`"field": "first"`},
			handlers: map[string]func(args []string, writer *bufio.Writer){
				"HGETALL": func(args []string, writer *bufio.Writer) {
					writeTestRedisArrayResponse(writer, "first", "one", "second", "two")
				},
			},
		},
		{
			name:    "list returns requested index range",
			key:     "list:1",
			keyType: "list",
			limit:   2,
			offset:  1,
			format:  "csv",
			want:    []string{"key,type,index,value,ttl", "list:1,list,1,b,-1", "list:1,list,2,c,-1"},
			handlers: map[string]func(args []string, writer *bufio.Writer){
				"LRANGE": func(args []string, writer *bufio.Writer) {
					if got := strings.Join(args, " "); !strings.Contains(got, "1 2") {
						writeTestRedisError(writer, "ERR unexpected LRANGE bounds")
						return
					}
					writeTestRedisArrayResponse(writer, "b", "c")
				},
			},
		},
		{
			name:    "set sorts members before pagination",
			key:     "set:1",
			keyType: "set",
			limit:   1,
			offset:  1,
			format:  "json",
			want:    []string{`"index": 1`, `"value": "b"`},
			forbid:  []string{`"value": "a"`, `"value": "c"`},
			handlers: map[string]func(args []string, writer *bufio.Writer){
				"SMEMBERS": func(args []string, writer *bufio.Writer) {
					writeTestRedisArrayResponse(writer, "c", "a", "b")
				},
			},
		},
		{
			name:    "zset keeps scores",
			key:     "zset:1",
			keyType: "zset",
			limit:   2,
			offset:  0,
			format:  "json",
			want:    []string{`"value": "alice"`, `"score": "42"`, `"value": "bob"`, `"score": "99"`},
			handlers: map[string]func(args []string, writer *bufio.Writer){
				"ZRANGE": func(args []string, writer *bufio.Writer) {
					writeTestRedisArrayResponse(writer, "alice", "42", "bob", "99")
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newScriptedRedisServer(t, func(args []string, writer *bufio.Writer) {
				if len(args) == 0 {
					writeTestRedisError(writer, "ERR empty command")
					return
				}

				switch command := strings.ToUpper(args[0]); command {
				case "PING", "SELECT":
					writeTestRedisSimpleString(writer, "OK")
				case "TYPE":
					writeTestRedisSimpleString(writer, tt.keyType)
				case "TTL":
					writeTestRedisInteger(writer, -1)
				default:
					handler := tt.handlers[command]
					if handler == nil {
						writeTestRedisError(writer, "ERR unexpected command "+command)
						return
					}
					handler(args, writer)
				}
			})

			output, err := ExecuteQuery(context.Background(), Config{Engine: "redis", DSN: server.dsn, IsReadOnly: true}, "GET "+tt.key, tt.limit, tt.offset, tt.format)
			if err != nil {
				t.Fatalf("ExecuteQuery returned error: %v", err)
			}
			for _, expected := range tt.want {
				if !strings.Contains(output, expected) {
					t.Fatalf("expected %q in output: %s", expected, output)
				}
			}
			for _, forbidden := range tt.forbid {
				if strings.Contains(output, forbidden) {
					t.Fatalf("did not expect %q in output: %s", forbidden, output)
				}
			}
		})
	}
}

func TestRedisParsingHelpers(t *testing.T) {
	args, err := parseRedisCommand(`MGET "session one" 'session two' escaped\ key trailing\`)
	if err != nil {
		t.Fatalf("parseRedisCommand returned error: %v", err)
	}
	if want := []string{"MGET", "session one", "session two", "escaped key", "trailing\\"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("unexpected parsed args: got %#v want %#v", args, want)
	}
	if _, err := parseRedisCommand(`GET "unterminated`); err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("expected unterminated quote error, got %v", err)
	}

	parsed, err := parseRedisDSN("rediss://user:pass@example.com:6380/3")
	if err != nil {
		t.Fatalf("parseRedisDSN returned error: %v", err)
	}
	if parsed.address != "example.com:6380" || parsed.username != "user" || parsed.password != "pass" || parsed.database != 3 || !parsed.useTLS {
		t.Fatalf("unexpected parsed DSN: %#v", parsed)
	}
	passwordOnly, err := parseRedisDSN("redis://secret@localhost/2")
	if err != nil {
		t.Fatalf("parseRedisDSN password-only returned error: %v", err)
	}
	if passwordOnly.address != "localhost:6379" || passwordOnly.username != "" || passwordOnly.password != "secret" || passwordOnly.database != 2 || passwordOnly.useTLS {
		t.Fatalf("unexpected password-only DSN: %#v", passwordOnly)
	}
	plain, err := parseRedisDSN("localhost")
	if err != nil {
		t.Fatalf("parseRedisDSN plain host returned error: %v", err)
	}
	if plain.address != "localhost:6379" {
		t.Fatalf("unexpected plain host address: %#v", plain)
	}

	for _, dsn := range []string{"", "http://localhost", "redis://localhost/not-a-number", "redis://localhost/-1"} {
		if _, err := parseRedisDSN(dsn); err == nil {
			t.Fatalf("expected parseRedisDSN(%q) to fail", dsn)
		}
	}
}

func TestRedisFormattingHelpers(t *testing.T) {
	rows := redisResponseRows("MGET", []interface{}{"alpha", nil, int64(7), []interface{}{"nested", "value"}}, 3, 1)
	if len(rows) != 3 || rows[0]["index"] != 1 || rows[0]["result"] != nil || rows[2]["index"] != 3 {
		t.Fatalf("unexpected paginated rows: %#v", rows)
	}

	csvOutput, err := formatRedisRows(rows, "csv")
	if err != nil {
		t.Fatalf("formatRedisRows CSV returned error: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(csvOutput)).ReadAll()
	if err != nil {
		t.Fatalf("parse formatted CSV: %v", err)
	}
	wantRecords := [][]string{
		{"index", "command", "result"},
		{"1", "MGET", "NULL"},
		{"2", "MGET", "7"},
		{"3", "MGET", `["nested","value"]`},
	}
	if !reflect.DeepEqual(records, wantRecords) {
		t.Fatalf("unexpected CSV records:\ngot:  %#v\nwant: %#v", records, wantRecords)
	}

	jsonOutput, err := formatRedisRows(rows, "json")
	if err != nil {
		t.Fatalf("formatRedisRows JSON returned error: %v", err)
	}
	if !strings.Contains(jsonOutput, `"result": null`) || !strings.Contains(jsonOutput, `[\"nested\",\"value\"]`) {
		t.Fatalf("unexpected JSON output: %s", jsonOutput)
	}
	if _, err := formatRedisRows(rows, "yaml"); err == nil || !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("expected unsupported format error, got %v", err)
	}

	columns := redisColumns([]map[string]interface{}{{"score": "42", "z_extra": true, "key": "k", "a_extra": 1}})
	if want := []string{"key", "score", "a_extra", "z_extra"}; !reflect.DeepEqual(columns, want) {
		t.Fatalf("unexpected redis columns: got %#v want %#v", columns, want)
	}
	if got, want := redisCellString(map[string]int{"a": 1}), `{"a":1}`; got != want {
		t.Fatalf("unexpected map cell: got %q want %q", got, want)
	}
	if got, want := redisCellString(true), "true"; got != want {
		t.Fatalf("unexpected bool cell: got %q want %q", got, want)
	}
}

func TestRedisStreamAndRangeHelpers(t *testing.T) {
	start, end := boundedRange(3, 10, 1)
	if start != 1 || end != 3 {
		t.Fatalf("boundedRange clipped end incorrectly: got (%d,%d)", start, end)
	}
	start, end = boundedRange(3, 1, 5)
	if start != 3 || end != 3 {
		t.Fatalf("boundedRange clipped offset incorrectly: got (%d,%d)", start, end)
	}

	rows := redisStreamRows(func() map[string]interface{} {
		return map[string]interface{}{"key": "stream:1", "type": "stream"}
	}, []interface{}{
		[]interface{}{"1680000000000-0", []interface{}{"event", "created", "user", "alice"}},
		"malformed entry",
	})
	if len(rows) != 1 {
		t.Fatalf("expected one stream row, got %#v", rows)
	}
	if rows[0]["id"] != "1680000000000-0" || rows[0]["index"] != 0 || !strings.Contains(fmt.Sprint(rows[0]["value"]), `"user":"alice"`) {
		t.Fatalf("unexpected stream row: %#v", rows[0])
	}

	if !isRedisNil(redisError("nil")) {
		t.Fatal("expected redis nil error to be recognized")
	}
	if isRedisNil(fmt.Errorf("nil")) {
		t.Fatal("plain nil text should not be treated as redis nil error")
	}
}

func createSQLiteFixture(t *testing.T) string {
	t.Helper()

	dbPath := t.TempDir() + "/items.db"
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite fixture: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL, note TEXT)`); err != nil {
		t.Fatalf("create fixture table: %v", err)
	}
	fixtures := []struct {
		name string
		note interface{}
	}{
		{name: "alpha", note: nil},
		{name: `beta"quoted`, note: `line\break`},
	}
	for _, fixture := range fixtures {
		if _, err := db.Exec(`INSERT INTO items (name, note) VALUES (?, ?)`, fixture.name, fixture.note); err != nil {
			t.Fatalf("insert fixture row %q: %v", fixture.name, err)
		}
	}
	return dbPath
}

func newScriptedRedisServer(t *testing.T, handler func(args []string, writer *bufio.Writer)) testRedisServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleScriptedRedisConn(conn, handler)
		}
	}()

	return testRedisServer{dsn: "redis://" + listener.Addr().String() + "/0"}
}

func handleScriptedRedisConn(conn net.Conn, handler func(args []string, writer *bufio.Writer)) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	for {
		args, err := readTestRedisArray(reader)
		if err != nil {
			return
		}
		handler(args, writer)
	}
}

func writeTestRedisInteger(writer *bufio.Writer, value int64) {
	_, _ = writer.WriteString(fmt.Sprintf(":%d\r\n", value))
	_ = writer.Flush()
}

func writeTestRedisArrayResponse(writer *bufio.Writer, values ...interface{}) {
	writeTestRedisValueNoFlush(writer, values)
	_ = writer.Flush()
}

func writeTestRedisValueNoFlush(writer *bufio.Writer, value interface{}) {
	switch typed := value.(type) {
	case nil:
		_, _ = writer.WriteString("$-1\r\n")
	case string:
		writeTestRedisBulkStringNoFlush(writer, typed)
	case int:
		_, _ = writer.WriteString(fmt.Sprintf(":%d\r\n", typed))
	case int64:
		_, _ = writer.WriteString(fmt.Sprintf(":%d\r\n", typed))
	case []interface{}:
		_, _ = writer.WriteString(fmt.Sprintf("*%d\r\n", len(typed)))
		for _, item := range typed {
			writeTestRedisValueNoFlush(writer, item)
		}
	case []string:
		_, _ = writer.WriteString(fmt.Sprintf("*%d\r\n", len(typed)))
		for _, item := range typed {
			writeTestRedisBulkStringNoFlush(writer, item)
		}
	case []map[string]interface{}:
		_, _ = writer.WriteString(fmt.Sprintf("*%d\r\n", len(typed)))
		for _, item := range typed {
			writeTestRedisValueNoFlush(writer, item)
		}
	default:
		writeTestRedisBulkStringNoFlush(writer, fmt.Sprint(typed))
	}
}
