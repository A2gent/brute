package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestHandleAgentCard(t *testing.T) {
	t.Run("default agent name", func(t *testing.T) {
		// ARRANGE
		tempDir, err := os.MkdirTemp("", "a2a-test-*")
		if err != nil {
			t.Fatalf("Failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tempDir)

		cfg := &config.Config{}
		toolManager := tools.NewManager(".")
		store, err := storage.NewSQLiteStore(tempDir)
		if err != nil {
			t.Fatalf("Failed to create store: %v", err)
		}
		sessionManager := session.NewManager(store)
		speechClips := speechcache.New(0)

		server := NewServer(cfg, nil, toolManager, sessionManager, store, speechClips, 0)

		req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
		rec := httptest.NewRecorder()

		// ACT
		server.handleAgentCard(rec, req)

		// ASSERT
		if rec.Code != http.StatusOK {
			t.Errorf("Expected status %d, got %d", http.StatusOK, rec.Code)
		}

		contentType := rec.Header().Get("Content-Type")
		if contentType != "application/json" {
			t.Errorf("Expected Content-Type application/json, got %s", contentType)
		}

		var card AgentCard
		if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
			t.Fatalf("Failed to parse agent card: %v", err)
		}

		if card.Name != "A2gent" {
			t.Errorf("Expected default name 'A2gent', got '%s'", card.Name)
		}
		if card.Description == "" {
			t.Error("Expected Description to be set")
		}
		if card.Version == "" {
			t.Error("Expected Version to be set")
		}
		if len(card.SupportedInterfaces) == 0 {
			t.Error("Expected at least one SupportedInterface")
		}
		if len(card.Skills) == 0 {
			t.Error("Expected at least one Skill")
		}
		if len(card.Tools) == 0 {
			t.Error("Expected at least one Tool")
		}

		iface := card.SupportedInterfaces[0]
		if iface.URL == "" {
			t.Error("Expected Interface URL to be set")
		}
		if iface.ProtocolBinding == "" {
			t.Error("Expected Interface ProtocolBinding to be set")
		}
		if iface.ProtocolVersion == "" {
			t.Error("Expected Interface ProtocolVersion to be set")
		}

		for _, skill := range card.Skills {
			if skill.ID == "" {
				t.Error("Expected Skill ID to be set")
			}
			if skill.Name == "" {
				t.Error("Expected Skill Name to be set")
			}
			if skill.Description == "" {
				t.Error("Expected Skill Description to be set")
			}
		}

		for _, tool := range card.Tools {
			if tool.Name == "" {
				t.Error("Expected Tool Name to be set")
			}
			if tool.Description == "" {
				t.Error("Expected Tool Description to be set")
			}
		}
	})

	t.Run("custom agent name from settings", func(t *testing.T) {
		// ARRANGE
		tempDir, err := os.MkdirTemp("", "a2a-test-*")
		if err != nil {
			t.Fatalf("Failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tempDir)

		cfg := &config.Config{}
		toolManager := tools.NewManager(".")
		store, err := storage.NewSQLiteStore(tempDir)
		if err != nil {
			t.Fatalf("Failed to create store: %v", err)
		}

		customName := "MyCustomAgent"
		err = store.SaveSettings(map[string]string{
			"AAGENT_NAME": customName,
		})
		if err != nil {
			t.Fatalf("Failed to save settings: %v", err)
		}

		sessionManager := session.NewManager(store)
		speechClips := speechcache.New(0)

		server := NewServer(cfg, nil, toolManager, sessionManager, store, speechClips, 0)

		req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
		rec := httptest.NewRecorder()

		// ACT
		server.handleAgentCard(rec, req)

		// ASSERT
		if rec.Code != http.StatusOK {
			t.Errorf("Expected status %d, got %d", http.StatusOK, rec.Code)
		}

		var card AgentCard
		if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
			t.Fatalf("Failed to parse agent card: %v", err)
		}

		if card.Name != customName {
			t.Errorf("Expected custom name '%s', got '%s'", customName, card.Name)
		}
	})

	t.Run("tools list matches tool manager", func(t *testing.T) {
		// ARRANGE
		tempDir, err := os.MkdirTemp("", "a2a-test-*")
		if err != nil {
			t.Fatalf("Failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tempDir)

		cfg := &config.Config{}
		toolManager := tools.NewManager(".")
		store, err := storage.NewSQLiteStore(tempDir)
		if err != nil {
			t.Fatalf("Failed to create store: %v", err)
		}
		sessionManager := session.NewManager(store)
		speechClips := speechcache.New(0)

		server := NewServer(cfg, nil, toolManager, sessionManager, store, speechClips, 0)

		req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
		rec := httptest.NewRecorder()

		// ACT
		server.handleAgentCard(rec, req)

		// ASSERT
		var card AgentCard
		if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
			t.Fatalf("Failed to parse agent card: %v", err)
		}

		toolDefs := toolManager.GetDefinitions()
		if len(card.Tools) != len(toolDefs) {
			t.Errorf("Expected %d tools, got %d", len(toolDefs), len(card.Tools))
		}

		toolNames := make(map[string]bool)
		for _, tool := range card.Tools {
			toolNames[tool.Name] = true
		}

		for _, def := range toolDefs {
			if !toolNames[def.Name] {
				t.Errorf("Expected tool '%s' in agent card, but not found", def.Name)
			}
		}
	})
}
