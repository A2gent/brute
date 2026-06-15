package dbtool

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"testing"
)

func TestRedisGetTablesListsKeys(t *testing.T) {
	server := newTestRedisServer(t, map[string]string{
		"session:1": "alpha",
		"session:2": "beta",
	})

	keys, err := GetTables(context.Background(), Config{Engine: "redis", DSN: server.dsn})
	if err != nil {
		t.Fatalf("GetTables returned error: %v", err)
	}

	if got, want := strings.Join(keys, ","), "session:1,session:2"; got != want {
		t.Fatalf("unexpected redis keys: got %q want %q", got, want)
	}
}

func TestRedisExecuteQueryGetsStringValueAsJSON(t *testing.T) {
	server := newTestRedisServer(t, map[string]string{"session:1": "alpha"})

	result, err := ExecuteQuery(context.Background(), Config{Engine: "redis", DSN: server.dsn, IsReadOnly: true}, "GET session:1", 50, 0, "json")
	if err != nil {
		t.Fatalf("ExecuteQuery returned error: %v", err)
	}

	for _, expected := range []string{`"key": "session:1"`, `"type": "string"`, `"value": "alpha"`} {
		if !strings.Contains(result, expected) {
			t.Fatalf("expected %s in result: %s", expected, result)
		}
	}
}

func TestRedisReadOnlyRejectsWriteCommandsBeforeConnecting(t *testing.T) {
	_, err := ExecuteQuery(context.Background(), Config{Engine: "redis", DSN: "redis://127.0.0.1:1", IsReadOnly: true}, "SET session:1 alpha", 50, 0, "json")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "read-only") {
		t.Fatalf("expected read-only error, got %v", err)
	}
}

type testRedisServer struct {
	dsn string
}

func newTestRedisServer(t *testing.T, values map[string]string) testRedisServer {
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
			go handleTestRedisConn(conn, values)
		}
	}()

	return testRedisServer{dsn: "redis://" + listener.Addr().String() + "/0"}
}

func handleTestRedisConn(conn net.Conn, values map[string]string) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	for {
		args, err := readTestRedisArray(reader)
		if err != nil {
			return
		}
		if len(args) == 0 {
			writeTestRedisError(writer, "ERR empty command")
			continue
		}

		switch strings.ToUpper(args[0]) {
		case "PING":
			writeTestRedisSimpleString(writer, "PONG")
		case "SELECT":
			writeTestRedisSimpleString(writer, "OK")
		case "SCAN":
			writeTestRedisScan(writer, values)
		case "GET":
			if len(args) < 2 {
				writeTestRedisError(writer, "ERR wrong number of arguments")
				continue
			}
			value, ok := values[args[1]]
			if !ok {
				writeTestRedisNullBulk(writer)
				continue
			}
			writeTestRedisBulkString(writer, value)
		case "TYPE":
			if len(args) < 2 {
				writeTestRedisSimpleString(writer, "none")
				continue
			}
			if _, ok := values[args[1]]; !ok {
				writeTestRedisSimpleString(writer, "none")
				continue
			}
			writeTestRedisSimpleString(writer, "string")
		case "TTL":
			_, _ = writer.WriteString(":-1\r\n")
			_ = writer.Flush()
		case "STRLEN":
			length := 0
			if len(args) >= 2 {
				length = len(values[args[1]])
			}
			_, _ = writer.WriteString(fmt.Sprintf(":%d\r\n", length))
			_ = writer.Flush()
		default:
			writeTestRedisError(writer, "ERR unsupported command")
		}
	}
}

func readTestRedisArray(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	var count int
	if _, err := fmt.Sscanf(line, "*%d", &count); err != nil {
		return nil, err
	}
	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		bulkHeader, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		bulkHeader = strings.TrimSuffix(strings.TrimSuffix(bulkHeader, "\n"), "\r")
		var length int
		if _, err := fmt.Sscanf(bulkHeader, "$%d", &length); err != nil {
			return nil, err
		}
		buf := make([]byte, length+2)
		if _, err := reader.Read(buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:length]))
	}
	return args, nil
}

func writeTestRedisScan(writer *bufio.Writer, values map[string]string) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	_, _ = writer.WriteString("*2\r\n")
	writeTestRedisBulkStringNoFlush(writer, "0")
	_, _ = writer.WriteString(fmt.Sprintf("*%d\r\n", len(keys)))
	for _, key := range keys {
		writeTestRedisBulkStringNoFlush(writer, key)
	}
	_ = writer.Flush()
}

func writeTestRedisSimpleString(writer *bufio.Writer, value string) {
	_, _ = writer.WriteString("+" + value + "\r\n")
	_ = writer.Flush()
}

func writeTestRedisError(writer *bufio.Writer, value string) {
	_, _ = writer.WriteString("-" + value + "\r\n")
	_ = writer.Flush()
}

func writeTestRedisBulkString(writer *bufio.Writer, value string) {
	writeTestRedisBulkStringNoFlush(writer, value)
	_ = writer.Flush()
}

func writeTestRedisBulkStringNoFlush(writer *bufio.Writer, value string) {
	_, _ = writer.WriteString(fmt.Sprintf("$%d\r\n%s\r\n", len(value), value))
}

func writeTestRedisNullBulk(writer *bufio.Writer) {
	_, _ = writer.WriteString("$-1\r\n")
	_ = writer.Flush()
}
