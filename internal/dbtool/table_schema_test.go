package dbtool

import (
	"context"
	"testing"
)

func TestGetTableColumnsSQLite(t *testing.T) {
	dbPath := createSQLiteFixture(t)
	columns, err := GetTableColumns(context.Background(), Config{Engine: "sqlite", DSN: dbPath, IsReadOnly: true}, "items")
	if err != nil {
		t.Fatalf("GetTableColumns returned error: %v", err)
	}
	if len(columns) != 3 {
		t.Fatalf("expected 3 columns, got %#v", columns)
	}
	if columns[0].Name != "id" || !columns[0].IsPrimaryKey {
		t.Fatalf("unexpected id column: %#v", columns[0])
	}
}
