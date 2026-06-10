package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestRegisterCurrentA2AAgentRegistersAndSavesIntegration(t *testing.T) {
	var got squareRegisterAgentRequest
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/agents/register" {
			t.Fatalf("unexpected registry request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode registry request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{
			"agent": {
				"id": "agent-123",
				"name": %q,
				"agent_handle": %q,
				"public_id": %q,
				"approval_status": "pending",
				"discoverable": true
			},
			"api_key": "sq_generated",
			"message": "Agent registered and pending owner approval."
		}`, got.Name, got.AgentHandle, got.AgentHandle)
	}))
	defer registry.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := store.SaveSettings(map[string]string{
		agentNameSettingKey:              "My Laptop Agent",
		a2aRegistryOwnerEmailSettingKey: "owner@example.com",
	}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	server := &Server{store: store}
	resp, status, err := server.registerCurrentA2AAgent(context.Background(), registerCurrentA2AAgentRequest{
		RegistryURL:    registry.URL,
		Transport:      "grpc",
		SquareGRPCAddr: "square.example:9001",
	})
	if err != nil {
		t.Fatalf("registerCurrentA2AAgent returned error: status=%d err=%v", status, err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if resp.RegistryAPIKey != "sq_generated" || resp.RegistryAgentID != "agent-123" || resp.ApprovalStatus != "pending" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	if got.OwnerEmail != "owner@example.com" {
		t.Fatalf("owner_email = %q", got.OwnerEmail)
	}
	if got.Name != "My Laptop Agent" {
		t.Fatalf("name = %q", got.Name)
	}
	if got.NetworkAccess != "behind_nat" || got.EndpointURL != "" {
		t.Fatalf("unexpected network registration: access=%q endpoint=%q", got.NetworkAccess, got.EndpointURL)
	}
	if got.AgentType != "personal" || got.Category != "personal" {
		t.Fatalf("unexpected classification: type=%q category=%q", got.AgentType, got.Category)
	}
	if got.Discoverable == nil || !*got.Discoverable {
		t.Fatalf("discoverable should default to true")
	}
	if got.PricePerSession <= 0 {
		t.Fatalf("price_per_session should be set so Square accepts registration")
	}

	settings, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	savedHandle := settings[a2aRegistryAgentHandleSettingKey]
	if savedHandle == "" || got.AgentHandle != savedHandle {
		t.Fatalf("agent handle was not persisted: request=%q settings=%q", got.AgentHandle, savedHandle)
	}
	if strings.Contains(savedHandle, " ") || len(savedHandle) > 64 {
		t.Fatalf("saved handle is not registry-safe: %q", savedHandle)
	}

	integrations, err := store.ListIntegrations()
	if err != nil {
		t.Fatalf("ListIntegrations: %v", err)
	}
	if len(integrations) != 1 {
		t.Fatalf("expected one integration, got %d", len(integrations))
	}
	integration := integrations[0]
	if integration.Provider != "a2_registry" || !integration.Enabled {
		t.Fatalf("unexpected integration metadata: %+v", integration)
	}
	if integration.Config["api_key"] != "sq_generated" || integration.Config["owner_email"] != "owner@example.com" {
		t.Fatalf("integration did not save registry credentials: %+v", integration.Config)
	}
	if integration.Config["registry_url"] != registry.URL || integration.Config["square_grpc_addr"] != "square.example:9001" || integration.Config["transport"] != "grpc" {
		t.Fatalf("integration did not save registry/tunnel config: %+v", integration.Config)
	}
	if integration.Config["agent_handle"] != savedHandle || integration.Config["agent_id"] != "agent-123" {
		t.Fatalf("integration did not save registry identity: %+v", integration.Config)
	}
}

func TestRegisterCurrentA2AAgentRequiresOwnerEmail(t *testing.T) {
	called := false
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer registry.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	server := &Server{store: store}

	_, status, err := server.registerCurrentA2AAgent(context.Background(), registerCurrentA2AAgentRequest{
		RegistryURL:    registry.URL,
		SquareGRPCAddr: "square.example:9001",
	})
	if err == nil {
		t.Fatalf("expected owner_email validation error")
	}
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if called {
		t.Fatalf("registry should not be called when owner_email is missing")
	}
}
