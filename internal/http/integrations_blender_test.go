package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

// writeFakeBlender creates an executable stub that mimics `blender --version`.
func writeFakeBlender(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "blender")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("failed to write fake blender: %v", err)
	}
	return path
}

func newBlenderTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sessionManager := session.NewManager(store)
	return NewServer(config.DefaultConfig(), nil, tools.NewManager("."), sessionManager, store, speechcache.New(0), 0)
}

func TestIntegrationCreateAcceptsBlenderProvider(t *testing.T) {
	server := newBlenderTestServer(t)
	binary := writeFakeBlender(t, "echo 'Blender 5.2.0 LTS'")

	body, err := json.Marshal(IntegrationRequest{
		Provider: "blender",
		Mode:     "notify_only",
		Config:   map[string]string{"binary_path": binary},
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/integrations", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleCreateIntegration(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp IntegrationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Provider != "blender" {
		t.Fatalf("expected provider blender, got %q", resp.Provider)
	}
	if resp.Name != "Blender" {
		t.Fatalf("expected default name Blender, got %q", resp.Name)
	}
	if resp.Config["binary_path"] != binary {
		t.Fatalf("expected binary_path preserved, got %q", resp.Config["binary_path"])
	}
}

func TestIntegrationCreateRejectsBlenderWithoutBinaryPath(t *testing.T) {
	server := newBlenderTestServer(t)

	body, _ := json.Marshal(IntegrationRequest{
		Provider: "blender",
		Mode:     "notify_only",
		Config:   map[string]string{},
	})
	req := httptest.NewRequest(http.MethodPost, "/integrations", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.handleCreateIntegration(rec, req)
	if rec.Code == http.StatusCreated {
		t.Fatalf("expected validation failure, got created: %s", rec.Body.String())
	}
}

func TestTestBlenderIntegrationReportsVersion(t *testing.T) {
	server := newBlenderTestServer(t)
	binary := writeFakeBlender(t, "echo 'Blender 5.2.0 LTS'")

	now := time.Now().UTC()
	integration := &storage.Integration{
		ID:        "blender-ok",
		Provider:  "blender",
		Name:      "Blender",
		Mode:      "notify_only",
		Enabled:   true,
		Config:    map[string]string{"binary_path": binary},
		CreatedAt: now,
		UpdatedAt: now,
	}

	ok, message := server.testBlenderIntegration(context.Background(), integration)
	if !ok {
		t.Fatalf("expected reachable blender, got %q", message)
	}
	if !strings.Contains(message, "5.2.0") {
		t.Fatalf("expected version in message, got %q", message)
	}
}

func TestTestBlenderIntegrationMissingBinary(t *testing.T) {
	server := newBlenderTestServer(t)

	integration := &storage.Integration{
		ID:       "blender-missing",
		Provider: "blender",
		Name:     "Blender",
		Mode:     "notify_only",
		Enabled:  true,
		Config:   map[string]string{"binary_path": filepath.Join(t.TempDir(), "nope")},
	}

	ok, message := server.testBlenderIntegration(context.Background(), integration)
	if ok {
		t.Fatalf("expected failure for missing binary, got %q", message)
	}
	if !strings.Contains(strings.ToLower(message), "blender") {
		t.Fatalf("expected blender error detail, got %q", message)
	}
}
