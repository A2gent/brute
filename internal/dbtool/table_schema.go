package dbtool

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type TableColumn struct {
	Name         string                      `json:"name"`
	DataType     string                      `json:"data_type"`
	IsPrimaryKey bool                        `json:"is_primary_key"`
	IsNullable   bool                        `json:"is_nullable"`
	ForeignKeys  []ColumnAnalyticsForeignKey `json:"foreign_keys,omitempty"`
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

	var columns []TableColumn
	switch engine {
	case "postgres":
		columns, err = getPostgresTableColumns(ctx, db, tableName)
	case "mysql":
		columns, err = getMySQLTableColumns(ctx, db, tableName)
	case "sqlite":
		columns, err = getSQLiteTableColumns(ctx, db, tableName)
	default:
		return nil, fmt.Errorf("unsupported engine %s", engine)
	}
	if err != nil {
		return nil, err
	}

	foreignKeysByColumn, err := getTableForeignKeysByColumn(ctx, db, engine, tableName)
	if err != nil {
		return nil, fmt.Errorf("failed to load foreign keys: %w", err)
	}
	for index := range columns {
		columns[index].ForeignKeys = foreignKeysByColumn[columns[index].Name]
		if columns[index].ForeignKeys == nil {
			columns[index].ForeignKeys = []ColumnAnalyticsForeignKey{}
		}
	}
	return columns, nil
}

func getTableForeignKeysByColumn(ctx context.Context, db *sql.DB, engine, tableName string) (map[string][]ColumnAnalyticsForeignKey, error) {
	foreignKeysByColumn := map[string][]ColumnAnalyticsForeignKey{}
	switch engine {
	case "postgres":
		rows, err := db.QueryContext(ctx, `
SELECT kcu.column_name, tc.constraint_name, ccu.table_name, ccu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON tc.constraint_name = kcu.constraint_name AND tc.constraint_schema = kcu.constraint_schema
JOIN information_schema.constraint_column_usage ccu
  ON ccu.constraint_name = tc.constraint_name AND ccu.constraint_schema = tc.constraint_schema
WHERE tc.constraint_type = 'FOREIGN KEY'
  AND tc.table_schema = current_schema()
  AND tc.table_name = $1
ORDER BY tc.constraint_name, kcu.ordinal_position`, tableName)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var columnName string
			var foreignKey ColumnAnalyticsForeignKey
			if err := rows.Scan(&columnName, &foreignKey.ConstraintName, &foreignKey.ReferencedTable, &foreignKey.ReferencedColumn); err != nil {
				return nil, err
			}
			foreignKeysByColumn[columnName] = append(foreignKeysByColumn[columnName], foreignKey)
		}
		return foreignKeysByColumn, rows.Err()
	case "mysql":
		rows, err := db.QueryContext(ctx, `
SELECT column_name, constraint_name, referenced_table_name, referenced_column_name
FROM information_schema.key_column_usage
WHERE table_schema = DATABASE()
  AND table_name = ?
  AND referenced_table_name IS NOT NULL
ORDER BY constraint_name, ordinal_position`, tableName)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var columnName string
			var foreignKey ColumnAnalyticsForeignKey
			if err := rows.Scan(&columnName, &foreignKey.ConstraintName, &foreignKey.ReferencedTable, &foreignKey.ReferencedColumn); err != nil {
				return nil, err
			}
			foreignKeysByColumn[columnName] = append(foreignKeysByColumn[columnName], foreignKey)
		}
		return foreignKeysByColumn, rows.Err()
	case "sqlite":
		rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_list(%s)", quoteSQLIdentifier(engine, tableName)))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id, sequence int
			var referencedTable, sourceColumn, referencedColumn, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &sequence, &referencedTable, &sourceColumn, &referencedColumn, &onUpdate, &onDelete, &match); err != nil {
				return nil, err
			}
			foreignKeysByColumn[sourceColumn] = append(foreignKeysByColumn[sourceColumn], ColumnAnalyticsForeignKey{
				ConstraintName:   fmt.Sprintf("fk_%d_%d", id, sequence),
				ReferencedTable:  referencedTable,
				ReferencedColumn: referencedColumn,
			})
		}
		return foreignKeysByColumn, rows.Err()
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
