package dbtool

import (
	"context"
	"database/sql"
	"testing"
)

func TestGetTableColumnsPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local PostgreSQL")
	}

	dsn := "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer db.Close()

	_, _ = db.Exec(`DROP TABLE IF EXISTS explorer_cell_edit_test`)
	_, err = db.Exec(`
CREATE TABLE explorer_cell_edit_test (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  active BOOLEAN NOT NULL DEFAULT false,
  note TEXT
)`)
	if err != nil {
		t.Fatalf("create fixture table: %v", err)
	}
	defer db.Exec(`DROP TABLE IF EXISTS explorer_cell_edit_test`)

	cfg := Config{Engine: "postgres", DSN: dsn, IsReadOnly: false}
	columns, err := GetTableColumns(context.Background(), cfg, "explorer_cell_edit_test")
	if err != nil {
		t.Fatalf("GetTableColumns returned error: %v", err)
	}
	if len(columns) != 4 {
		t.Fatalf("expected 4 columns, got %#v", columns)
	}
	if columns[0].Name != "id" || !columns[0].IsPrimaryKey {
		t.Fatalf("unexpected id column: %#v", columns[0])
	}
	if columns[2].DataType != "boolean" {
		t.Fatalf("expected boolean column, got %#v", columns[2])
	}
}

func TestUpdateTableCellPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local PostgreSQL")
	}

	dsn := "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer db.Close()

	_, _ = db.Exec(`DROP TABLE IF EXISTS explorer_cell_edit_test`)
	_, err = db.Exec(`
CREATE TABLE explorer_cell_edit_test (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  active BOOLEAN NOT NULL DEFAULT false,
  note TEXT
)`)
	if err != nil {
		t.Fatalf("create fixture table: %v", err)
	}
	_, err = db.Exec(`INSERT INTO explorer_cell_edit_test (id, name, active, note) VALUES (1, 'alpha', false, NULL)`)
	if err != nil {
		t.Fatalf("insert fixture row: %v", err)
	}
	defer db.Exec(`DROP TABLE IF EXISTS explorer_cell_edit_test`)

	cfg := Config{Engine: "postgres", DSN: dsn, IsReadOnly: false}
	nextName := "beta"
	result, err := UpdateTableCell(context.Background(), cfg, "explorer_cell_edit_test", "name", &nextName, map[string]string{"id": "1"})
	if err != nil {
		t.Fatalf("UpdateTableCell returned error: %v", err)
	}
	if result.RowsAffected != 1 {
		t.Fatalf("expected one affected row, got %d", result.RowsAffected)
	}
	if result.Query == "" {
		t.Fatalf("expected display query, got empty string")
	}

	active := "true"
	_, err = UpdateTableCell(context.Background(), cfg, "explorer_cell_edit_test", "active", &active, map[string]string{"id": "1"})
	if err != nil {
		t.Fatalf("UpdateTableCell boolean returned error: %v", err)
	}

	var storedName string
	var storedActive bool
	if err := db.QueryRow(`SELECT name, active FROM explorer_cell_edit_test WHERE id = 1`).Scan(&storedName, &storedActive); err != nil {
		t.Fatalf("read updated row: %v", err)
	}
	if storedName != "beta" || !storedActive {
		t.Fatalf("unexpected stored values: name=%q active=%v", storedName, storedActive)
	}
}

func TestUpdateTableCellRejectsReadOnlyConnection(t *testing.T) {
	_, err := UpdateTableCell(
		context.Background(),
		Config{Engine: "postgres", DSN: "postgres://example", IsReadOnly: true},
		"users",
		"name",
		ptr("next"),
		map[string]string{"id": "1"},
	)
	if err == nil {
		t.Fatal("expected read-only error")
	}
}

func ptr(value string) *string {
	return &value
}
