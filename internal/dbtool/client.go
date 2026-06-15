package dbtool

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

const (
	redisDefaultAddress = "127.0.0.1:6379"
	redisCommandTimeout = 5 * time.Second
	// WHY: The existing explorer endpoint returns a single list without pagination.
	// Cap Redis SCAN results to keep projects with huge keyspaces responsive.
	redisMaxExplorerKeys = 1000
	redisScanCount       = 250
)

// Config represents a database connection config
type Config struct {
	Engine     string
	DSN        string
	IsReadOnly bool
}

// Connect opens a SQL database connection based on engine type.
func Connect(cfg Config) (*sql.DB, error) {
	var driverName string
	switch strings.ToLower(strings.TrimSpace(cfg.Engine)) {
	case "postgres":
		driverName = "postgres"
	case "mysql":
		driverName = "mysql"
	case "sqlite":
		driverName = "sqlite"
	default:
		return nil, fmt.Errorf("unsupported database engine: %s", cfg.Engine)
	}

	db, err := sql.Open(driverName, cfg.DSN)
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

// ExecuteQuery runs a SQL query or Redis command and returns results in a specified format (csv or json).
// It also enforces read-only safety by inspecting the query/command.
func ExecuteQuery(ctx context.Context, cfg Config, query string, limit, offset int, format string) (string, error) {
	engine := strings.ToLower(strings.TrimSpace(cfg.Engine))
	cfg.Engine = engine
	if engine == "redis" {
		return executeRedisQuery(ctx, cfg, query, limit, offset, format)
	}

	// Security check for read-only SQL connections.
	if cfg.IsReadOnly {
		upperQuery := strings.ToUpper(strings.TrimSpace(query))
		if strings.HasPrefix(upperQuery, "INSERT") || strings.HasPrefix(upperQuery, "UPDATE") ||
			strings.HasPrefix(upperQuery, "DELETE") || strings.HasPrefix(upperQuery, "DROP") ||
			strings.HasPrefix(upperQuery, "CREATE") || strings.HasPrefix(upperQuery, "ALTER") ||
			strings.HasPrefix(upperQuery, "TRUNCATE") || strings.HasPrefix(upperQuery, "GRANT") ||
			strings.HasPrefix(upperQuery, "REVOKE") || strings.HasPrefix(upperQuery, "EXEC") {
			return "", fmt.Errorf("modifying statements are not allowed on a read-only connection")
		}
	}

	// Basic check for multiple statements (preventing semicolon injection)
	if strings.Contains(query, ";") {
		parts := strings.Split(query, ";")
		validParts := 0
		for _, part := range parts {
			if strings.TrimSpace(part) != "" {
				validParts++
			}
		}
		if validParts > 1 {
			return "", fmt.Errorf("multiple statements (semicolon-separated) are not supported")
		}
	}

	// Apply pagination
	// Strip trailing semicolon for safe injection
	query = strings.TrimSpace(query)
	query = strings.TrimSuffix(query, ";")

	if limit > 0 {
		if engine == "postgres" || engine == "sqlite" || engine == "mysql" {
			query = fmt.Sprintf("%s LIMIT %d OFFSET %d", query, limit, offset)
		}
	}

	db, err := Connect(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to connect: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return "", fmt.Errorf("query execution failed: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return "", err
	}

	// For simple CSV formatting
	var buf bytes.Buffer
	if format == "csv" || format == "" {
		writer := csv.NewWriter(&buf)
		if err := writer.Write(cols); err != nil {
			return "", err
		}

		rawResult := make([][]byte, len(cols))
		dest := make([]interface{}, len(cols))
		for i := range rawResult {
			dest[i] = &rawResult[i]
		}

		for rows.Next() {
			if err := rows.Scan(dest...); err != nil {
				return "", err
			}
			rowStr := make([]string, len(cols))
			for i, raw := range rawResult {
				if raw != nil {
					rowStr[i] = string(raw)
				} else {
					rowStr[i] = "NULL"
				}
			}
			if err := writer.Write(rowStr); err != nil {
				return "", err
			}
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return "", err
		}
	} else if format == "json" {
		// JSON formatting (simplified)
		buf.WriteString("[\n")
		firstRow := true

		rawResult := make([][]byte, len(cols))
		dest := make([]interface{}, len(cols))
		for i := range rawResult {
			dest[i] = &rawResult[i]
		}

		for rows.Next() {
			if err := rows.Scan(dest...); err != nil {
				return "", err
			}
			if !firstRow {
				buf.WriteString(",\n")
			}
			firstRow = false
			buf.WriteString("  {")

			for i, raw := range rawResult {
				colName := cols[i]
				val := "null"
				if raw != nil {
					// Extremely naive escaping for JSON. Better to use json.Marshal
					val = fmt.Sprintf(`"%s"`, strings.ReplaceAll(strings.ReplaceAll(string(raw), "\\", "\\\\"), "\"", "\\\""))
				}
				buf.WriteString(fmt.Sprintf(`"%s": %s`, colName, val))
				if i < len(cols)-1 {
					buf.WriteString(", ")
				}
			}
			buf.WriteString("}")
		}
		buf.WriteString("\n]")
	} else {
		return "", fmt.Errorf("unsupported format: %s", format)
	}

	return buf.String(), nil
}

// GetTables returns a list of SQL tables or Redis keys for a given database connection.
func GetTables(ctx context.Context, cfg Config) ([]string, error) {
	cfg.Engine = strings.ToLower(strings.TrimSpace(cfg.Engine))
	if cfg.Engine == "redis" {
		client, err := connectRedis(ctx, cfg.DSN)
		if err != nil {
			return nil, fmt.Errorf("failed to connect: %w", err)
		}
		defer client.close()
		return scanRedisKeys(ctx, client, "*", redisMaxExplorerKeys)
	}

	db, err := Connect(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer db.Close()

	var query string
	switch cfg.Engine {
	case "postgres":
		query = "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public'"
	case "mysql":
		query = "SHOW TABLES"
	case "sqlite":
		query = "SELECT name FROM sqlite_master WHERE type='table'"
	default:
		return nil, fmt.Errorf("unsupported engine")
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, nil
}

func executeRedisQuery(ctx context.Context, cfg Config, query string, limit, offset int, format string) (string, error) {
	args, err := parseRedisCommand(query)
	if err != nil {
		return "", err
	}
	if len(args) == 0 {
		return "", fmt.Errorf("redis command is required")
	}
	if cfg.IsReadOnly {
		if err := ensureRedisReadOnlyCommand(args[0]); err != nil {
			return "", err
		}
	}
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	client, err := connectRedis(ctx, cfg.DSN)
	if err != nil {
		return "", fmt.Errorf("failed to connect: %w", err)
	}
	defer client.close()

	command := strings.ToUpper(args[0])
	if command == "GET" && len(args) == 2 {
		rows, err := previewRedisKey(ctx, client, args[1], limit, offset)
		if err != nil {
			return "", err
		}
		return formatRedisRows(rows, format)
	}

	resp, err := client.command(ctx, args...)
	if err != nil {
		return "", err
	}
	return formatRedisRows(redisResponseRows(command, resp, limit, offset), format)
}

type redisClient struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
}

type redisConnConfig struct {
	address  string
	username string
	password string
	database int
	useTLS   bool
}

type redisError string

func (e redisError) Error() string { return string(e) }

func connectRedis(ctx context.Context, dsn string) (*redisClient, error) {
	cfg, err := parseRedisDSN(dsn)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: redisCommandTimeout}
	var conn net.Conn
	if cfg.useTLS {
		tlsDialer := tls.Dialer{NetDialer: dialer, Config: &tls.Config{MinVersion: tls.VersionTLS12}}
		conn, err = tlsDialer.DialContext(ctx, "tcp", cfg.address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", cfg.address)
	}
	if err != nil {
		return nil, err
	}

	client := &redisClient{
		conn:   conn,
		reader: bufio.NewReader(conn),
		writer: bufio.NewWriter(conn),
	}

	if cfg.password != "" {
		if cfg.username != "" {
			_, err = client.command(ctx, "AUTH", cfg.username, cfg.password)
		} else {
			_, err = client.command(ctx, "AUTH", cfg.password)
		}
		if err != nil {
			client.close()
			return nil, err
		}
	}

	if cfg.database > 0 {
		if _, err := client.command(ctx, "SELECT", strconv.Itoa(cfg.database)); err != nil {
			client.close()
			return nil, err
		}
	}

	if _, err := client.command(ctx, "PING"); err != nil {
		client.close()
		return nil, err
	}

	return client, nil
}

func parseRedisDSN(dsn string) (redisConnConfig, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return redisConnConfig{}, fmt.Errorf("redis DSN is required")
	}
	if !strings.Contains(dsn, "://") {
		return redisConnConfig{address: withRedisDefaultPort(dsn)}, nil
	}

	parsed, err := url.Parse(dsn)
	if err != nil {
		return redisConnConfig{}, err
	}
	if parsed.Scheme != "redis" && parsed.Scheme != "rediss" {
		return redisConnConfig{}, fmt.Errorf("unsupported redis DSN scheme: %s", parsed.Scheme)
	}

	cfg := redisConnConfig{
		address: withRedisDefaultPort(parsed.Host),
		useTLS:  parsed.Scheme == "rediss",
	}
	if cfg.address == "" {
		cfg.address = redisDefaultAddress
	}
	if parsed.User != nil {
		cfg.username = parsed.User.Username()
		cfg.password, _ = parsed.User.Password()
		// redis://:password@host encodes an empty username. If only one token was
		// provided as redis://password@host, treat it as the password for compatibility.
		if cfg.password == "" && cfg.username != "" {
			cfg.password = cfg.username
			cfg.username = ""
		}
	}
	if path := strings.Trim(parsed.Path, "/"); path != "" {
		database, err := strconv.Atoi(path)
		if err != nil || database < 0 {
			return redisConnConfig{}, fmt.Errorf("invalid redis database index: %s", path)
		}
		cfg.database = database
	}
	return cfg, nil
}

func withRedisDefaultPort(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return redisDefaultAddress
	}
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}
	return net.JoinHostPort(address, "6379")
}

func (c *redisClient) close() {
	_ = c.conn.Close()
}

func (c *redisClient) command(ctx context.Context, args ...string) (interface{}, error) {
	deadline := time.Now().Add(redisCommandTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok {
		deadline = ctxDeadline
	}
	_ = c.conn.SetDeadline(deadline)

	if _, err := fmt.Fprintf(c.writer, "*%d\r\n", len(args)); err != nil {
		return nil, err
	}
	for _, arg := range args {
		if _, err := fmt.Fprintf(c.writer, "$%d\r\n%s\r\n", len([]byte(arg)), arg); err != nil {
			return nil, err
		}
	}
	if err := c.writer.Flush(); err != nil {
		return nil, err
	}
	resp, err := readRedisRESP(c.reader)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func readRedisRESP(reader *bufio.Reader) (interface{}, error) {
	prefix, err := reader.ReadByte()
	if err != nil {
		return nil, err
	}
	switch prefix {
	case '+':
		return readRedisLine(reader)
	case '-':
		line, err := readRedisLine(reader)
		if err != nil {
			return nil, err
		}
		return nil, redisError(line)
	case ':':
		line, err := readRedisLine(reader)
		if err != nil {
			return nil, err
		}
		return strconv.ParseInt(line, 10, 64)
	case '$':
		line, err := readRedisLine(reader)
		if err != nil {
			return nil, err
		}
		length, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if length < 0 {
			return nil, nil
		}
		buf := make([]byte, length+2)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return nil, err
		}
		return string(buf[:length]), nil
	case '*':
		line, err := readRedisLine(reader)
		if err != nil {
			return nil, err
		}
		length, err := strconv.Atoi(line)
		if err != nil {
			return nil, err
		}
		if length < 0 {
			return nil, nil
		}
		values := make([]interface{}, 0, length)
		for i := 0; i < length; i++ {
			value, err := readRedisRESP(reader)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("unsupported redis response prefix %q", prefix)
	}
}

func readRedisLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

func scanRedisKeys(ctx context.Context, client *redisClient, pattern string, maxKeys int) ([]string, error) {
	if maxKeys <= 0 {
		maxKeys = redisMaxExplorerKeys
	}
	cursor := "0"
	keys := make([]string, 0)
	for {
		resp, err := client.command(ctx, "SCAN", cursor, "MATCH", pattern, "COUNT", strconv.Itoa(redisScanCount))
		if err != nil {
			return nil, err
		}
		nextCursor, batch, err := parseRedisScanResponse(resp)
		if err != nil {
			return nil, err
		}
		keys = append(keys, batch...)
		if len(keys) >= maxKeys {
			keys = keys[:maxKeys]
			break
		}
		cursor = nextCursor
		if cursor == "0" {
			break
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func parseRedisScanResponse(resp interface{}) (string, []string, error) {
	values, ok := resp.([]interface{})
	if !ok || len(values) != 2 {
		return "", nil, fmt.Errorf("unexpected SCAN response: %#v", resp)
	}
	cursor, ok := values[0].(string)
	if !ok {
		return "", nil, fmt.Errorf("unexpected SCAN cursor: %#v", values[0])
	}
	keyValues, ok := values[1].([]interface{})
	if !ok {
		return "", nil, fmt.Errorf("unexpected SCAN key list: %#v", values[1])
	}
	keys := make([]string, 0, len(keyValues))
	for _, keyValue := range keyValues {
		key, ok := keyValue.(string)
		if ok {
			keys = append(keys, key)
		}
	}
	return cursor, keys, nil
}

func parseRedisCommand(query string) ([]string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("redis command is required")
	}

	args := make([]string, 0)
	var current strings.Builder
	var quote rune
	escaped := false
	inToken := false
	for _, r := range query {
		if escaped {
			current.WriteRune(r)
			escaped = false
			inToken = true
			continue
		}
		if r == '\\' {
			escaped = true
			inToken = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			inToken = true
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if inToken {
				args = append(args, current.String())
				current.Reset()
				inToken = false
			}
			continue
		}
		current.WriteRune(r)
		inToken = true
	}
	if escaped {
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quoted redis argument")
	}
	if inToken {
		args = append(args, current.String())
	}
	return args, nil
}

var redisReadOnlyCommands = map[string]struct{}{
	"DBSIZE":   {},
	"EXISTS":   {},
	"GET":      {},
	"HGET":     {},
	"HGETALL":  {},
	"HLEN":     {},
	"KEYS":     {},
	"LLEN":     {},
	"LRANGE":   {},
	"MGET":     {},
	"PTTL":     {},
	"SCARD":    {},
	"SCAN":     {},
	"SCOMMAND": {},
	"SMEMBERS": {},
	"SSCAN":    {},
	"STRLEN":   {},
	"TTL":      {},
	"TYPE":     {},
	"XINFO":    {},
	"XLEN":     {},
	"XRANGE":   {},
	"XREVRANGE": {},
	"ZCARD":     {},
	"ZRANGE":    {},
	"ZREVRANGE": {},
}

func ensureRedisReadOnlyCommand(command string) error {
	command = strings.ToUpper(strings.TrimSpace(command))
	if _, ok := redisReadOnlyCommands[command]; ok {
		return nil
	}
	return fmt.Errorf("redis command %q is not allowed on a read-only connection", command)
}

func previewRedisKey(ctx context.Context, client *redisClient, key string, limit, offset int) ([]map[string]interface{}, error) {
	keyType, err := redisStringCommand(ctx, client, "TYPE", key)
	if err != nil {
		return nil, err
	}
	ttl, _ := redisIntCommand(ctx, client, "TTL", key)
	base := func() map[string]interface{} {
		return map[string]interface{}{
			"key":  key,
			"type": keyType,
			"ttl":  ttl,
		}
	}

	switch keyType {
	case "none":
		row := base()
		row["value"] = nil
		return []map[string]interface{}{row}, nil
	case "string":
		value, err := client.command(ctx, "GET", key)
		if err != nil {
			return nil, err
		}
		size, _ := redisIntCommand(ctx, client, "STRLEN", key)
		row := base()
		row["value"] = value
		row["size"] = size
		return []map[string]interface{}{row}, nil
	case "hash":
		resp, err := client.command(ctx, "HGETALL", key)
		if err != nil {
			return nil, err
		}
		items := redisStringSlice(resp)
		rows := make([]map[string]interface{}, 0, len(items)/2)
		start, end := boundedRange(len(items)/2, limit, offset)
		for pairIndex := start; pairIndex < end; pairIndex++ {
			row := base()
			row["field"] = items[pairIndex*2]
			if pairIndex*2+1 < len(items) {
				row["value"] = items[pairIndex*2+1]
			}
			rows = append(rows, row)
		}
		return rows, nil
	case "list":
		start := offset
		stop := offset + limit - 1
		resp, err := client.command(ctx, "LRANGE", key, strconv.Itoa(start), strconv.Itoa(stop))
		if err != nil {
			return nil, err
		}
		values := redisStringSlice(resp)
		rows := make([]map[string]interface{}, 0, len(values))
		for index, value := range values {
			row := base()
			row["index"] = offset + index
			row["value"] = value
			rows = append(rows, row)
		}
		return rows, nil
	case "set":
		resp, err := client.command(ctx, "SMEMBERS", key)
		if err != nil {
			return nil, err
		}
		values := redisStringSlice(resp)
		sort.Strings(values)
		start, end := boundedRange(len(values), limit, offset)
		rows := make([]map[string]interface{}, 0, end-start)
		for index, value := range values[start:end] {
			row := base()
			row["index"] = start + index
			row["value"] = value
			rows = append(rows, row)
		}
		return rows, nil
	case "zset":
		start := offset
		stop := offset + limit - 1
		resp, err := client.command(ctx, "ZRANGE", key, strconv.Itoa(start), strconv.Itoa(stop), "WITHSCORES")
		if err != nil {
			return nil, err
		}
		values := redisStringSlice(resp)
		rows := make([]map[string]interface{}, 0, len(values)/2)
		for pairIndex := 0; pairIndex < len(values); pairIndex += 2 {
			row := base()
			row["index"] = offset + pairIndex/2
			row["value"] = values[pairIndex]
			if pairIndex+1 < len(values) {
				row["score"] = values[pairIndex+1]
			}
			rows = append(rows, row)
		}
		return rows, nil
	case "stream":
		resp, err := client.command(ctx, "XRANGE", key, "-", "+", "COUNT", strconv.Itoa(limit))
		if err != nil {
			return nil, err
		}
		return redisStreamRows(base, resp), nil
	default:
		row := base()
		row["value"] = fmt.Sprintf("Preview for Redis type %q is not implemented yet", keyType)
		return []map[string]interface{}{row}, nil
	}
}

func redisStringCommand(ctx context.Context, client *redisClient, args ...string) (string, error) {
	resp, err := client.command(ctx, args...)
	if err != nil {
		return "", err
	}
	value, ok := resp.(string)
	if !ok {
		return "", fmt.Errorf("unexpected redis string response: %#v", resp)
	}
	return value, nil
}

func redisIntCommand(ctx context.Context, client *redisClient, args ...string) (int64, error) {
	resp, err := client.command(ctx, args...)
	if err != nil {
		return 0, err
	}
	value, ok := resp.(int64)
	if !ok {
		return 0, fmt.Errorf("unexpected redis integer response: %#v", resp)
	}
	return value, nil
}

func redisStringSlice(resp interface{}) []string {
	values, ok := resp.([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func boundedRange(total, limit, offset int) (int, int) {
	if offset > total {
		return total, total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return offset, end
}

func redisStreamRows(base func() map[string]interface{}, resp interface{}) []map[string]interface{} {
	entries, ok := resp.([]interface{})
	if !ok {
		return nil
	}
	rows := make([]map[string]interface{}, 0, len(entries))
	for index, entry := range entries {
		entryParts, ok := entry.([]interface{})
		if !ok || len(entryParts) != 2 {
			continue
		}
		row := base()
		row["index"] = index
		row["id"], _ = entryParts[0].(string)
		fields := map[string]string{}
		for fieldIndex, fieldValue := range redisStringSlice(entryParts[1]) {
			if fieldIndex%2 == 0 {
				fields[fieldValue] = ""
				continue
			}
			fieldName := redisStringSlice(entryParts[1])[fieldIndex-1]
			fields[fieldName] = fieldValue
		}
		encoded, _ := json.Marshal(fields)
		row["value"] = string(encoded)
		rows = append(rows, row)
	}
	return rows
}

func redisResponseRows(command string, resp interface{}, limit, offset int) []map[string]interface{} {
	if values, ok := resp.([]interface{}); ok {
		start, end := boundedRange(len(values), limit, offset)
		rows := make([]map[string]interface{}, 0, end-start)
		for index, value := range values[start:end] {
			rows = append(rows, map[string]interface{}{
				"command": command,
				"index":   start + index,
				"result":  redisDisplayValue(value),
			})
		}
		return rows
	}
	return []map[string]interface{}{{
		"command": command,
		"result":  redisDisplayValue(resp),
	}}
}

func redisDisplayValue(value interface{}) interface{} {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case string, int64:
		return typed
	case []interface{}:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprintf("%#v", typed)
		}
		return string(encoded)
	default:
		return fmt.Sprintf("%#v", typed)
	}
}

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
