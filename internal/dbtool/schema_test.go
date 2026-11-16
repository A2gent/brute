package dbtool

import (
	"context"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestSelectedSchemaDefaults(t *testing.T) {
	cases := []struct {
		cfg  Config
		want string
	}{
		{Config{Engine: "postgres"}, ""},
		{Config{Engine: "postgres", Schema: " sales "}, "sales"},
		{Config{Engine: "mysql"}, ""},
		{Config{Engine: "mysql", Schema: "shop"}, "shop"},
	}
	for _, tc := range cases {
		if got := selectedSchema(tc.cfg); got != tc.want {
			t.Errorf("selectedSchema(%+v) = %q, want %q", tc.cfg, got, tc.want)
		}
	}
}

func TestApplySchemaToDSN(t *testing.T) {
	got, err := applySchemaToDSN("postgres", "postgres://u:p@localhost:5432/app?sslmode=disable", "Sales")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "search_path=%22Sales%22%2C+public") || !strings.Contains(got, "sslmode=disable") {
		t.Fatalf("unexpected postgres URL DSN: %s", got)
	}

	got, err = applySchemaToDSN("postgres", "host=localhost dbname=app", `it's`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `host=localhost dbname=app search_path='"it\'s", public'`; got != want {
		t.Fatalf("postgres key/value DSN = %s, want %s", got, want)
	}

	got, err = applySchemaToDSN("mysql", "user:pass@tcp(localhost:3306)/app?parseTime=true", "shop")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mysql.ParseDSN(got)
	if err != nil || parsed.DBName != "shop" || !parsed.ParseTime {
		t.Fatalf("unexpected mysql DSN %s: %+v %v", got, parsed, err)
	}

	for _, engine := range []string{"sqlite", "redis"} {
		if got, _ := applySchemaToDSN(engine, "file.db", "ignored"); got != "file.db" {
			t.Fatalf("%s DSN should be unchanged, got %s", engine, got)
		}
	}
	if got, _ := applySchemaToDSN("postgres", "host=x", ""); got != "host=x" {
		t.Fatalf("empty schema should keep DSN, got %s", got)
	}
	if _, err := applySchemaToDSN("postgres", "host=x", "bad\x00name"); err == nil {
		t.Fatal("expected NUL schema name to be rejected")
	}
}

func TestListSchemasUnsupportedEngineReturnsEmpty(t *testing.T) {
	schemas, err := ListSchemas(context.Background(), Config{Engine: "sqlite", DSN: ":memory:"})
	if err != nil || schemas == nil || len(schemas) != 0 {
		t.Fatalf("expected empty list, got %v %v", schemas, err)
	}
}
