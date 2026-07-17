package dbtool

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const defaultColumnAnalyticsTopValuesLimit = 20

type ColumnAnalytics struct {
	Table              string                      `json:"table"`
	Column             string                      `json:"column"`
	TotalRowCount      int64                       `json:"total_row_count"`
	DistinctCount      int64                       `json:"distinct_count"`
	NullCount          int64                       `json:"null_count"`
	TopValues          []ColumnAnalyticsValue      `json:"top_values"`
	TopValuesTruncated bool                        `json:"top_values_truncated"`
	ForeignKeys        []ColumnAnalyticsForeignKey `json:"foreign_keys"`
}

type ColumnAnalyticsValue struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

type ColumnAnalyticsForeignKey struct {
	ConstraintName   string `json:"constraint_name,omitempty"`
	ReferencedTable  string `json:"referenced_table"`
	ReferencedColumn string `json:"referenced_column"`
}

// GetColumnAnalytics performs exact, read-only aggregation only after table and column
// names have been verified against database metadata. This keeps identifier input out of SQL.
func GetColumnAnalytics(ctx context.Context, cfg Config, tableName, columnName string, topValuesLimit int) (*ColumnAnalytics, error) {
	engine := strings.ToLower(strings.TrimSpace(cfg.Engine))
	if engine != "postgres" && engine != "mysql" && engine != "sqlite" {
		return nil, fmt.Errorf("column analytics is unsupported for engine %s", cfg.Engine)
	}
	if topValuesLimit <= 0 {
		topValuesLimit = defaultColumnAnalyticsTopValuesLimit
	}

	db, err := Connect(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer db.Close()

	if err := validateAnalyticsColumn(ctx, db, engine, tableName, columnName); err != nil {
		return nil, err
	}

	quotedTable := quoteSQLIdentifier(engine, tableName)
	quotedColumn := quoteSQLIdentifier(engine, columnName)
	analytics := &ColumnAnalytics{
		Table:       tableName,
		Column:      columnName,
		TopValues:   []ColumnAnalyticsValue{},
		ForeignKeys: []ColumnAnalyticsForeignKey{},
	}

	countsQuery := fmt.Sprintf(
		"SELECT COUNT(*), COUNT(DISTINCT %s), COUNT(*) - COUNT(%s) FROM %s",
		quotedColumn,
		quotedColumn,
		quotedTable,
	)
	if err := db.QueryRowContext(ctx, countsQuery).Scan(
		&analytics.TotalRowCount,
		&analytics.DistinctCount,
		&analytics.NullCount,
	); err != nil {
		return nil, fmt.Errorf("failed to aggregate column: %w", err)
	}

	topValuesQuery := fmt.Sprintf(
		"SELECT %s, COUNT(*) AS value_count FROM %s WHERE %s IS NOT NULL GROUP BY %s ORDER BY value_count DESC LIMIT %d",
		quotedColumn,
		quotedTable,
		quotedColumn,
		quotedColumn,
		topValuesLimit+1,
	)
	rows, err := db.QueryContext(ctx, topValuesQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch top column values: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var value any
		var count int64
		if err := rows.Scan(&value, &count); err != nil {
			return nil, fmt.Errorf("failed to scan top column value: %w", err)
		}
		if len(analytics.TopValues) == topValuesLimit {
			analytics.TopValuesTruncated = true
			continue
		}
		analytics.TopValues = append(analytics.TopValues, ColumnAnalyticsValue{
			Value: databaseValueString(value),
			Count: count,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read top column values: %w", err)
	}

	foreignKeys, err := getColumnForeignKeys(ctx, db, engine, tableName, columnName)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect foreign keys: %w", err)
	}
	analytics.ForeignKeys = foreignKeys
	return analytics, nil
}

func validateAnalyticsColumn(ctx context.Context, db *sql.DB, engine, tableName, columnName string) error {
	query := fmt.Sprintf("SELECT * FROM %s WHERE 1 = 0", quoteSQLIdentifier(engine, tableName))
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("table %q was not found: %w", tableName, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("failed to inspect table %q: %w", tableName, err)
	}
	for _, column := range columns {
		if column == columnName {
			return nil
		}
	}
	return fmt.Errorf("column %q was not found in table %q", columnName, tableName)
}

func getColumnForeignKeys(ctx context.Context, db *sql.DB, engine, tableName, columnName string) ([]ColumnAnalyticsForeignKey, error) {
	switch engine {
	case "postgres":
		return queryColumnForeignKeys(ctx, db, `
SELECT tc.constraint_name, ccu.table_name, ccu.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
  ON tc.constraint_name = kcu.constraint_name AND tc.constraint_schema = kcu.constraint_schema
JOIN information_schema.constraint_column_usage ccu
  ON ccu.constraint_name = tc.constraint_name AND ccu.constraint_schema = tc.constraint_schema
WHERE tc.constraint_type = 'FOREIGN KEY'
  AND tc.table_schema = current_schema()
  AND tc.table_name = $1
  AND kcu.column_name = $2
ORDER BY tc.constraint_name`, tableName, columnName)
	case "mysql":
		return queryColumnForeignKeys(ctx, db, `
SELECT constraint_name, referenced_table_name, referenced_column_name
FROM information_schema.key_column_usage
WHERE table_schema = DATABASE()
  AND table_name = ?
  AND column_name = ?
  AND referenced_table_name IS NOT NULL
ORDER BY constraint_name`, tableName, columnName)
	case "sqlite":
		rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA foreign_key_list(%s)", quoteSQLIdentifier(engine, tableName)))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		foreignKeys := []ColumnAnalyticsForeignKey{}
		for rows.Next() {
			var id, sequence int
			var referencedTable, sourceColumn, referencedColumn, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &sequence, &referencedTable, &sourceColumn, &referencedColumn, &onUpdate, &onDelete, &match); err != nil {
				return nil, err
			}
			if sourceColumn == columnName {
				foreignKeys = append(foreignKeys, ColumnAnalyticsForeignKey{
					ConstraintName:   fmt.Sprintf("fk_%d_%d", id, sequence),
					ReferencedTable:  referencedTable,
					ReferencedColumn: referencedColumn,
				})
			}
		}
		return foreignKeys, rows.Err()
	default:
		return nil, fmt.Errorf("unsupported engine %s", engine)
	}
}

func queryColumnForeignKeys(ctx context.Context, db *sql.DB, query, tableName, columnName string) ([]ColumnAnalyticsForeignKey, error) {
	rows, err := db.QueryContext(ctx, query, tableName, columnName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	foreignKeys := []ColumnAnalyticsForeignKey{}
	for rows.Next() {
		var foreignKey ColumnAnalyticsForeignKey
		if err := rows.Scan(&foreignKey.ConstraintName, &foreignKey.ReferencedTable, &foreignKey.ReferencedColumn); err != nil {
			return nil, err
		}
		foreignKeys = append(foreignKeys, foreignKey)
	}
	return foreignKeys, rows.Err()
}

func quoteSQLIdentifier(engine, identifier string) string {
	if engine == "mysql" {
		return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func databaseValueString(value any) string {
	switch typed := value.(type) {
	case nil:
		return "NULL"
	case []byte:
		return string(typed)
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case time.Time:
		return typed.Format(time.RFC3339Nano)
	default:
		return fmt.Sprint(typed)
	}
}
