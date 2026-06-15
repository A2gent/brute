package http

import (
	"bytes"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestHTTPAccessLogDisabledByDefault(t *testing.T) {
	server, cleanup := newRequestLoggingTestServer(t)
	defer cleanup()

	var out bytes.Buffer
	server.setHTTPAccessLogWriter(&out)

	req := httptest.NewRequest(stdhttp.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("health status = %d, want %d", rec.Code, stdhttp.StatusOK)
	}
	if got := out.String(); got != "" {
		t.Fatalf("expected request logging to stay silent by default, got %q", got)
	}
}

func TestHTTPAccessLogWritesStartAndCompletionWhenEnabled(t *testing.T) {
	server, cleanup := newRequestLoggingTestServer(t)
	defer cleanup()

	var out bytes.Buffer
	server.EnableHTTPAccessLog(&out)

	req := httptest.NewRequest(stdhttp.MethodGet, "/health", nil)
	req.RemoteAddr = "192.0.2.10:54321"
	req.Header.Set("User-Agent", "request-logging-test")
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("health status = %d, want %d", rec.Code, stdhttp.StatusOK)
	}

	log := out.String()
	for _, want := range []string{
		"HTTP request started",
		"HTTP request completed",
		"method=GET",
		"path=/health",
		"remote=192.0.2.10:54321",
		"user_agent=\"request-logging-test\"",
		"status=200",
		"duration=",
		"bytes=",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("request log missing %q in:\n%s", want, log)
		}
	}
}

func newRequestLoggingTestServer(t *testing.T) (*Server, func()) {
	t.Helper()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()
	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)

	return server, func() {
		_ = store.Close()
	}
}
