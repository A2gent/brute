package skills

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v2"
)

// LoadSkillsFromDirectory loads all skills from a directory
func LoadSkillsFromDirectory(dir string, config *SkillConfig) ([]*Skill, error) {
	skills := make([]*Skill, 0)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		// Skip hidden directories
		if d.IsDir() {
			if path != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}

		// Only process SKILL.md files or markdown files
		if !isSkillFile(d.Name()) {
			return nil
		}

		// Load skill
		skill, err := LoadSkill(path)
		if err != nil {
			// Skip invalid skills but don't fail the whole operation
			return nil
		}

		// Calculate relative path
		relPath, _ := filepath.Rel(dir, path)
		skill.RelativePath = filepath.ToSlash(relPath)

		// Apply configuration
		skill.Strategy = GetSkillStrategy(skill.Name, config)
		skill.Priority = GetSkillPriority(skill.Name, config)
		skill.Triggers = GetSkillTriggers(skill.Name, config)

		skills = append(skills, skill)
		return nil
	})

	return skills, err
}

// LoadSkill loads a single skill from a file
func LoadSkill(path string) (*Skill, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	skill := &Skill{
		Path: path,
	}

	// Try to parse frontmatter
	if hasFrontmatter(content) {
		frontmatter, body := extractFrontmatter(content)
		if err := yaml.Unmarshal(frontmatter, skill); err == nil {
			skill.Body = string(body)
		} else {
			// Fallback: use entire content as body
			skill.Body = string(content)
			skill.Name = deriveNameFromContent(content, path)
		}
	} else {
		// No frontmatter: legacy format
		skill.Body = string(content)
		skill.Name = deriveNameFromContent(content, path)
	}

	// Ensure name is set
	if skill.Name == "" {
		skill.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(filepath.Base(path)))
	}

	return skill, nil
}

// hasFrontmatter checks if content starts with YAML frontmatter
func hasFrontmatter(content []byte) bool {
	return bytes.HasPrefix(content, []byte("---\n")) || bytes.HasPrefix(content, []byte("---\r\n"))
}

// extractFrontmatter extracts YAML frontmatter and body from content
func extractFrontmatter(content []byte) (frontmatter []byte, body []byte) {
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) < 3 {
		return nil, content
	}

	// Find closing ---
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		trimmed := bytes.TrimSpace(lines[i])
		if bytes.Equal(trimmed, []byte("---")) {
			endIdx = i
			break
		}
	}

	if endIdx < 0 {
		return nil, content
	}

	// Extract frontmatter (between first and second ---)
	frontmatter = bytes.Join(lines[1:endIdx], []byte("\n"))

	// Extract body (after second ---)
	if endIdx+1 < len(lines) {
		body = bytes.Join(lines[endIdx+1:], []byte("\n"))
	}

	return frontmatter, body
}

// deriveNameFromContent extracts skill name from first heading or filename
func deriveNameFromContent(content []byte, path string) string {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if heading != "" {
				return heading
			}
		}
		break
	}

	// Fallback to filename
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(filepath.Base(path)))
}

// isSkillFile checks if a file is a skill file (SKILL.md or *.md/*.markdown)
func isSkillFile(name string) bool {
	lower := strings.ToLower(name)
	return lower == "skill.md" || strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown")
}
