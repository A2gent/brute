package agentdef

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/storage"
)

// FromSubAgent converts a legacy Brute sub-agent into a docker-runtime unified
// definition. Host execution is retired: rich sub-agent configuration (LLM,
// tools, instruction blocks, project binding) carries over onto Docker agents.
// Project binding stays in the local section so the portable part of the YAML
// never carries machine-specific IDs.
func FromSubAgent(sa *storage.SubAgent) (*Definition, error) {
	if sa == nil {
		return nil, fmt.Errorf("sub-agent is empty")
	}

	blocks, err := parseInstructionBlocks(sa.InstructionBlocks)
	if err != nil {
		return nil, fmt.Errorf("sub-agent %s has invalid instruction blocks: %w", sa.ID, err)
	}

	def := &Definition{
		Version: CurrentVersion,
		Agent: AgentMeta{
			ID:   sa.ID,
			Name: sa.Name,
		},
		Runtime: Runtime{Type: RuntimeDocker, Lifecycle: "warm"},
		LLM: LLM{
			Provider: sa.Provider,
			Model:    sa.Model,
		},
		Instructions: Instructions{Blocks: blocks},
		// Sub-agents historically worked read-write on the parent session's
		// project, so the migrated definitions preserve that ergonomic.
		Workspace: Workspace{
			Scope: WorkspaceScopeCurrentProject,
			Mount: WorkspaceMountRW,
		},
	}

	if len(sa.EnabledTools) > 0 {
		def.Tools = Tools{Mode: ToolsModeAllow, Enabled: append([]string(nil), sa.EnabledTools...)}
	} else {
		def.Tools = Tools{Mode: ToolsModeAll}
	}

	if sa.ProjectID != nil && strings.TrimSpace(*sa.ProjectID) != "" {
		def.Workspace.Scope = WorkspaceScopeConfiguredProject
		def.Local.ProjectBindings = map[string]string{
			WorkspaceScopeConfiguredProject: strings.TrimSpace(*sa.ProjectID),
		}
	}

	def.Normalize()
	return def, nil
}

// ToSubAgentConfig extracts the rich saved-agent configuration shape from any
// definition: name, LLM, tool allow-list, instruction blocks, and project
// binding. Prompt composition, delegated sessions, and A2A inbound routing use this
// so the unified definition stays the single source of truth.
func ToSubAgentConfig(def *Definition) (*storage.SubAgent, error) {
	if def == nil {
		return nil, fmt.Errorf("agent definition is empty")
	}
	def.Normalize()
	if err := def.Validate(); err != nil {
		return nil, err
	}

	name := def.Agent.Name
	if name == "" {
		name = def.Agent.ID
	}

	blocks := def.Instructions.Blocks
	if system := strings.TrimSpace(def.Instructions.System); system != "" {
		// Definitions written by hand may use instructions.system instead of a
		// text block; fold it in so every consumer sees one block list.
		blocks = append([]InstructionBlock{{Type: "text", Value: system}}, blocks...)
	}
	blocksJSON, err := encodeInstructionBlocks(blocks)
	if err != nil {
		return nil, err
	}

	sa := &storage.SubAgent{
		ID:                def.Agent.ID,
		Name:              name,
		Provider:          def.LLM.Provider,
		Model:             def.LLM.Model,
		EnabledTools:      []string{},
		InstructionBlocks: blocksJSON,
	}
	if def.Tools.Mode == ToolsModeAllow {
		sa.EnabledTools = append([]string(nil), def.Tools.Enabled...)
	}
	if binding := strings.TrimSpace(def.Local.ProjectBindings[WorkspaceScopeConfiguredProject]); binding != "" {
		sa.ProjectID = &binding
	}
	return sa, nil
}

func parseInstructionBlocks(raw string) ([]InstructionBlock, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" {
		return nil, nil
	}
	var blocks []InstructionBlock
	if err := json.Unmarshal([]byte(trimmed), &blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}

func encodeInstructionBlocks(blocks []InstructionBlock) (string, error) {
	if len(blocks) == 0 {
		return "[]", nil
	}
	encoded, err := json.Marshal(blocks)
	if err != nil {
		return "", fmt.Errorf("failed to encode instruction blocks: %w", err)
	}
	return string(encoded), nil
}
