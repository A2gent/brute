package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Project settings helpers stay together because project persistence and prompt migration share the same encoding rules.

const (
	projectInstructionBlocksSettingKey        = "A2GENT_PROJECT_INSTRUCTION_BLOCKS"
	legacyAgentInstructionBlocksSettingKey    = "A2GENT_AGENT_INSTRUCTION_BLOCKS"
	legacyBranchTaskDocDirectorySettingPrefix = "A2GENT_PROJECT_BRANCH_TASK_DOC_DIRECTORY."
	legacyBranchTaskDocModeSettingPrefix      = "A2GENT_PROJECT_BRANCH_TASK_DOC_MODE."
	projectBranchTaskDocDirectorySettingKey   = "A2GENT_PROJECT_BRANCH_TASK_DOC_DIRECTORY"
	projectBranchTaskDocModeSettingKey        = "A2GENT_PROJECT_BRANCH_TASK_DOC_MODE"
)

type storedInstructionBlock struct {
	Type    string `json:"type"`
	Value   string `json:"value"`
	Enabled *bool  `json:"enabled,omitempty"`
}

func normalizeProjectSettings(settings map[string]string) map[string]string {
	if len(settings) == 0 {
		return map[string]string{}
	}
	normalized := make(map[string]string, len(settings))
	for key, value := range settings {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		normalized[trimmedKey] = strings.TrimSpace(value)
	}
	return normalized
}

func marshalProjectSettings(settings map[string]string) (string, error) {
	data, err := json.Marshal(normalizeProjectSettings(settings))
	if err != nil {
		return "", fmt.Errorf("failed to encode project settings: %w", err)
	}
	return string(data), nil
}

func normalizeProjectURLPatterns(patterns []string) []string {
	if len(patterns) == 0 {
		return []string{}
	}
	normalized := make([]string, 0, len(patterns))
	seen := make(map[string]struct{}, len(patterns))
	for _, pattern := range patterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	return normalized
}

func marshalProjectURLPatterns(patterns []string) (string, error) {
	data, err := json.Marshal(normalizeProjectURLPatterns(patterns))
	if err != nil {
		return "", fmt.Errorf("failed to encode project URL patterns: %w", err)
	}
	return string(data), nil
}

func unmarshalProjectURLPatterns(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []string{}
	}
	var patterns []string
	if err := json.Unmarshal([]byte(trimmed), &patterns); err != nil {
		return []string{}
	}
	return normalizeProjectURLPatterns(patterns)
}
func unmarshalProjectSettings(raw string) map[string]string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]string{}
	}
	settings := map[string]string{}
	if err := json.Unmarshal([]byte(trimmed), &settings); err != nil {
		return map[string]string{}
	}
	return normalizeProjectSettings(settings)
}

func extractLegacyProjectInstructionBlocks(raw string) []storedInstructionBlock {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var blocks []storedInstructionBlock
	if err := json.Unmarshal([]byte(trimmed), &blocks); err != nil {
		return nil
	}
	projectBlocks := make([]storedInstructionBlock, 0, len(blocks))
	for _, block := range blocks {
		blockType := strings.TrimSpace(block.Type)
		if blockType != "project_agents_md" && blockType != "branch_task_doc" {
			continue
		}
		projectBlocks = append(projectBlocks, storedInstructionBlock{
			Type:    blockType,
			Value:   strings.TrimSpace(block.Value),
			Enabled: block.Enabled,
		})
	}
	return projectBlocks
}

func parseStoredInstructionBlocks(raw string) []storedInstructionBlock {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []storedInstructionBlock{}
	}
	var blocks []storedInstructionBlock
	if err := json.Unmarshal([]byte(trimmed), &blocks); err != nil {
		return []storedInstructionBlock{}
	}
	return blocks
}

func appendMissingInstructionBlocks(existing []storedInstructionBlock, additions []storedInstructionBlock) ([]storedInstructionBlock, bool) {
	if len(additions) == 0 {
		return existing, false
	}
	seen := make(map[string]struct{}, len(existing))
	for _, block := range existing {
		seen[strings.TrimSpace(block.Type)+"\x00"+strings.TrimSpace(block.Value)] = struct{}{}
	}
	changed := false
	for _, block := range additions {
		key := strings.TrimSpace(block.Type) + "\x00" + strings.TrimSpace(block.Value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		existing = append(existing, block)
		changed = true
	}
	return existing, changed
}

func serializeStoredInstructionBlocks(blocks []storedInstructionBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	data, err := json.Marshal(blocks)
	if err != nil {
		return ""
	}
	return string(data)
}

func (s *SQLiteStore) migrateProjectPromptSettingsFromAppSettings() error {
	settings, err := s.GetSettings()
	if err != nil {
		return err
	}
	legacyBlocks := extractLegacyProjectInstructionBlocks(settings[legacyAgentInstructionBlocksSettingKey])
	hasLegacyBranchSettings := false
	for key := range settings {
		if strings.HasPrefix(key, legacyBranchTaskDocDirectorySettingPrefix) || strings.HasPrefix(key, legacyBranchTaskDocModeSettingPrefix) {
			hasLegacyBranchSettings = true
			break
		}
	}
	if len(legacyBlocks) == 0 && !hasLegacyBranchSettings {
		return nil
	}

	rows, err := s.db.Query(`SELECT id, settings FROM projects`)
	if err != nil {
		return err
	}

	type projectPromptSettingsRow struct {
		projectID   string
		rawSettings string
	}
	projectRows := []projectPromptSettingsRow{}
	for rows.Next() {
		var rawSettings sql.NullString
		var projectID string
		if err := rows.Scan(&projectID, &rawSettings); err != nil {
			rows.Close()
			return err
		}
		projectRows = append(projectRows, projectPromptSettingsRow{
			projectID:   projectID,
			rawSettings: rawSettings.String,
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, row := range projectRows {
		projectSettings := unmarshalProjectSettings(row.rawSettings)
		changed := false

		legacyDirectory := strings.TrimSpace(settings[legacyBranchTaskDocDirectorySettingPrefix+row.projectID])
		if legacyDirectory != "" && projectSettings[projectBranchTaskDocDirectorySettingKey] == "" {
			projectSettings[projectBranchTaskDocDirectorySettingKey] = legacyDirectory
			mode := strings.TrimSpace(settings[legacyBranchTaskDocModeSettingPrefix+row.projectID])
			if mode != "path" {
				mode = "content"
			}
			projectSettings[projectBranchTaskDocModeSettingKey] = mode
			changed = true
		}

		if len(legacyBlocks) > 0 {
			currentBlocks := parseStoredInstructionBlocks(projectSettings[projectInstructionBlocksSettingKey])
			mergedBlocks, blocksChanged := appendMissingInstructionBlocks(currentBlocks, legacyBlocks)
			if blocksChanged {
				projectSettings[projectInstructionBlocksSettingKey] = serializeStoredInstructionBlocks(mergedBlocks)
				changed = true
			}
		}

		if !changed {
			continue
		}
		encoded, err := marshalProjectSettings(projectSettings)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE projects SET settings = ?, updated_at = ? WHERE id = ?`, encoded, time.Now(), row.projectID); err != nil {
			return fmt.Errorf("failed to migrate project %s prompt settings: %w", row.projectID, err)
		}
	}
	return nil
}
