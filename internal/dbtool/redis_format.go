package dbtool

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// WHY: Redis rows are synthesized maps rather than sql.Rows, so formatting them
// separately avoids mixing Redis-specific CSV/JSON rules into the SQL path.
func formatRedisRows(rows []map[string]interface{}, format string) (string, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "csv"
	}
	if format == "json" {
		encoded, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
	if format != "csv" {
		return "", fmt.Errorf("unsupported format: %s", format)
	}

	columns := redisColumns(rows)
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	if err := writer.Write(columns); err != nil {
		return "", err
	}
	for _, row := range rows {
		values := make([]string, len(columns))
		for i, column := range columns {
			values[i] = redisCellString(row[column])
		}
		if err := writer.Write(values); err != nil {
			return "", err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func redisColumns(rows []map[string]interface{}) []string {
	preferred := []string{"key", "type", "index", "id", "field", "value", "member", "score", "ttl", "size", "command", "result"}
	seen := map[string]struct{}{}
	for _, row := range rows {
		for column := range row {
			seen[column] = struct{}{}
		}
	}
	columns := make([]string, 0, len(seen))
	for _, column := range preferred {
		if _, ok := seen[column]; ok {
			columns = append(columns, column)
			delete(seen, column)
		}
	}
	extra := make([]string, 0, len(seen))
	for column := range seen {
		extra = append(extra, column)
	}
	sort.Strings(extra)
	return append(columns, extra...)
}

func redisCellString(value interface{}) string {
	if value == nil {
		return "NULL"
	}
	switch typed := value.(type) {
	case string:
		return typed
	case int64:
		return strconv.FormatInt(typed, 10)
	case int:
		return strconv.Itoa(typed)
	case bool:
		return strconv.FormatBool(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err == nil {
			return string(encoded)
		}
		return fmt.Sprint(typed)
	}
}

func isRedisNil(err error) bool {
	return errors.Is(err, redisError("nil"))
}
