package dbtool

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"strings"
	"bytes"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// Config represents a database connection config
type Config struct {
	Engine      string
	DSN         string
	IsReadOnly  bool
}

// Connect opens a database connection based on engine type
func Connect(cfg Config) (*sql.DB, error) {
	var driverName string
	switch cfg.Engine {
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

// ExecuteQuery runs a query and returns results in a specified format (csv or json).
// It also enforces read-only safety by inspecting the query.
func ExecuteQuery(ctx context.Context, cfg Config, query string, limit, offset int, format string) (string, error) {
	// Security check for read-only connections
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
		if cfg.Engine == "postgres" || cfg.Engine == "sqlite" || cfg.Engine == "mysql" {
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

// GetTables returns a list of tables for a given database connection
func GetTables(ctx context.Context, cfg Config) ([]string, error) {
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