package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestEnableClaudeUsageInstallsStatusLineWithoutReplacingSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(claudeRateLimitsCachePathEnv, filepath.Join(home, ".a2gent", "limits.json"))
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"model":"sonnet","permissions":{"allow":["Read"]}}`), 0600); err != nil {
		t.Fatal(err)
	}

	server := newAnthropicUsageTestServer(t)
	router := chi.NewRouter()
	router.Post("/providers/{providerType}/usage/enable", server.handleEnableClaudeUsage)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/providers/anthropic/usage/enable", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if string(settings["model"]) != `"sonnet"` || !strings.Contains(string(settings["permissions"]), "Read") {
		t.Fatalf("existing settings changed: %s", data)
	}
	var statusLine struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(settings["statusLine"], &statusLine); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(statusLine.Command, "python3 ") {
		t.Fatalf("unexpected command: %q", statusLine.Command)
	}
	scriptPath := strings.Trim(strings.TrimPrefix(statusLine.Command, "python3 "), "'")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "rate_limits") || !strings.Contains(string(script), "limits.json") {
		t.Fatalf("unexpected script: %s", script)
	}
	// Exercise the installed collector, not just its configuration.
	cmd := exec.Command("python3", scriptPath)
	cmd.Stdin = strings.NewReader(`{"rate_limits":{"five_hour":{"used_percentage":37,"resets_at":4102444800}}}`)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("collector: %v: %s", err, output)
	}
	usage := server.providerUsageStatus(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "anthropic")
	if usage.Status != providerUsageStatusAvailable || len(usage.UsageBars) != 1 || usage.UsageBars[0].LeftPercent != 63 {
		t.Fatalf("collector did not produce usage: %+v", usage)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/providers/anthropic/usage/enable", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("idempotent status = %d: %s", response.Code, response.Body.String())
	}
}

func TestEnableClaudeUsageDoesNotOverwriteExistingStatusLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"statusLine":{"command":"my-statusline"}}`)
	if err := os.WriteFile(settingsPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	server := newAnthropicUsageTestServer(t)
	router := chi.NewRouter()
	router.Post("/providers/{providerType}/usage/enable", server.handleEnableClaudeUsage)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/providers/anthropic/usage/enable", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatalf("settings modified: %s", data)
	}
}

func TestEnableClaudeUsageRejectsCustomInstance(t *testing.T) {
	server := newAnthropicUsageTestServer(t)
	router := chi.NewRouter()
	router.Post("/providers/{providerType}/usage/enable", server.handleEnableClaudeUsage)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/providers/anthropic:work/usage/enable", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
}
