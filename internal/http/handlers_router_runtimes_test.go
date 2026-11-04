package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRuntimeUsesRouter(t *testing.T) {
	named := LocalDockerAgent{Labels: map[string]string{dockerRuntimeLLMFingerprintLabelKey: "automatic_router||fast"}}
	def := LocalDockerAgent{Labels: map[string]string{dockerRuntimeLLMFingerprintLabelKey: "automatic_router||"}}
	pinned := LocalDockerAgent{Labels: map[string]string{dockerRuntimeLLMFingerprintLabelKey: "anthropic|sonnet|"}}
	if !runtimeUsesRouter(named, "fast") || runtimeUsesRouter(named, "") || runtimeUsesRouter(named, "slow") {
		t.Fatal("named router match is wrong")
	}
	if !runtimeUsesRouter(def, "") || runtimeUsesRouter(pinned, "") {
		t.Fatal("default router match is wrong")
	}
}

func TestCountRunningSessions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"status":"running"},{"status":"completed"},{"status":"running"}]`))
	}))
	defer srv.Close()
	n, ok := countRunningSessions(context.Background(), srv.URL)
	if !ok || n != 2 {
		t.Fatalf("got %d %v", n, ok)
	}
}

func TestRuntimeUsesRouterOldFingerprint(t *testing.T) {
	old := LocalDockerAgent{Labels: map[string]string{dockerRuntimeLLMFingerprintLabelKey: "automatic_router|"}}
	if !runtimeUsesRouter(old, "") {
		t.Fatal("two-part fingerprint must count as default router")
	}
}
