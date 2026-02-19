package http

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/A2gent/brute/internal/skills"
	"gopkg.in/yaml.v2"
)

var integrationToolsByProvider = map[string][]string{
	"google_calendar": {"google_calendar_query"},
	"brave_search":    {"brave_search_query"},
	"elevenlabs":      {"elevenlabs_tts"},
	"telegram":        {"telegram_send_message"},
	"exa":             {"exa_search"},
}

var integrationToolNameSet = func() map[string]struct{} {
	out := make(map[string]struct{}, 8)
	for _, names := range integrationToolsByProvider {
		for _, name := range names {
			out[name] = struct{}{}
		}
	}
	return out
}()

type SkillFile struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Path         string `json:"path"`
	RelativePath string `json:"relative_path"`
}

type SkillDiscoverResponse struct {
	Folder string      `json:"folder"`
	Skills []SkillFile `json:"skills"`
}

type SkillBrowseResponse struct {
	Path    string          `json:"path"`
	Entries []MindTreeEntry `json:"entries"`
}

type BuiltInSkill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

type BuiltInSkillResponse struct {
	Skills []BuiltInSkill `json:"skills"`
}

type IntegrationToolInfo struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema,omitempty"`
}

type IntegrationBackedSkill struct {
	ID       string                `json:"id"`
	Name     string                `json:"name"`
	Provider string                `json:"provider"`
	Mode     string                `json:"mode"`
	Enabled  bool                  `json:"enabled"`
	Tools    []IntegrationToolInfo `json:"tools"`
}

type IntegrationBackedSkillsResponse struct {
	Skills []IntegrationBackedSkill `json:"skills"`
}

func (s *Server) handleListBuiltInSkills(w http.ResponseWriter, r *http.Request) {
	skills := make([]BuiltInSkill, 0, 16)

	toolDefinitions := s.toolManager.GetDefinitions()
	sort.Slice(toolDefinitions, func(i, j int) bool {
		return strings.ToLower(toolDefinitions[i].Name) < strings.ToLower(toolDefinitions[j].Name)
	})
	for _, definition := range toolDefinitions {
		if _, isIntegrationTool := integrationToolNameSet[definition.Name]; isIntegrationTool {
			continue
		}
		skills = append(skills, BuiltInSkill{
			ID:          "tool:" + definition.Name,
			Name:        definition.Name,
			Kind:        "tool",
			Description: strings.TrimSpace(definition.Description),
		})
	}

	s.jsonResponse(w, http.StatusOK, BuiltInSkillResponse{Skills: skills})
}

func (s *Server) handleListIntegrationBackedSkills(w http.ResponseWriter, r *http.Request) {
	integrations, err := s.store.ListIntegrations()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list integrations: "+err.Error())
		return
	}

	defByName := map[string]IntegrationToolInfo{}
	for _, definition := range s.toolManager.GetDefinitions() {
		if _, isIntegrationTool := integrationToolNameSet[definition.Name]; !isIntegrationTool {
			continue
		}
		defByName[definition.Name] = IntegrationToolInfo{
			Name:        definition.Name,
			Description: strings.TrimSpace(definition.Description),
			InputSchema: definition.InputSchema,
		}
	}

	skills := make([]IntegrationBackedSkill, 0, len(integrations))
	for _, integration := range integrations {
		if integration == nil {
			continue
		}
		toolNames := integrationToolsByProvider[strings.TrimSpace(integration.Provider)]
		tools := make([]IntegrationToolInfo, 0, len(toolNames))
		for _, toolName := range toolNames {
			if def, ok := defByName[toolName]; ok {
				tools = append(tools, def)
			}
		}
		sort.Slice(tools, func(i, j int) bool {
			return strings.ToLower(tools[i].Name) < strings.ToLower(tools[j].Name)
		})

		skills = append(skills, IntegrationBackedSkill{
			ID:       integration.ID,
			Name:     integration.Name,
			Provider: integration.Provider,
			Mode:     integration.Mode,
			Enabled:  integration.Enabled,
			Tools:    tools,
		})
	}

	sort.Slice(skills, func(i, j int) bool {
		left := strings.ToLower(skills[i].Provider + "|" + skills[i].Name)
		right := strings.ToLower(skills[j].Provider + "|" + skills[j].Name)
		return left < right
	})

	s.jsonResponse(w, http.StatusOK, IntegrationBackedSkillsResponse{Skills: skills})
}

func (s *Server) handleBrowseSkillDirectories(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		if homeDir, err := os.UserHomeDir(); err == nil {
			path = homeDir
		} else {
			path = string(os.PathSeparator)
		}
	}

	resolvedPath, err := filepath.Abs(path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid path")
		return
	}

	entries, err := os.ReadDir(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to list directory: "+err.Error())
		return
	}

	respEntries := make([]MindTreeEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		fullPath := filepath.Join(resolvedPath, entry.Name())
		hasChild := directoryHasChildren(fullPath)
		respEntries = append(respEntries, MindTreeEntry{
			Name:     entry.Name(),
			Path:     fullPath,
			Type:     "directory",
			HasChild: hasChild,
		})
	}

	sort.Slice(respEntries, func(i, j int) bool {
		return strings.ToLower(respEntries[i].Name) < strings.ToLower(respEntries[j].Name)
	})

	s.jsonResponse(w, http.StatusOK, SkillBrowseResponse{
		Path:    resolvedPath,
		Entries: respEntries,
	})
}

func (s *Server) handleDiscoverSkills(w http.ResponseWriter, r *http.Request) {
	folder := strings.TrimSpace(r.URL.Query().Get("folder"))
	if folder == "" {
		s.errorResponse(w, http.StatusBadRequest, "folder is required")
		return
	}

	resolvedFolder, err := filepath.Abs(folder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid folder path")
		return
	}

	info, err := os.Stat(resolvedFolder)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusBadRequest, "Folder does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access folder: "+err.Error())
		return
	}
	if !info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Path is not a folder")
		return
	}

	skills := make([]SkillFile, 0)
	walkErr := filepath.WalkDir(resolvedFolder, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			if path != resolvedFolder && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}

		if !isMarkdownFile(d.Name()) {
			return nil
		}

		relPath, err := filepath.Rel(resolvedFolder, path)
		if err != nil {
			return nil
		}

		// Parse skill metadata (name and description from frontmatter)
		name, description := parseSkillMetadata(path)
		if name == "" {
			name = strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
		}

		skills = append(skills, SkillFile{
			Name:         name,
			Description:  description,
			Path:         path,
			RelativePath: filepath.ToSlash(relPath),
		})
		return nil
	})
	if walkErr != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to scan skills folder: "+walkErr.Error())
		return
	}

	sort.Slice(skills, func(i, j int) bool {
		return strings.ToLower(skills[i].RelativePath) < strings.ToLower(skills[j].RelativePath)
	})

	s.jsonResponse(w, http.StatusOK, SkillDiscoverResponse{
		Folder: resolvedFolder,
		Skills: skills,
	})
}

// SkillMetadata represents the YAML frontmatter in SKILL.md files
type SkillMetadata struct {
	Name          string      `yaml:"name"`
	Description   string      `yaml:"description"`
	License       string      `yaml:"license,omitempty"`
	Compatibility string      `yaml:"compatibility,omitempty"`
	Homepage      string      `yaml:"homepage,omitempty"`
	Metadata      interface{} `yaml:"metadata,omitempty"`
	AllowedTools  string      `yaml:"allowed-tools,omitempty"`
}

// parseSkillMetadata extracts YAML frontmatter and metadata from a SKILL.md file
func parseSkillMetadata(path string) (name string, description string) {
	content, err := os.ReadFile(path)
	if err != nil {
		// Fallback to filename
		base := filepath.Base(path)
		return strings.TrimSuffix(base, filepath.Ext(base)), ""
	}

	// Check for YAML frontmatter (starts with ---)
	if bytes.HasPrefix(content, []byte("---\n")) || bytes.HasPrefix(content, []byte("---\r\n")) {
		// Find the closing ---
		lines := bytes.Split(content, []byte("\n"))
		endIdx := -1
		for i := 1; i < len(lines); i++ {
			trimmed := bytes.TrimSpace(lines[i])
			if bytes.Equal(trimmed, []byte("---")) {
				endIdx = i
				break
			}
		}

		if endIdx > 0 {
			// Extract frontmatter
			frontmatterBytes := bytes.Join(lines[1:endIdx], []byte("\n"))
			var metadata SkillMetadata
			if err := yaml.Unmarshal(frontmatterBytes, &metadata); err == nil {
				if metadata.Name != "" {
					return metadata.Name, metadata.Description
				}
			}
		}
	}

	// Fallback: extract first heading (old format)
	name = deriveSkillNameFromHeading(content, path)
	return name, ""
}

// deriveSkillNameFromHeading extracts skill name from first markdown heading (legacy)
func deriveSkillNameFromHeading(content []byte, path string) string {
	defaultName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(filepath.Base(path)))

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

	if defaultName == "" {
		return "Skill"
	}
	return defaultName
}

// deriveSkillName is the legacy function, kept for compatibility
func deriveSkillName(path, defaultName string) string {
	name, _ := parseSkillMetadata(path)
	if name == "" {
		return strings.TrimSpace(strings.TrimSuffix(defaultName, filepath.Ext(defaultName)))
	}
	return name
}

// RegistrySkill represents a skill from clawhub.ai (for API responses)
type RegistrySkill struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Author      string            `json:"author"`
	Version     string            `json:"version"`
	Downloads   int               `json:"downloads"`
	Rating      float64           `json:"rating"`
	Tags        []string          `json:"tags"`
	Metadata    map[string]string `json:"metadata"`
	DownloadURL string            `json:"download_url"`
	UpdatedAt   string            `json:"updated_at,omitempty"`
}

// SkillSearchRequest represents a search request
type SkillSearchRequest struct {
	Query string `json:"query"`
	Page  int    `json:"page"`
	Limit int    `json:"limit"`
}

// SkillSearchResponse represents search results
type SkillSearchResponse struct {
	Skills []RegistrySkill `json:"skills"`
	Total  int             `json:"total"`
	Page   int             `json:"page"`
	Limit  int             `json:"limit"`
}

// SkillInstallRequest represents an install request
type SkillInstallRequest struct {
	SkillID string `json:"skill_id"`
}

// SkillInstallResponse represents install result
type SkillInstallResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Name    string `json:"name"`
	Path    string `json:"path"`
}

// handleSearchRegistry searches/lists clawhub.ai skills using real API
func (s *Server) handleSearchRegistry(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))

	limit := 20
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	sort := r.URL.Query().Get("sort")
	if sort == "" {
		sort = "installsCurrent" // Default to most installed
	}

	client := skills.NewRegistryClient("")
	var resultSkills []RegistrySkill

	if query != "" {
		// Use search API for queries
		searchResp, err := client.SearchSkills(query, limit)
		if err != nil {
			s.errorResponse(w, http.StatusInternalServerError, fmt.Sprintf("Failed to search skills: %v", err))
			return
		}

		resultSkills = make([]RegistrySkill, len(searchResp.Results))
		for i, result := range searchResp.Results {
			resultSkills[i] = RegistrySkill{
				ID:          result.Slug,
				Name:        result.DisplayName,
				Description: result.Summary,
				Version:     result.Version,
				UpdatedAt:   fmt.Sprintf("%d", result.UpdatedAt),
			}
			if result.Stats != nil {
				resultSkills[i].Downloads = result.Stats.Downloads
				resultSkills[i].Rating = float64(result.Stats.Stars)
			}
		}
	} else {
		// Use list API with sorting when no query
		listResp, err := client.ListSkills(limit, sort)
		if err != nil {
			s.errorResponse(w, http.StatusInternalServerError, fmt.Sprintf("Failed to list skills: %v", err))
			return
		}

		resultSkills = make([]RegistrySkill, len(listResp.Items))
		for i, item := range listResp.Items {
			version := ""
			if item.LatestVersion != nil {
				version = item.LatestVersion.Version
			}

			resultSkills[i] = RegistrySkill{
				ID:          item.Slug,
				Name:        item.DisplayName,
				Description: item.Summary,
				Version:     version,
				Downloads:   item.Stats.Downloads,
				Rating:      float64(item.Stats.Stars),
				UpdatedAt:   fmt.Sprintf("%d", item.UpdatedAt),
			}
		}
	}

	s.jsonResponse(w, http.StatusOK, SkillSearchResponse{
		Skills: resultSkills,
		Total:  len(resultSkills),
		Page:   1,
		Limit:  limit,
	})
}

// handleInstallSkill installs a skill from clawhub.ai
func (s *Server) handleInstallSkill(w http.ResponseWriter, r *http.Request) {
	var req SkillInstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.SkillID == "" {
		s.errorResponse(w, http.StatusBadRequest, "skill_id is required")
		return
	}

	// Get skills folder from settings
	settings, err := s.store.GetSettings()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to get settings")
		return
	}

	folder := strings.TrimSpace(settings[skillsFolderSettingKey])
	if folder == "" {
		s.errorResponse(w, http.StatusBadRequest, "Skills folder is not configured")
		return
	}

	resolvedFolder, err := filepath.Abs(folder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid skills folder path")
		return
	}

	// Install skill using real ClawHub API v1
	_, err = skills.InstallSkill("", req.SkillID, resolvedFolder)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, fmt.Sprintf("Failed to install skill: %v", err))
		return
	}

	s.jsonResponse(w, http.StatusOK, SkillInstallResponse{
		Success: true,
		Message: fmt.Sprintf("Skill '%s' installed successfully", req.SkillID),
		Name:    req.SkillID,
		Path:    filepath.Join(resolvedFolder, req.SkillID),
	})
}

// handleDeleteSkill deletes a skill from the skills folder
func (s *Server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SkillPath string `json:"skill_path"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.SkillPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "skill_path is required")
		return
	}

	// Get skills folder from settings to validate the path
	settings, err := s.store.GetSettings()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to get settings")
		return
	}

	folder := strings.TrimSpace(settings[skillsFolderSettingKey])
	if folder == "" {
		s.errorResponse(w, http.StatusBadRequest, "Skills folder is not configured")
		return
	}

	resolvedFolder, err := filepath.Abs(folder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid skills folder path")
		return
	}

	// Resolve the skill path
	resolvedPath, err := filepath.Abs(req.SkillPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid skill path")
		return
	}

	// Security check: ensure the path is within the skills folder
	if !strings.HasPrefix(resolvedPath, resolvedFolder) {
		s.errorResponse(w, http.StatusBadRequest, "Skill path is outside the skills folder")
		return
	}

	// Get the skill directory (parent of SKILL.md)
	skillDir := filepath.Dir(resolvedPath)

	// Delete the skill directory
	if err := os.RemoveAll(skillDir); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, fmt.Sprintf("Failed to delete skill: %v", err))
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Skill deleted successfully"),
		"path":    skillDir,
	})
}
