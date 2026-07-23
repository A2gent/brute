package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Seed helpers keep built-in records and Soul defaults together because migrations call them as one bootstrap step.

// System project IDs - must match frontend constants in Sidebar.tsx
const (
	SystemProjectKBID    = "system-kb"
	SystemProjectAgentID = "system-agent"
	SystemProjectSoulID  = "system-soul"
)

const BuiltInSpecificationSubAgentID = "builtin-specification-agent"

const agentDefinitionSchemaRelPath = "agent-definitions/agent-definition.schema.yaml"

const agentDefinitionSchemaManagedMarker = "# A2gent managed agent definition schema"

const agentDefinitionSchemaYAML = agentDefinitionSchemaManagedMarker + `
$schema: "https://json-schema.org/draft/2020-12/schema"
$id: "https://a2gent.local/schemas/agent-definition.v1.schema.json"
title: A2gent Agent Definition
description: Unified reusable agent definition. Local reusable agents run in Docker; runtime.type=host is accepted only as a legacy import marker and is coerced to docker.
type: object
additionalProperties: false
required:
  - agent
properties:
  version:
    type: string
    const: "1"
    default: "1"
  agent:
    type: object
    additionalProperties: false
    anyOf:
      - required: [id]
      - required: [name]
    properties:
      id:
        type: string
        pattern: "^[a-zA-Z0-9][a-zA-Z0-9_.-]*$"
      name:
        type: string
        minLength: 1
      emoji:
        type: string
      description:
        type: string
      kind:
        type: string
  runtime:
    type: object
    additionalProperties: false
    properties:
      type:
        type: string
        enum: [docker, remote, host]
        default: docker
        description: Use docker for local reusable agents. host is legacy-import-only and is normalized to docker.
      image:
        type: string
        default: a2gent-brute:latest
      lifecycle:
        type: string
        enum: [warm]
        default: warm
      resources:
        type: object
        additionalProperties: false
        properties:
          cpus:
            type: string
          memory:
            type: string
          gpus:
            type: string
  llm:
    type: object
    additionalProperties: false
    properties:
      provider:
        type: string
      model:
        type: string
  instructions:
    type: object
    additionalProperties: false
    properties:
      system:
        type: string
      blocks:
        type: array
        items:
          type: object
          additionalProperties: false
          required: [type]
          properties:
            type:
              type: string
              examples: [builtin_tools, integration_skills, external_markdown_skills, mcp_servers, text, file]
            value:
              type: string
            enabled:
              type: boolean
  workspace:
    type: object
    additionalProperties: false
    properties:
      scope:
        type: string
        enum: [none, current_project, configured_project, selected_projects, all_projects, explicit_volumes, snapshot]
        default: current_project
      mount:
        type: string
        enum: [ro, rw]
        default: ro
  tools:
    type: object
    additionalProperties: false
    properties:
      mode:
        type: string
        enum: [all, allow]
        default: all
      enabled:
        type: array
        items:
          type: string
      disabled:
        type: array
        items:
          type: string
  skills:
    type: object
    additionalProperties: false
    properties:
      external_markdown:
        type: array
        items:
          type: string
      integrations:
        type: array
        items:
          type: string
  mcp:
    type: object
    additionalProperties: false
    properties:
      servers:
        type: array
        items:
          type: string
  secrets:
    type: object
    additionalProperties: false
    properties:
      required:
        type: array
        items:
          type: string
  networking:
    type: object
    additionalProperties: false
    properties:
      internet_access:
        type: boolean
  publish:
    type: object
    additionalProperties: false
    properties:
      square:
        type: object
        additionalProperties: false
        properties:
          category:
            type: string
          discoverable:
            type: boolean
  local:
    type: object
    additionalProperties: false
    description: Machine-local installation details. Strip this before publishing templates.
    properties:
      host_port:
        type: integer
        description: Optional fixed host port for the child Brute HTTP server. Omit this field to let A2gent choose an available port automatically.
        minimum: 1
        maximum: 65535
      project_bindings:
        type: object
        additionalProperties:
          type: string
      credentials:
        type: object
        additionalProperties:
          type: object
          additionalProperties: false
          properties:
            env:
              type: string
            file:
              type: string
      definition_dir:
        type: string
        description: Optional local folder that contains this definition's YAML, skills, and adjacent settings. Folder imports set this automatically and Docker agents mount it read-only at /soul/agents/<agent-id>.
examples:
  - version: "1"
    agent:
      id: code-reviewer
      name: Code Reviewer
      description: Reviews code changes for correctness and regressions.
      kind: reviewer
    runtime:
      type: docker
      image: a2gent-brute:latest
      lifecycle: warm
    llm:
      provider: openai
      model: gpt-5.5
    instructions:
      system: |
        You are a focused code reviewer. Prioritize correctness risks.
      blocks:
        - type: builtin_tools
          enabled: true
    workspace:
      scope: current_project
      mount: ro
    tools:
      mode: allow
      enabled: [read, grep, find_files]
      disabled: [delegate_to_agent]
`

const builtInSpecificationSubAgentPrompt = `You are the built-in Specification sub-agent for A2gent Plan view.

Your job is to help the user create and improve bilingual markdown task specifications before implementation starts.

Required behavior:
- Use caesar/docs/specification-standard.md as the formatting and parsing authority when it is available in the workspace. If the file is unavailable, apply the same rules described below.
- Use the question tool whenever requirements, scope, business rules, UX behavior, non-functional constraints, acceptance criteria, or implementation boundaries are ambiguous. Do not silently guess important decisions.
- Ask focused questions with 2-4 actionable options when possible, and allow custom answers.
- Inspect the project codebase when it helps identify existing behavior, constraints, dependencies, terminology, or contradictions.
- Treat the .md file as the English source of truth and its paired .ru.md file as the Russian review translation.
- Keep both language versions synchronized after every planning edit, preserving matching requirement IDs and meaning.
- Group requirements into Functional and Non-functional categories: Performance, Security, Quality, Complexity, Documentation, UX.
- Record explicit decisions under Decisions and unresolved items under Open questions or Ambiguities / risks.
- Write strict, testable acceptance criteria.
- When asked to reformat an existing specification, preserve intent and IDs where possible, convert parser-relevant prose/tables into markdown checklist items, and place every item under the standard Requirements-tab headings.
- Detect contradictions between requirements, decisions, and existing code. Resolve them through questions before editing the spec.
- Do not implement product/code changes. Your deliverable is an improved specification document.
- Preserve clear IDs such as REQ-F-001, REQ-NF-SEC-001, DEC-001, Q-001, RISK-001, AC-001.

When the specification is ready, summarize remaining risks and say whether it is ready for implementation sessions.`

func (s *SQLiteStore) seedBuiltInSubAgents() error {
	instructionBlocks, err := json.Marshal([]map[string]interface{}{
		{"type": "builtin_tools", "value": "", "enabled": true},
		{"type": "text", "value": builtInSpecificationSubAgentPrompt, "enabled": true},
	})
	if err != nil {
		return fmt.Errorf("failed to encode specification sub-agent instructions: %w", err)
	}

	now := time.Now()
	// WHY: Plan view launches this stable sub-agent ID directly; seeding keeps the
	// built-in specialist available without requiring each user to configure it.
	sa := &SubAgent{
		ID:                BuiltInSpecificationSubAgentID,
		Name:              "Specification",
		Provider:          "",
		Model:             "",
		EnabledTools:      []string{},
		InstructionBlocks: string(instructionBlocks),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	existing, err := s.GetSubAgent(BuiltInSpecificationSubAgentID)
	if err == nil && subAgentMatchesSeed(existing, sa) {
		return nil
	}
	if err != nil && !strings.Contains(err.Error(), "sub-agent not found") {
		return err
	}
	return s.SaveSubAgent(sa)
}

// seedSystemProjects creates the system projects if they don't exist.
// These are required for the Knowledge Base and Agent session lists in the sidebar.
func (s *SQLiteStore) seedSystemProjects() error {
	var bodyFolder *string
	if strings.TrimSpace(s.dataPath) != "" {
		folder := filepath.Join(s.dataPath, "body")
		bodyFolder = &folder
	}

	systemProjects := []struct {
		id     string
		name   string
		folder *string
	}{
		{SystemProjectKBID, "Knowledge Base", nil},
		{SystemProjectAgentID, "Body", bodyFolder},
		{SystemProjectSoulID, "Soul", &s.dataPath},
	}

	now := time.Now()
	for _, p := range systemProjects {
		project, err := s.GetProject(p.id)
		if err != nil {
			if !strings.Contains(err.Error(), "project not found") {
				return fmt.Errorf("failed to inspect system project %s: %w", p.id, err)
			}
			_, err := s.db.Exec(`
				INSERT INTO projects (id, name, folder, is_system, created_at, updated_at)
				VALUES (?, ?, ?, 1, ?, ?)
			`, p.id, p.name, p.folder, now, now)
			if err != nil {
				return fmt.Errorf("failed to seed system project %s: %w", p.id, err)
			}
			project, err = s.GetProject(p.id)
			if err != nil {
				return fmt.Errorf("failed to reload system project %s: %w", p.id, err)
			}
		}
		// Keep canonical names in sync for existing installations and pin Soul folder.
		if p.id == SystemProjectSoulID {
			if projectSystemMetadataNeedsUpdate(project, p.name, p.folder, true) {
				if _, err := s.db.Exec(`
					UPDATE projects
					SET name = ?, is_system = 1, folder = ?, updated_at = ?
					WHERE id = ?
				`, p.name, p.folder, now, p.id); err != nil {
					return fmt.Errorf("failed to update system project %s metadata: %w", p.id, err)
				}
			}
			if err := s.ensureSoulProjectDefaults(); err != nil {
				return fmt.Errorf("failed to apply soul project defaults: %w", err)
			}
			continue
		}
		if p.id == SystemProjectAgentID && p.folder != nil {
			if projectSystemMetadataNeedsUpdate(project, p.name, p.folder, false) {
				if _, err := s.db.Exec(`
					UPDATE projects
					SET name = ?, is_system = 1, folder = COALESCE(NULLIF(TRIM(folder), ''), ?), updated_at = ?
					WHERE id = ?
				`, p.name, *p.folder, now, p.id); err != nil {
					return fmt.Errorf("failed to update system project %s metadata: %w", p.id, err)
				}
			}
			if err := s.ensureBodyProjectDefaults(); err != nil {
				return fmt.Errorf("failed to apply body project defaults: %w", err)
			}
			continue
		}
		if project.Name != p.name || !project.IsSystem {
			if _, err := s.db.Exec(`
				UPDATE projects
				SET name = ?, is_system = 1, updated_at = ?
				WHERE id = ?
			`, p.name, now, p.id); err != nil {
				return fmt.Errorf("failed to update system project %s metadata: %w", p.id, err)
			}
		}
	}
	return nil
}

func subAgentMatchesSeed(existing, seed *SubAgent) bool {
	if existing == nil || seed == nil {
		return false
	}
	if existing.ID != seed.ID ||
		existing.Name != seed.Name ||
		existing.Provider != seed.Provider ||
		existing.Model != seed.Model ||
		existing.InstructionBlocks != seed.InstructionBlocks {
		return false
	}
	if (existing.ProjectID == nil) != (seed.ProjectID == nil) {
		return false
	}
	if existing.ProjectID != nil && seed.ProjectID != nil && *existing.ProjectID != *seed.ProjectID {
		return false
	}
	if len(existing.EnabledTools) != len(seed.EnabledTools) {
		return false
	}
	for i := range existing.EnabledTools {
		if existing.EnabledTools[i] != seed.EnabledTools[i] {
			return false
		}
	}
	return true
}

func projectSystemMetadataNeedsUpdate(project *Project, name string, folder *string, pinFolder bool) bool {
	if project == nil || project.Name != name || !project.IsSystem {
		return true
	}
	if pinFolder {
		return !nullableStringsEqual(project.Folder, folder)
	}
	return project.Folder == nil || strings.TrimSpace(*project.Folder) == ""
}

func nullableStringsEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func (s *SQLiteStore) ensureBodyProjectDefaults() error {
	project, err := s.GetProject(SystemProjectAgentID)
	if err != nil {
		return err
	}
	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		return nil
	}

	root := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(root) {
		root = filepath.Join(".", root)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}

	schemaPath := filepath.Join(root, filepath.FromSlash(agentDefinitionSchemaRelPath))
	if err := os.MkdirAll(filepath.Dir(schemaPath), 0o755); err != nil {
		return err
	}
	existing, err := os.ReadFile(schemaPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(existing) > 0 && !strings.Contains(string(existing), agentDefinitionSchemaManagedMarker) {
		return nil
	}
	return os.WriteFile(schemaPath, []byte(agentDefinitionSchemaYAML), 0o644)
}

const soulGitignoreManagedBlock = "# A2gent Soul defaults\nlogs/\n*.log\n"

func (s *SQLiteStore) ensureSoulProjectDefaults() error {
	if strings.TrimSpace(s.dataPath) == "" {
		return nil
	}

	if err := os.MkdirAll(s.dataPath, 0o755); err != nil {
		return err
	}

	gitignorePath := filepath.Join(s.dataPath, ".gitignore")
	existing, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	content := string(existing)
	if strings.Contains(content, "# A2gent Soul defaults") {
		return nil
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += soulGitignoreManagedBlock

	return os.WriteFile(gitignorePath, []byte(content), 0o644)
}
