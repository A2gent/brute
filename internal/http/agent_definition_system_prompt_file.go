package http

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/A2gent/brute/internal/agentdef"
)

const defaultAgentDefinitionSystemPromptFile = "system.md"

func resolveAgentDefinitionSystemPrompt(def *agentdef.Definition) (string, error) {
	if def == nil {
		return "", nil
	}
	if strings.TrimSpace(def.Instructions.SystemFile) == "" {
		return strings.TrimSpace(def.Instructions.System), nil
	}
	content, err := readAgentDefinitionSystemPromptFile(def)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(content), nil
}

func readAgentDefinitionSystemPromptFile(def *agentdef.Definition) (string, error) {
	pathRef := strings.TrimSpace(def.Instructions.SystemFile)
	if pathRef == "" {
		return "", nil
	}
	if filepath.IsAbs(pathRef) {
		return "", fmt.Errorf("instructions.system_file must be relative to the agent definition folder")
	}
	cleanRef := filepath.Clean(pathRef)
	if cleanRef == "." || strings.HasPrefix(cleanRef, ".."+string(filepath.Separator)) || cleanRef == ".." {
		return "", fmt.Errorf("instructions.system_file must stay inside the agent definition folder")
	}
	definitionDir := strings.TrimSpace(def.Local.DefinitionDir)
	if definitionDir == "" {
		return "", fmt.Errorf("instructions.system_file %q requires local.definition_dir", pathRef)
	}
	fullPath := filepath.Join(definitionDir, cleanRef)
	raw, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to read instructions.system_file %q: %w", pathRef, err)
	}
	return string(raw), nil
}

func applyResolvedAgentDefinitionSystemPrompt(def *agentdef.Definition) error {
	if def == nil {
		return nil
	}
	resolved, err := resolveAgentDefinitionSystemPrompt(def)
	if err != nil {
		return err
	}
	def.Instructions.System = resolved
	return nil
}

func resolveLocalDockerAgentSystemPrompt(req createLocalDockerAgentRequest) (string, error) {
	prompt := strings.TrimSpace(req.SystemPrompt)
	pathRef := strings.TrimSpace(req.SystemPromptFile)
	if pathRef == "" {
		return prompt, nil
	}
	if filepath.IsAbs(pathRef) {
		return "", fmt.Errorf("system_prompt_file must be relative to the YAML config directory")
	}
	cleanRef := filepath.Clean(pathRef)
	if cleanRef == "." || cleanRef == ".." || strings.HasPrefix(cleanRef, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("system_prompt_file must stay inside the YAML config directory")
	}
	baseDir := strings.TrimSpace(req.ConfigBaseDir)
	if baseDir == "" {
		baseDir = "."
	}
	raw, err := os.ReadFile(filepath.Join(baseDir, cleanRef))
	if err != nil {
		return "", fmt.Errorf("failed to read system_prompt_file %q: %w", pathRef, err)
	}
	return strings.TrimSpace(string(raw)), nil
}
