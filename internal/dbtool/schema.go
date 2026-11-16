package dbtool

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/go-sql-driver/mysql"
)

const defaultPostgresSchema = "public"

// SupportsSchemaSelection reports whether the engine lets the explorer switch schema
// (Postgres) or database (MySQL) for a saved connection.
func SupportsSchemaSelection(engine string) bool {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "postgres", "mysql":
		return true
	default:
		return false
	}
}

func ValidateSchemaName(schema string) error {
	if len(schema) > 64 {
		return fmt.Errorf("schema name %q is too long", schema)
	}
	if strings.ContainsRune(schema, 0) {
		return fmt.Errorf("schema name contains a NUL character")
	}
	return nil
}

// selectedSchema returns the configured schema or database; "" means the connection default
// (Postgres current_schema(), MySQL DATABASE() from the DSN).
func selectedSchema(cfg Config) string {
	return strings.TrimSpace(cfg.Schema)
}

// applySchemaToDSN makes unqualified table names in user queries resolve inside the
// selected schema, so explorer previews and the SQL panel see the same tables as the list.
func applySchemaToDSN(engine, dsn, schema string) (string, error) {
	schema = strings.TrimSpace(schema)
	if schema == "" || !SupportsSchemaSelection(engine) {
		return dsn, nil
	}
	if err := ValidateSchemaName(schema); err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "postgres":
		searchPath := quoteSQLIdentifier("postgres", schema)
		if schema != defaultPostgresSchema {
			searchPath += ", " + defaultPostgresSchema
		}
		if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
			parsed, err := url.Parse(dsn)
			if err != nil {
				return "", fmt.Errorf("invalid postgres DSN: %w", err)
			}
			query := parsed.Query()
			query.Set("search_path", searchPath)
			parsed.RawQuery = query.Encode()
			return parsed.String(), nil
		}
		escaped := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(searchPath)
		return strings.TrimSpace(dsn) + " search_path='" + escaped + "'", nil
	case "mysql":
		parsed, err := mysql.ParseDSN(dsn)
		if err != nil {
			return "", fmt.Errorf("invalid mysql DSN: %w", err)
		}
		parsed.DBName = schema
		return parsed.FormatDSN(), nil
	}
	return dsn, nil
}

var mysqlSystemDatabases = map[string]bool{
	"information_schema": true,
	"mysql":              true,
	"performance_schema": true,
	"sys":                true,
}

// ListSchemas returns user schemas (Postgres) or databases (MySQL) for a connection.
// Other engines have no schema concept and return an empty list.
func ListSchemas(ctx context.Context, cfg Config) ([]string, error) {
	engine := strings.ToLower(strings.TrimSpace(cfg.Engine))
	if !SupportsSchemaSelection(engine) {
		return []string{}, nil
	}
	cfg.Engine = engine
	cfg.Schema = ""
	db, err := Connect(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer db.Close()

	query := `SELECT schema_name FROM information_schema.schemata
WHERE schema_name NOT LIKE 'pg\_%' AND schema_name <> 'information_schema'
ORDER BY schema_name`
	if engine == "mysql" {
		query = "SHOW DATABASES"
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	schemas := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if engine == "mysql" && mysqlSystemDatabases[strings.ToLower(name)] {
			continue
		}
		schemas = append(schemas, name)
	}
	return schemas, rows.Err()
}
