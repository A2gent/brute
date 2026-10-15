package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSourceSnapshotWatchesOnlyServerSources(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main")
	base := sourceSnapshot(root)

	write("main_test.go", "package main")
	write("tmp/gen.go", "package tmp")
	write("README.md", "x")
	if sourceSnapshot(root) != base {
		t.Fatal("tests, tmp dir and non-go files must not trigger reloads")
	}

	write("pkg/a.go", "package pkg")
	if sourceSnapshot(root) == base {
		t.Fatal("new Go source must trigger a reload")
	}
}

func TestServerIdle(t *testing.T) {
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"idle":false}`))
	}))
	defer busy.Close()
	if serverIdle(busy.URL) {
		t.Fatal("busy server reported idle")
	}

	// Old binaries without the endpoint, or a dead server, must not block restarts.
	missing := httptest.NewServer(http.NotFoundHandler())
	defer missing.Close()
	if !serverIdle(missing.URL) {
		t.Fatal("missing endpoint should count as idle")
	}
	if !serverIdle("http://127.0.0.1:1/unreachable") {
		t.Fatal("unreachable server should count as idle")
	}
}

func TestWaitIdleDefersUntilServerIdle(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 3 {
			_, _ = w.Write([]byte(`{"idle":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"idle":true}`))
	}))
	defer srv.Close()

	root := t.TempDir()
	t.Chdir(root)
	changed, interrupted := waitIdle(srv.URL, 10*time.Millisecond, sourceSnapshot("."), make(chan os.Signal))
	if changed || interrupted || calls < 4 {
		t.Fatalf("changed=%v interrupted=%v calls=%d", changed, interrupted, calls)
	}
}
