package http

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/storage"
)

const (
	agentDefinitionsFolderSettingKey           = "AAGENT_AGENT_DEFINITIONS_FOLDER"
	projectAgentDefinitionsDirectorySettingKey = "A2GENT_PROJECT_AGENT_DEFINITIONS_DIRECTORY"
	defaultProjectAgentDefinitionsDirectory    = "agents"
	defaultGlobalAgentDefinitionsFolder        = "agents"
)

type discoveredAgentDefinition struct {
	ID            string
	Name          string
	ConfigPath    string
	DefinitionDir string
	Definition    *agentdef.Definition
	ProjectID     string
}

func (s *Server) resolveGlobalAgentDefinitionsDirectory(settings map[string]string) string {
	folder := strings.TrimSpace(settings[agentDefinitionsFolderSettingKey])
	soulFolder := s.soulProjectFolder()
	if soulFolder == "" {
		if folder == "" {
			return ""
		}
		if filepath.IsAbs(folder) {
			return filepath.Clean(folder)
		}
		return ""
	}
	if folder == "" {
		return filepath.Join(soulFolder, defaultGlobalAgentDefinitionsFolder)
	}
	if filepath.IsAbs(folder) {
		return filepath.Clean(folder)
	}
	return filepath.Join(soulFolder, folder)
}

func (s *Server) resolveProjectAgentDefinitionsDirectory(project *storage.Project) string {
	if project == nil || project.Folder == nil {
		return ""
	}
	rootFolder := strings.TrimSpace(*project.Folder)
	if rootFolder == "" {
		return ""
	}
	configured := ""
	if project.Settings != nil {
		configured = strings.TrimSpace(project.Settings[projectAgentDefinitionsDirectorySettingKey])
	}
	if configured == "" {
		configured = defaultProjectAgentDefinitionsDirectory
	}
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	return filepath.Join(rootFolder, configured)
}

func (s *Server) resolveScopedProjectAgentDefinitionsDirectory(project *storage.Project) string {
	if project == nil || project.Folder == nil {
		return s.resolveProjectAgentDefinitionsDirectory(project)
	}
	rootFolder := strings.TrimSpace(*project.Folder)
	if rootFolder == "" {
		return s.resolveProjectAgentDefinitionsDirectory(project)
	}
	configured := s.resolveProjectAgentDefinitionsDirectory(project)
	if configured == "" {
		return configured
	}
	rootAbs, err := filepath.Abs(rootFolder)
	if err != nil {
		return configured
	}
	dirAbs, err := filepath.Abs(configured)
	if err != nil {
		return filepath.Join(rootAbs, defaultProjectAgentDefinitionsDirectory)
	}
	rel, err := filepath.Rel(rootAbs, dirAbs)
	if err != nil || strings.HasPrefix(rel, "..") {
		// WHY: project listings must not scan a global agents folder configured via absolute path.
		return filepath.Join(rootAbs, defaultProjectAgentDefinitionsDirectory)
	}
	return dirAbs
}

func (s *Server) soulProjectFolder() string {
	project, err := s.store.GetProject(storage.SystemProjectSoulID)
	if err != nil || project == nil || project.Folder == nil {
		return ""
	}
	return strings.TrimSpace(*project.Folder)
}

func pathWithinDirectory(path string, root string) bool {
	path = strings.TrimSpace(path)
	root = strings.TrimSpace(root)
	if path == "" || root == "" {
		return false
	}
	pathAbs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return false
	}
	if pathAbs == rootAbs {
		return true
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func discoverAgentDefinitionsInDirectory(rootDir string) ([]discoveredAgentDefinition, []string) {
	rootDir = strings.TrimSpace(rootDir)
	if rootDir == "" {
		return nil, nil
	}

	info, err := os.Stat(rootDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, []string{fmt.Sprintf("agent definitions directory %s does not exist", rootDir)}
		}
		return nil, []string{"agent definitions directory unavailable: " + err.Error()}
	}
	if !info.IsDir() {
		return nil, []string{fmt.Sprintf("agent definitions path %s is not a directory", rootDir)}
	}

	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return nil, []string{"failed to read agent definitions directory: " + err.Error()}
	}

	discovered := make([]discoveredAgentDefinition, 0, len(entries))
	seenIDs := make(map[string]struct{}, len(entries))
	warnings := []string{}

	for _, entry := range entries {
		entryPath := filepath.Join(rootDir, entry.Name())
		if entry.IsDir() {
			configPath, resolveErr := resolveLocalDockerAgentYAMLDirectoryConfigFile(entryPath)
			if resolveErr != nil {
				continue
			}
			item, itemWarnings := discoveredAgentDefinitionFromConfigPath(configPath, entryPath)
			warnings = append(warnings, itemWarnings...)
			if item == nil {
				continue
			}
			if _, exists := seenIDs[item.ID]; exists {
				warnings = append(warnings, fmt.Sprintf("duplicate agent definition id %q in %s", item.ID, rootDir))
				continue
			}
			seenIDs[item.ID] = struct{}{}
			discovered = append(discovered, *item)
			continue
		}

		name := entry.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".yaml") && !strings.HasSuffix(lower, ".yml") {
			continue
		}
		item, itemWarnings := discoveredAgentDefinitionFromConfigPath(entryPath, filepath.Dir(entryPath))
		warnings = append(warnings, itemWarnings...)
		if item == nil {
			continue
		}
		if _, exists := seenIDs[item.ID]; exists {
			warnings = append(warnings, fmt.Sprintf("duplicate agent definition id %q in %s", item.ID, rootDir))
			continue
		}
		seenIDs[item.ID] = struct{}{}
		discovered = append(discovered, *item)
	}

	return discovered, warnings
}

func discoveredAgentDefinitionFromConfigPath(configPath string, definitionDir string) (*discoveredAgentDefinition, []string) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, []string{fmt.Sprintf("failed to read agent definition %s: %v", configPath, err)}
	}
	def, err := agentdef.ParseYAML(raw)
	if err != nil {
		return nil, []string{fmt.Sprintf("invalid agent definition %s: %v", configPath, err)}
	}
	if def.Runtime.Type != "" && def.Runtime.Type != agentdef.RuntimeDocker && def.Runtime.Type != agentdef.RuntimeHost {
		return nil, nil
	}

	id := strings.TrimSpace(def.Agent.ID)
	if id == "" {
		id = slugifyForDockerName(def.Agent.Name)
	}
	if id == "" {
		id = slugifyForDockerName(strings.TrimSuffix(filepath.Base(configPath), filepath.Ext(configPath)))
	}
	if id == "" {
		return nil, []string{fmt.Sprintf("agent definition %s is missing agent.id and agent.name", configPath)}
	}
	def.Agent.ID = id

	name := strings.TrimSpace(def.Agent.Name)
	if name == "" {
		name = id
	}
	def.Agent.Name = name

	if strings.TrimSpace(def.Local.DefinitionDir) == "" {
		def.Local.DefinitionDir = strings.TrimSpace(definitionDir)
	}
	if err := applyResolvedAgentDefinitionSystemPrompt(def); err != nil {
		return nil, []string{fmt.Sprintf("invalid agent definition %s: %v", configPath, err)}
	}

	projectID := ""
	if projectRef := projectIDFromDefinition(def); projectRef != nil {
		projectID = strings.TrimSpace(*projectRef)
	}

	return &discoveredAgentDefinition{
		ID:            id,
		Name:          name,
		ConfigPath:    configPath,
		DefinitionDir: strings.TrimSpace(definitionDir),
		Definition:    def,
		ProjectID:     projectID,
	}, nil
}

type discoveredAgentDefinitionLocation struct {
	Item        discoveredAgentDefinition
	CatalogRoot string
}

func discoveredAgentDefinitionByIDInDirectory(id string, rootDir string) (*discoveredAgentDefinition, error) {
	id = strings.TrimSpace(id)
	rootDir = strings.TrimSpace(rootDir)
	if id == "" {
		return nil, fmt.Errorf("agent ID is required")
	}
	if rootDir == "" {
		return nil, fmt.Errorf("agent definitions directory is empty")
	}

	discovered, _ := discoverAgentDefinitionsInDirectory(rootDir)
	for _, item := range discovered {
		if item.ID != id || item.Definition == nil {
			continue
		}
		return &item, nil
	}
	return nil, fmt.Errorf("discovered agent definition %q not found in %s", id, rootDir)
}

func discoveredDefinitionByIDInDirectory(id string, rootDir string) (*agentdef.Definition, string, error) {
	item, err := discoveredAgentDefinitionByIDInDirectory(id, rootDir)
	if err != nil {
		return nil, "", err
	}
	def := *item.Definition
	if strings.TrimSpace(def.Local.DefinitionDir) == "" {
		def.Local.DefinitionDir = strings.TrimSpace(item.DefinitionDir)
	}
	return &def, strings.TrimSpace(item.ProjectID), nil
}

// deleteDiscoveredAgentDefinitionAtCatalog removes a YAML file or its parent folder
// when definitions use the usual one-folder-per-agent layout.
func deleteDiscoveredAgentDefinitionAtCatalog(item discoveredAgentDefinition, catalogRoot string) error {
	configPath := filepath.Clean(strings.TrimSpace(item.ConfigPath))
	definitionDir := filepath.Clean(strings.TrimSpace(item.DefinitionDir))
	catalogRoot = filepath.Clean(strings.TrimSpace(catalogRoot))
	if configPath == "" || catalogRoot == "" {
		return fmt.Errorf("invalid discovered agent definition paths")
	}
	if !pathWithinDirectory(configPath, catalogRoot) {
		return fmt.Errorf("agent definition path is outside allowed directory")
	}

	if definitionDir != "" && definitionDir != catalogRoot && pathWithinDirectory(definitionDir, catalogRoot) {
		if err := os.RemoveAll(definitionDir); err != nil {
			return fmt.Errorf("failed to remove agent definition directory %s: %w", definitionDir, err)
		}
		return nil
	}

	if err := os.Remove(configPath); err != nil {
		return fmt.Errorf("failed to remove agent definition file %s: %w", configPath, err)
	}
	return nil
}

func (s *Server) findDiscoveredAgentDefinitionLocation(id string, projectID string) (*discoveredAgentDefinitionLocation, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("agent ID is required")
	}

	appSettings, settingsErr := s.store.GetSettings()
	if settingsErr != nil {
		appSettings = map[string]string{}
	}

	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		project, err := s.store.GetProject(projectID)
		if err != nil {
			return nil, err
		}
		dir := s.resolveScopedProjectAgentDefinitionsDirectory(project)
		if item, err := discoveredAgentDefinitionByIDInDirectory(id, dir); err == nil {
			return &discoveredAgentDefinitionLocation{Item: *item, CatalogRoot: dir}, nil
		}
	}

	globalDir := s.resolveGlobalAgentDefinitionsDirectory(appSettings)
	if item, err := discoveredAgentDefinitionByIDInDirectory(id, globalDir); err == nil {
		return &discoveredAgentDefinitionLocation{Item: *item, CatalogRoot: globalDir}, nil
	}

	if projectID != "" {
		return nil, fmt.Errorf("agent definition not found: %s", id)
	}

	projects, err := s.store.ListProjects()
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		if project == nil {
			continue
		}
		dir := s.resolveScopedProjectAgentDefinitionsDirectory(project)
		if item, err := discoveredAgentDefinitionByIDInDirectory(id, dir); err == nil {
			return &discoveredAgentDefinitionLocation{Item: *item, CatalogRoot: dir}, nil
		}
	}

	return nil, fmt.Errorf("agent definition not found: %s", id)
}

func (s *Server) discoveredDefinitionForUnifiedAgent(id string, projectID string) (*agentdef.Definition, string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, "", fmt.Errorf("agent ID is required")
	}

	appSettings, settingsErr := s.store.GetSettings()
	if settingsErr != nil {
		appSettings = map[string]string{}
	}

	projectID = strings.TrimSpace(projectID)
	if projectID != "" {
		project, err := s.store.GetProject(projectID)
		if err != nil {
			return nil, "", err
		}
		dir := s.resolveScopedProjectAgentDefinitionsDirectory(project)
		if def, discoveredProjectID, err := discoveredDefinitionByIDInDirectory(id, dir); err == nil {
			if discoveredProjectID == "" {
				discoveredProjectID = projectID
			}
			return def, discoveredProjectID, nil
		}
	}

	globalDir := s.resolveGlobalAgentDefinitionsDirectory(appSettings)
	if def, discoveredProjectID, err := discoveredDefinitionByIDInDirectory(id, globalDir); err == nil {
		return def, discoveredProjectID, nil
	}

	if projectID != "" {
		return nil, "", fmt.Errorf("discovered agent definition not found: %s", id)
	}

	projects, err := s.store.ListProjects()
	if err != nil {
		return nil, "", err
	}
	for _, project := range projects {
		if project == nil {
			continue
		}
		dir := s.resolveScopedProjectAgentDefinitionsDirectory(project)
		if def, discoveredProjectID, err := discoveredDefinitionByIDInDirectory(id, dir); err == nil {
			if discoveredProjectID == "" {
				discoveredProjectID = strings.TrimSpace(project.ID)
			}
			return def, discoveredProjectID, nil
		}
	}

	return nil, "", fmt.Errorf("discovered agent definition not found: %s", id)
}

func unifiedAgentResponseFromDiscoveredDefinition(item discoveredAgentDefinition) UnifiedAgentResponse {
	entry := UnifiedAgentResponse{
		ID:         item.ID,
		Name:       item.Name,
		Runtime:    agentdef.RuntimeDocker,
		ProjectID:  item.ProjectID,
		Managed:    true,
		Status:     agentDefinitionStatusNotCreated,
		Definition: item.Definition,
	}
	return entry
}
