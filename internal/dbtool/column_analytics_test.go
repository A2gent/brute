package dbtool

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestGetColumnAnalyticsReturnsCardinalityTopValuesAndForeignKey(t *testing.T) {
	dbPath := t.TempDir() + "/analytics.db"
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite fixture: %v", err)
	}
	defer db.Close()

	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE categories (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE events (id INTEGER PRIMARY KEY, category_id INTEGER, FOREIGN KEY (category_id) REFERENCES categories(id))`,
		`INSERT INTO categories (id, name) VALUES (1, 'Popular'), (2, 'Rare')`,
		`INSERT INTO events (category_id) VALUES (1), (1), (1), (2), (NULL)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("execute fixture statement %q: %v", statement, err)
		}
	}

	analytics, err := GetColumnAnalytics(context.Background(), Config{Engine: "sqlite", DSN: dbPath, IsReadOnly: true}, "events", "category_id", 20)
	if err != nil {
		t.Fatalf("GetColumnAnalytics returned error: %v", err)
	}
	if analytics.Table != "events" || analytics.Column != "category_id" {
		t.Fatalf("unexpected analytics identity: %+v", analytics)
	}
	if analytics.TotalRowCount != 5 || analytics.DistinctCount != 2 || analytics.NullCount != 1 {
		t.Fatalf("unexpected counts: %+v", analytics)
	}
	if analytics.TopValuesTruncated {
		t.Fatal("two distinct values should not be truncated")
	}
	if got, want := len(analytics.TopValues), 2; got != want {
		t.Fatalf("unexpected top values length: got %d want %d", got, want)
	}
	if analytics.TopValues[0].Value != "1" || analytics.TopValues[0].Count != 3 {
		t.Fatalf("unexpected most popular value: %+v", analytics.TopValues[0])
	}
	if analytics.TopValues[1].Value != "2" || analytics.TopValues[1].Count != 1 {
		t.Fatalf("unexpected second value: %+v", analytics.TopValues[1])
	}
	if got, want := len(analytics.ForeignKeys), 1; got != want {
		t.Fatalf("unexpected foreign keys length: got %d want %d", got, want)
	}
	foreignKey := analytics.ForeignKeys[0]
	if foreignKey.ReferencedTable != "categories" || foreignKey.ReferencedColumn != "id" {
		t.Fatalf("unexpected foreign key: %+v", foreignKey)
	}
}

func TestGetColumnAnalyticsValidatesMetadataAndLimitsTopValues(t *testing.T) {
	dbPath := t.TempDir() + "/analytics-limits.db"
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite fixture: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE "odd table" ("value""name" TEXT)`); err != nil {
		t.Fatalf("create fixture table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO "odd table" ("value""name") VALUES ('first'), ('second'), ('third')`); err != nil {
		t.Fatalf("insert fixture rows: %v", err)
	}

	analytics, err := GetColumnAnalytics(context.Background(), Config{Engine: "sqlite", DSN: dbPath}, "odd table", `value"name`, 2)
	if err != nil {
		t.Fatalf("analyze quoted identifiers: %v", err)
	}
	if len(analytics.TopValues) != 2 || !analytics.TopValuesTruncated {
		t.Fatalf("expected limited top values: %+v", analytics)
	}

	_, err = GetColumnAnalytics(context.Background(), Config{Engine: "sqlite", DSN: dbPath}, "odd table", "missing", 20)
	if err == nil || !strings.Contains(err.Error(), "column") {
		t.Fatalf("expected missing column error, got %v", err)
	}
	_, err = GetColumnAnalytics(context.Background(), Config{Engine: "redis", DSN: "redis://localhost:6379"}, "key", "value", 20)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected redis unsupported error, got %v", err)
	}
}

func TestQuoteSQLIdentifierUsesDatabaseDialect(t *testing.T) {
	tests := []struct {
		engine string
		name   string
		want   string
	}{
		{engine: "postgres", name: `order"items`, want: `"order""items"`},
		{engine: "sqlite", name: "group", want: `"group"`},
		{engine: "mysql", name: "user`events", want: "`user``events`"},
	}
	for _, test := range tests {
		if got := quoteSQLIdentifier(test.engine, test.name); got != test.want {
			t.Errorf("quoteSQLIdentifier(%q, %q) = %q, want %q", test.engine, test.name, got, test.want)
		}
	}
}
