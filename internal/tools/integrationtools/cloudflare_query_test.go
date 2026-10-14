package integrationtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

const cfTestZoneID = "0123456789abcdef0123456789abcdef"

func newCloudflareTestStore(t *testing.T, config map[string]string) storage.Store {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.SaveIntegration(&storage.Integration{
		ID: "cf-1", Provider: "cloudflare", Name: "Cloudflare", Mode: "notify_only", Enabled: true,
		Config: config, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("failed to save integration: %v", err)
	}
	return store
}

func TestCloudflareQueryToolResolvesDomainToZoneForDNSRecords(t *testing.T) {
	var paths []string
	var gotAuth, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/zones":
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + cfTestZoneID + `","name":"example.com"}]}`))
		case "/zones/" + cfTestZoneID + "/dns_records":
			gotQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"success":true,"result":[{"type":"A","name":"www.example.com"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := newCloudflareTestStore(t, map[string]string{"api_token": "secret", "base_url": server.URL})
	result, err := NewCloudflareQueryTool(store).Execute(context.Background(),
		json.RawMessage(`{"operation":"list_dns_records","zone":"Example.com","record_type":"a"}`))
	if err != nil || !result.Success {
		t.Fatalf("expected success, got err=%v result=%+v", err, result)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("unexpected auth header %q", gotAuth)
	}
	if len(paths) != 2 || paths[0] != "/zones" {
		t.Fatalf("expected zone lookup then dns call, got %v", paths)
	}
	if !strings.Contains(gotQuery, "type=A") {
		t.Fatalf("expected upper-cased type filter, got %q", gotQuery)
	}
	if !strings.Contains(result.Output, "www.example.com") {
		t.Fatalf("expected record in output, got %q", result.Output)
	}
}

func TestCloudflareQueryToolWorkersUsesOnlyVisibleAccount(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/accounts":
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"acc1","name":"Me"}]}`))
		case "/accounts/acc1/workers/scripts":
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"my-worker"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	store := newCloudflareTestStore(t, map[string]string{"api_token": "secret", "base_url": server.URL})
	result, _ := NewCloudflareQueryTool(store).Execute(context.Background(), json.RawMessage(`{"operation":"list_workers"}`))
	if !result.Success || !strings.Contains(result.Output, "my-worker") {
		t.Fatalf("unexpected result %+v (paths %v)", result, paths)
	}
}

func TestCloudflareQueryToolSurfacesAPIErrorsAndValidatesInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":9109,"message":"Unauthorized to access requested resource"}]}`))
	}))
	defer server.Close()

	store := newCloudflareTestStore(t, map[string]string{"api_token": "secret", "base_url": server.URL})
	tool := NewCloudflareQueryTool(store)

	result, _ := tool.Execute(context.Background(), json.RawMessage(`{"operation":"list_zones"}`))
	if result.Success || !strings.Contains(result.Error, "9109") || !strings.Contains(result.Error, "hint") {
		t.Fatalf("expected API error with hint, got %+v", result)
	}
	if result, _ := tool.Execute(context.Background(), json.RawMessage(`{"operation":"list_dns_records"}`)); result.Success || !strings.Contains(result.Error, "zone is required") {
		t.Fatalf("expected zone required error, got %+v", result)
	}
	if result, _ := tool.Execute(context.Background(), json.RawMessage(`{"operation":"purge_cache"}`)); result.Success || !strings.Contains(result.Error, "unsupported") {
		t.Fatalf("expected unsupported operation error, got %+v", result)
	}
}

func TestProbeCloudflareFallsBackToAccountTokenVerifyAndSummarizes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user/tokens/verify":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"Invalid API Token"}]}`))
		case "/accounts/acc1/tokens/verify":
			_, _ = w.Write([]byte(`{"success":true,"result":{"status":"active"}}`))
		case "/accounts":
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"acc1","name":"Me"}]}`))
		case "/zones":
			_, _ = w.Write([]byte(`{"success":true,"result":[{"name":"example.com"}],"result_info":{"total_count":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	integration := &storage.Integration{Provider: "cloudflare", Config: map[string]string{"api_token": "t", "account_id": "acc1", "base_url": server.URL}}
	msg, err := ProbeCloudflare(context.Background(), integration)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !strings.Contains(msg, "Me (ID acc1)") || !strings.Contains(msg, "1 domain(s): example.com") {
		t.Fatalf("unexpected summary %q", msg)
	}

	delete(integration.Config, "account_id")
	if _, err := ProbeCloudflare(context.Background(), integration); err == nil || !strings.Contains(err.Error(), "Invalid API Token") {
		t.Fatalf("expected invalid token error, got %v", err)
	}
}
