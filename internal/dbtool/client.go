package dbtool

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// Config represents a database connection config
type Config struct {
	Engine     string
	DSN        string
	IsReadOnly bool
}

// supportedEngines lists every engine the explorer, HTTP API, and sql_query tool accept.
var supportedEngines = []string{"postgres", "mysql", "sqlite", "redis", "elasticsearch"}

// SupportedEngines returns the engine names in a stable order for validation messages.
func SupportedEngines() []string {
	return append([]string(nil), supportedEngines...)
}

// IsSupportedEngine reports whether the engine has a client implementation in this package.
func IsSupportedEngine(engine string) bool {
	engine = strings.ToLower(strings.TrimSpace(engine))
	for _, supported := range supportedEngines {
		if engine == supported {
			return true
		}
	}
	return false
}

// Connect opens a SQL database connection based on engine type.
func Connect(cfg Config) (*sql.DB, error) {
	// WHY: db.Ping() without a deadline hangs ~75s on filtered ports; bound it.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return connectContext(ctx, cfg)
}

func connectContext(ctx context.Context, cfg Config) (*sql.DB, error) {
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

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

// isSelectQuery identifies SELECT statements before pagination is appended. For CTEs,
// it inspects only top-level tokens so SELECTs inside CTE bodies do not mask DDL/DML.
func isSelectQuery(query string) bool {
	keyword, position := nextSQLKeyword(query, 0)
	if keyword == "SELECT" {
		return true
	}
	if keyword != "WITH" {
		return false
	}

	depth := 0
	for position < len(query) {
		position = skipSQLTrivia(query, position)
		if position >= len(query) {
			break
		}

		switch query[position] {
		case '\'', '"', '`':
			position = skipSQLQuoted(query, position)
		case '(':
			depth++
			position++
		case ')':
			if depth > 0 {
				depth--
			}
			position++
		default:
			if !isSQLIdentifierStart(query[position]) {
				position++
				continue
			}
			start := position
			for position < len(query) && isSQLIdentifierPart(query[position]) {
				position++
			}
			if depth == 0 && isSQLStatementKeyword(query[start:position]) {
				return strings.EqualFold(query[start:position], "SELECT")
			}
		}
	}
	return false
}

func nextSQLKeyword(query string, position int) (string, int) {
	position = skipSQLTrivia(query, position)
	if position >= len(query) || !isSQLIdentifierStart(query[position]) {
		return "", position
	}
	start := position
	for position < len(query) && isSQLIdentifierPart(query[position]) {
		position++
	}
	return strings.ToUpper(query[start:position]), position
}

func skipSQLTrivia(query string, position int) int {
	for position < len(query) {
		switch {
		case query[position] == ' ' || query[position] == '\t' || query[position] == '\n' || query[position] == '\r' || query[position] == '\f':
			position++
		case strings.HasPrefix(query[position:], "--"):
			if end := strings.IndexByte(query[position:], '\n'); end >= 0 {
				position += end + 1
			} else {
				return len(query)
			}
		case strings.HasPrefix(query[position:], "/*"):
			if end := strings.Index(query[position+2:], "*/"); end >= 0 {
				position += end + 4
			} else {
				return len(query)
			}
		default:
			return position
		}
	}
	return position
}

func skipSQLQuoted(query string, position int) int {
	quote := query[position]
	position++
	for position < len(query) {
		if query[position] == quote {
			if position+1 < len(query) && query[position+1] == quote {
				position += 2
				continue
			}
			return position + 1
		}
		if query[position] == '\\' && position+1 < len(query) {
			position += 2
		} else {
			position++
		}
	}
	return position
}

func isSQLIdentifierStart(character byte) bool {
	return character == '_' || character == '$' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
}

func isSQLIdentifierPart(character byte) bool {
	return isSQLIdentifierStart(character) || character >= '0' && character <= '9'
}

func isSQLStatementKeyword(keyword string) bool {
	switch strings.ToUpper(keyword) {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "MERGE", "VALUES", "CALL", "CREATE", "ALTER", "DROP", "TRUNCATE", "GRANT", "REVOKE", "EXEC", "EXECUTE", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "PRAGMA", "VACUUM", "SET", "RESET", "ANALYZE", "REFRESH", "COMMENT", "DO", "TABLE":
		return true
	default:
		return false
	}
}

// ExecuteQuery runs a SQL query, Redis command, or Elasticsearch REST request and returns
// results in a specified format (csv or json).
// It also enforces read-only safety by inspecting the query/command.
func ExecuteQuery(ctx context.Context, cfg Config, query string, limit, offset int, format string) (string, error) {
	engine := strings.ToLower(strings.TrimSpace(cfg.Engine))
	cfg.Engine = engine
	if engine == "redis" {
		return executeRedisQuery(ctx, cfg, query, limit, offset, format)
	}
	if engine == "elasticsearch" {
		return executeElasticsearchQuery(ctx, cfg, query, limit, offset, format)
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

	// Pagination only applies to row-returning SELECTs. Appending LIMIT to DDL/DML
	// makes otherwise valid commands fail (for example, ALTER ... LIMIT ...).
	if limit > 0 && isSelectQuery(query) {
		if engine == "postgres" || engine == "sqlite" || engine == "mysql" {
			// Start the clause on a new line so a trailing SQL line comment cannot swallow it.
			query = fmt.Sprintf("%s\nLIMIT %d OFFSET %d", query, limit, offset)
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
		// WHY: json.Marshal(nil slice) emits null; the UI expects an array.
		records := make([]map[string]interface{}, 0)

		rawResult := make([][]byte, len(cols))
		dest := make([]interface{}, len(cols))
		for i := range rawResult {
			dest[i] = &rawResult[i]
		}

		for rows.Next() {
			if err := rows.Scan(dest...); err != nil {
				return "", err
			}
			record := make(map[string]interface{}, len(cols))
			for i, raw := range rawResult {
				if raw == nil {
					record[cols[i]] = nil
					continue
				}
				record[cols[i]] = string(raw)
			}
			records = append(records, record)
		}

		encoded, err := json.Marshal(records)
		if err != nil {
			return "", fmt.Errorf("failed to encode query results as JSON: %w", err)
		}
		buf.Write(encoded)
	} else {
		return "", fmt.Errorf("unsupported format: %s", format)
	}

	return buf.String(), nil
}

// GetTables returns a list of SQL tables, Redis keys, or Elasticsearch indices for a connection.
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
	if cfg.Engine == "elasticsearch" {
		// WHY: no root ping here. GET / needs cluster monitor privileges that index-scoped
		// API keys lack, and listing indices already fails with a clear error when the
		// cluster is unreachable.
		conn, err := parseElasticsearchDSN(cfg.DSN)
		if err != nil {
			return nil, fmt.Errorf("failed to connect: %w", err)
		}
		return listElasticsearchCollections(ctx, conn)
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
