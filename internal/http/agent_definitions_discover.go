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
