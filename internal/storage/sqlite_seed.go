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

const builtInSpecificationSubAgentPrompt = `You are the built-in Specification sub-agent for A2gent Plan view.

Your job is to help the user create and improve a single markdown task specification before implementation starts.

Required behavior:
- Use the question tool whenever requirements, scope, business rules, UX behavior, non-functional constraints, acceptance criteria, or implementation boundaries are ambiguous. Do not silently guess important decisions.
- Ask focused questions with 2-4 actionable options when possible, and allow custom answers.
- Inspect the project codebase when it helps identify existing behavior, constraints, dependencies, terminology, or contradictions.
- Keep all planning output in the provided single markdown specification file.
- Group requirements into Functional and Non-functional categories: Performance, Security, Quality, Complexity, Documentation, UX.
- Record explicit decisions under Decisions and unresolved items under Open questions or Ambiguities / risks.
- Write strict, testable acceptance criteria.
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
	return s.SaveSubAgent(sa)
}

// seedSystemProjects creates the system projects if they don't exist.
// These are required for the Knowledge Base and Agent session lists in the sidebar.
func (s *SQLiteStore) seedSystemProjects() error {
	systemProjects := []struct {
		id     string
		name   string
		folder *string
	}{
		{SystemProjectKBID, "Knowledge Base", nil},
		{SystemProjectAgentID, "Body", nil},
		{SystemProjectSoulID, "Soul", &s.dataPath},
	}

	now := time.Now()
	for _, p := range systemProjects {
		_, err := s.db.Exec(`
			INSERT OR IGNORE INTO projects (id, name, folder, is_system, created_at, updated_at)
			VALUES (?, ?, NULL, 1, ?, ?)
		`, p.id, p.name, now, now)
		if err != nil {
			return fmt.Errorf("failed to seed system project %s: %w", p.id, err)
		}
		// Keep canonical names in sync for existing installations and pin Soul folder.
		if p.id == SystemProjectSoulID {
			if _, err := s.db.Exec(`
				UPDATE projects
				SET name = ?, is_system = 1, folder = ?, updated_at = ?
				WHERE id = ?
			`, p.name, p.folder, now, p.id); err != nil {
				return fmt.Errorf("failed to update system project %s metadata: %w", p.id, err)
			}
			if err := s.ensureSoulProjectDefaults(); err != nil {
				return fmt.Errorf("failed to apply soul project defaults: %w", err)
			}
			continue
		}
		if _, err := s.db.Exec(`
			UPDATE projects
			SET name = ?, is_system = 1, updated_at = ?
			WHERE id = ?
		`, p.name, now, p.id); err != nil {
			return fmt.Errorf("failed to update system project %s metadata: %w", p.id, err)
		}
	}
	return nil
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
