package dbtool

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

type UpdateCellResult struct {
	Query        string `json:"query"`
	RowsAffected int64  `json:"rows_affected"`
}

// UpdateTableCell updates a single column value for one row identified by primary key values.
// WHY: The explorer needs safe, parameterized writes instead of accepting raw SQL from the UI.
func UpdateTableCell(
	ctx context.Context,
	cfg Config,
	tableName string,
	columnName string,
	newValue *string,
	primaryKeyValues map[string]string,
) (*UpdateCellResult, error) {
	engine := strings.ToLower(strings.TrimSpace(cfg.Engine))
	if engine != "postgres" {
		return nil, fmt.Errorf("cell updates are only supported for PostgreSQL connections")
	}
	if cfg.IsReadOnly {
		return nil, fmt.Errorf("modifying statements are not allowed on a read-only connection")
	}
	if strings.TrimSpace(tableName) == "" || strings.TrimSpace(columnName) == "" {
		return nil, fmt.Errorf("table and column are required")
	}
	if len(primaryKeyValues) == 0 {
		return nil, fmt.Errorf("primary key values are required to update a row")
	}

	db, err := Connect(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer db.Close()

	columns, err := getPostgresTableColumns(ctx, db, tableName)
	if err != nil {
		return nil, err
	}

	columnByName := make(map[string]TableColumn, len(columns))
	primaryKeyColumns := []TableColumn{}
	for _, column := range columns {
		columnByName[column.Name] = column
		if column.IsPrimaryKey {
			primaryKeyColumns = append(primaryKeyColumns, column)
		}
	}

	targetColumn, ok := columnByName[columnName]
	if !ok {
		return nil, fmt.Errorf("column %q was not found in table %q", columnName, tableName)
	}
	if len(primaryKeyColumns) == 0 {
		return nil, fmt.Errorf("table %q does not have a primary key", tableName)
	}

	args := make([]any, 0, len(primaryKeyColumns)+1)
	setLiteral, setArg, err := formatPostgresCellValue(targetColumn, newValue)
	if err != nil {
		return nil, err
	}
	args = append(args, setArg)

	whereParts := make([]string, 0, len(primaryKeyColumns))
	displayWhereParts := make([]string, 0, len(primaryKeyColumns))
	for _, primaryKeyColumn := range primaryKeyColumns {
		rawValue, ok := primaryKeyValues[primaryKeyColumn.Name]
		if !ok {
			return nil, fmt.Errorf("missing primary key value for column %q", primaryKeyColumn.Name)
		}
		whereLiteral, whereArg, err := formatPostgresCellValue(primaryKeyColumn, &rawValue)
		if err != nil {
			return nil, err
		}
		args = append(args, whereArg)
		quotedColumn := quoteSQLIdentifier(engine, primaryKeyColumn.Name)
		whereParts = append(whereParts, fmt.Sprintf("%s = $%d", quotedColumn, len(args)))
		displayWhereParts = append(displayWhereParts, fmt.Sprintf("%s = %s", quotedColumn, whereLiteral))
	}

	quotedTable := quoteSQLIdentifier(engine, tableName)
	quotedColumn := quoteSQLIdentifier(engine, columnName)
	query := fmt.Sprintf(
		"UPDATE %s SET %s = $1 WHERE %s",
		quotedTable,
		quotedColumn,
		strings.Join(whereParts, " AND "),
	)
	displayQuery := fmt.Sprintf(
		"UPDATE %s SET %s = %s WHERE %s",
		quotedTable,
		quotedColumn,
		setLiteral,
		strings.Join(displayWhereParts, " AND "),
	)

	result, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("update failed: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("failed to read rows affected: %w", err)
	}

	return &UpdateCellResult{
		Query:        displayQuery,
		RowsAffected: rowsAffected,
	}, nil
}

func formatPostgresCellValue(column TableColumn, rawValue *string) (literal string, arg any, err error) {
	if rawValue == nil {
		return "NULL", nil, nil
	}

	trimmed := strings.TrimSpace(*rawValue)
	if trimmed == "" && column.IsNullable && !isBooleanColumn(column) {
		return "NULL", nil, nil
	}

	switch {
	case isBooleanColumn(column):
		parsed, err := strconv.ParseBool(trimmed)
		if err != nil {
			return "", nil, fmt.Errorf("invalid boolean value for column %q", column.Name)
		}
		return strconv.FormatBool(parsed), parsed, nil
	case column.DataType == "integer" || column.DataType == "bigint" || column.DataType == "smallint":
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return "", nil, fmt.Errorf("invalid integer value for column %q", column.Name)
		}
		return strconv.FormatInt(parsed, 10), parsed, nil
	case column.DataType == "numeric" || column.DataType == "real" || column.DataType == "double precision":
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return "", nil, fmt.Errorf("invalid numeric value for column %q", column.Name)
		}
		return strconv.FormatFloat(parsed, 'f', -1, 64), parsed, nil
	default:
		return quotePostgresStringLiteral(trimmed), trimmed, nil
	}
}

func isBooleanColumn(column TableColumn) bool {
	return column.DataType == "boolean"
}

func quotePostgresStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
