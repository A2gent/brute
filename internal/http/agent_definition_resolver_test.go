package http

import (
	"testing"
	"time"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/storage"
)

func TestDefinitionForUnifiedAgentStoredHostOverridesLegacySubAgent(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)

	now := time.Now()
	legacyProject := "legacy-project"
	storedProject := "stored-project"
	if err := store.SaveSubAgent(&storage.SubAgent{
		ID:                "dup-agent",
		Name:              "Legacy",
		ProjectID:         &legacyProject,
		Provider:          "openai",
		Model:             "gpt-5.5",
		InstructionBlocks: "[]",
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("failed to save sub-agent: %v", err)
	}
	if err := store.SaveAgentDefinition(&storage.AgentDefinitionRecord{
		ID:        "dup-agent",
		Name:      "Imported Host",
		Runtime:   agentdef.RuntimeHost,
		ProjectID: &storedProject,
		DefinitionYAML: `
version: "1"
agent:
  id: dup-agent
  name: Imported Host
  kind: godot
runtime:
  type: host
`,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("failed to save agent definition: %v", err)
	}

	def, projectID, err := server.definitionForUnifiedAgent("dup-agent", "")
	if err != nil {
		t.Fatalf("definitionForUnifiedAgent failed: %v", err)
	}
	if def.Runtime.Type != agentdef.RuntimeHost {
		t.Fatalf("stored host definition must win over legacy sub-agent, got runtime %q", def.Runtime.Type)
	}
	if def.Agent.Name != "Imported Host" || def.Agent.Kind != "godot" {
		t.Fatalf("stored metadata not preserved: %+v", def.Agent)
	}
	if projectID != storedProject {
		t.Fatalf("project scope = %q, want %q", projectID, storedProject)
	}
}

func TestDefinitionForUnifiedAgentLegacyOnlyStillResolves(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)

	now := time.Now()
	projectID := "legacy-project"
	if err := store.SaveSubAgent(&storage.SubAgent{
		ID:                "legacy-only",
		Name:              "Legacy Only",
		ProjectID:         &projectID,
		Provider:          "openai",
		Model:             "gpt-5.5",
		InstructionBlocks: "[]",
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("failed to save sub-agent: %v", err)
	}

	def, discovered, err := server.definitionForUnifiedAgent("legacy-only", "")
	if err != nil {
		t.Fatalf("definitionForUnifiedAgent failed: %v", err)
	}
	if def.Runtime.Type != agentdef.RuntimeDocker || discovered != projectID {
		t.Fatalf("legacy fallback broken: runtime=%q project=%q", def.Runtime.Type, discovered)
	}
}
