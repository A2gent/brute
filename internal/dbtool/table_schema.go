package dbtool

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type TableColumn struct {
	Name         string `json:"name"`
	DataType     string `json:"data_type"`
	IsPrimaryKey bool   `json:"is_primary_key"`
	IsNullable   bool   `json:"is_nullable"`
}

// GetTableColumns returns column metadata for a SQL table. Postgres is required for
// cell editing in the explorer, but the metadata query is shared for all SQL engines.
func GetTableColumns(ctx context.Context, cfg Config, tableName string) ([]TableColumn, error) {
	engine := strings.ToLower(strings.TrimSpace(cfg.Engine))
	if engine == "redis" {
		return nil, fmt.Errorf("table schema is not available for Redis connections")
	}
	if engine != "postgres" && engine != "mysql" && engine != "sqlite" {
		return nil, fmt.Errorf("unsupported database engine: %s", cfg.Engine)
	}

	db, err := Connect(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer db.Close()

	if err := validateAnalyticsColumn(ctx, db, engine, tableName, lookupAnyColumn(ctx, db, engine, tableName)); err != nil {
		return nil, err
	}

	switch engine {
	case "postgres":
		return getPostgresTableColumns(ctx, db, tableName)
	case "mysql":
		return getMySQLTableColumns(ctx, db, tableName)
	case "sqlite":
		return getSQLiteTableColumns(ctx, db, tableName)
	default:
		return nil, fmt.Errorf("unsupported engine %s", engine)
	}
}

func lookupAnyColumn(ctx context.Context, db *sql.DB, engine, tableName string) string {
	query := fmt.Sprintf("SELECT * FROM %s WHERE 1 = 0", quoteSQLIdentifier(engine, tableName))
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return ""
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil || len(columns) == 0 {
		return ""
	}
	return columns[0]
}

func getPostgresTableColumns(ctx context.Context, db *sql.DB, tableName string) ([]TableColumn, error) {
	rows, err := db.QueryContext(ctx, `
SELECT
  c.column_name,
  c.data_type,
  c.is_nullable = 'YES' AS is_nullable,
  COALESCE(pk.is_primary_key, false) AS is_primary_key
FROM information_schema.columns c
LEFT JOIN (
  SELECT ku.column_name, true AS is_primary_key
  FROM information_schema.table_constraints tc
  JOIN information_schema.key_column_usage ku
    ON tc.constraint_name = ku.constraint_name
    AND tc.table_schema = ku.table_schema
  WHERE tc.constraint_type = 'PRIMARY KEY'
    AND tc.table_schema = current_schema()
    AND tc.table_name = $1
) pk ON pk.column_name = c.column_name
WHERE c.table_schema = current_schema()
  AND c.table_name = $1
ORDER BY c.ordinal_position`, tableName)
	if err != nil {
		return nil, fmt.Errorf("failed to load table schema: %w", err)
	}
	defer rows.Close()
	return scanTableColumns(rows)
}

func getMySQLTableColumns(ctx context.Context, db *sql.DB, tableName string) ([]TableColumn, error) {
	rows, err := db.QueryContext(ctx, `
SELECT
  c.column_name,
  c.data_type,
  c.is_nullable = 'YES' AS is_nullable,
  c.column_key = 'PRI' AS is_primary_key
FROM information_schema.columns c
WHERE c.table_schema = DATABASE()
  AND c.table_name = ?
ORDER BY c.ordinal_position`, tableName)
	if err != nil {
		return nil, fmt.Errorf("failed to load table schema: %w", err)
	}
	defer rows.Close()
	return scanTableColumns(rows)
}

func getSQLiteTableColumns(ctx context.Context, db *sql.DB, tableName string) ([]TableColumn, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", quoteSQLIdentifier("sqlite", tableName)))
	if err != nil {
		return nil, fmt.Errorf("failed to load table schema: %w", err)
	}
	defer rows.Close()

	columns := []TableColumn{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns = append(columns, TableColumn{
			Name:         name,
			DataType:     strings.ToLower(columnType),
			IsPrimaryKey: primaryKey > 0,
			IsNullable:   notNull == 0,
		})
	}
	return columns, rows.Err()
}

func scanTableColumns(rows *sql.Rows) ([]TableColumn, error) {
	columns := []TableColumn{}
	for rows.Next() {
		var column TableColumn
		if err := rows.Scan(&column.Name, &column.DataType, &column.IsNullable, &column.IsPrimaryKey); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	return columns, rows.Err()
}
