// toolmanager.go keeps tool-manager-specific helpers together after splitting the oversized server.go.
package http

import (
	"context"
	"encoding/json"
	"github.com/A2gent/brute/internal/contextcompress"
	"github.com/A2gent/brute/internal/filesearch"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools"
	"github.com/A2gent/brute/internal/tools/integrationtools"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Server) closeBrowserPageForSession(sessionID string) {
	if s == nil || s.toolManager == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.browserSessionToolsMu.Lock()
	toolsForSession := s.browserSessionTools[sessionID]
	delete(s.browserSessionTools, sessionID)
	s.browserSessionToolsMu.Unlock()
	if registered, ok := s.toolManager.Get("browser_chrome"); ok {
		if chrome, ok := registered.(*integrationtools.BrowserChromeTool); ok {
			if toolsForSession == nil {
				toolsForSession = make(map[*integrationtools.BrowserChromeTool]struct{})
			}
			toolsForSession[chrome] = struct{}{}
		}
	}
	for chrome := range toolsForSession {
		if err := chrome.CloseSessionPage(ctx, sessionID); err != nil {
			logging.Warn("Failed to close Chrome page for session %s: %v", sessionID, err)
		}
	}
}

func (s *Server) closeBrowserPageIfTerminal(sess *session.Session) {
	if sess == nil || (sess.Status != session.StatusCompleted && sess.Status != session.StatusFailed) {
		return
	}
	s.closeBrowserPageForSession(sess.ID)
}

func (s *Server) registerBrowserSessionTool(sessionID string, manager *tools.Manager) {
	if s == nil || sessionID == "" || manager == nil {
		return
	}
	registered, ok := manager.Get("browser_chrome")
	if !ok {
		return
	}
	chrome, ok := registered.(*integrationtools.BrowserChromeTool)
	if !ok {
		return
	}
	s.browserSessionToolsMu.Lock()
	if s.browserSessionTools == nil {
		s.browserSessionTools = make(map[string]map[*integrationtools.BrowserChromeTool]struct{})
	}
	if s.browserSessionTools[sessionID] == nil {
		s.browserSessionTools[sessionID] = make(map[*integrationtools.BrowserChromeTool]struct{})
	}
	s.browserSessionTools[sessionID][chrome] = struct{}{}
	s.browserSessionToolsMu.Unlock()
}

func (s *Server) resolveSessionWorkDir(sess *session.Session) string {
	defaultDir := strings.TrimSpace(s.config.WorkDir)
	if defaultDir == "" {
		defaultDir = "."
	}

	if sess == nil || sess.ProjectID == nil {
		return defaultDir
	}

	projectID := strings.TrimSpace(*sess.ProjectID)
	if projectID == "" {
		return defaultDir
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		logging.Warn("Failed to load project for session workdir: session=%s project=%s error=%v", sess.ID, projectID, err)
		return defaultDir
	}

	if project.Folder != nil {
		candidate := strings.TrimSpace(*project.Folder)
		if candidate != "" {
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(defaultDir, candidate)
			}
			candidate = filepath.Clean(candidate)

			info, statErr := os.Stat(candidate)
			if statErr != nil || !info.IsDir() {
				logging.Warn("Skipping invalid project folder for session workdir: session=%s folder=%s", sess.ID, candidate)
				return defaultDir
			}
			return candidate
		}
	}

	return defaultDir
}

func (s *Server) ToolManagerForSession(sess *session.Session) *tools.Manager {
	return s.toolManagerForSession(sess)
}

func (s *Server) toolManagerForSession(sess *session.Session) *tools.Manager {
	// Credentials can be added, rotated or removed after server startup.
	s.registerClassifyTool(s.toolManager)
	s.registerRelevanceGateTool(s.toolManager)
	s.registerWebRelevanceTools(s.toolManager)
	s.registerBrowserActTool(s.toolManager)
	workDir := s.resolveSessionWorkDir(sess)
	settings, err := s.store.GetSettings()
	if err != nil {
		settings = map[string]string{}
	}
	disabledTools := resolveDisabledToolNames(settings)

	isSubAgentSession := false
	var subAgentEnabledTools []string
	if sess != nil && sess.Metadata != nil {
		if saID, ok := sess.Metadata["sub_agent_id"].(string); ok && saID != "" {
			isSubAgentSession = true
			if sa, saErr := s.store.GetSubAgent(saID); saErr == nil && len(sa.EnabledTools) > 0 {
				subAgentEnabledTools = sa.EnabledTools
			}
		}
	}

	if isSubAgentSession {
		disabledTools = map[string]struct{}{}
	}

	// A Docker child has a parent on another Brute instance, so ParentID alone
	// cannot enforce the one-hop delegation boundary. Use the forwarded marker.
	if isDelegatedSession(sess) {
		for _, name := range delegationToolNames {
			disabledTools[name] = struct{}{}
		}
	}

	defaultDir := strings.TrimSpace(s.config.WorkDir)
	if defaultDir == "" {
		defaultDir = "."
	}
	indexingEnabled := s.resolveSessionFileIndexingEnabled(sess)
	indexingDiffers := indexingEnabled != filesearch.IndexingEnabled()
	if workDir == defaultDir && !indexingDiffers && len(disabledTools) == 0 && len(subAgentEnabledTools) == 0 {
		if sess != nil {
			s.registerBrowserSessionTool(sess.ID, s.toolManager)
		}
		return s.toolManager
	}

	var manager *tools.Manager
	managerOpts := &tools.ManagerOptions{FileIndexingEnabled: &indexingEnabled}
	if workDir == defaultDir && !indexingDiffers {
		manager = s.toolManager.Clone()
	} else {
		manager = tools.NewManagerWithOptions(workDir, managerOpts)
		integrationtools.Register(manager, s.store, s.speechClips, s.sessionManager)
		s.registerServerBackedTools(manager)
	}

	if sess != nil {
		s.registerBrowserSessionTool(sess.ID, manager)
	}

	for toolName := range disabledTools {
		manager.Unregister(toolName)
	}

	if len(subAgentEnabledTools) > 0 {
		allowed := make(map[string]struct{}, len(subAgentEnabledTools))
		for _, name := range subAgentEnabledTools {
			allowed[strings.TrimSpace(name)] = struct{}{}
		}

		allowed["question"] = struct{}{}
		allowed["session_task_progress"] = struct{}{}
		allowed["man"] = struct{}{}

		for _, def := range manager.GetDefinitions() {
			if _, ok := allowed[def.Name]; !ok {
				manager.Unregister(def.Name)
			}
		}
	}

	return manager
}

func (s *Server) registerServerBackedTools(manager *tools.Manager) {
	if manager == nil {
		logging.Warn("registerServerBackedTools called with nil manager")
		return
	}
	logging.Debug("Registering server-backed tools...")
	s.registerClassifyTool(manager)
	s.registerRelevanceGateTool(manager)
	s.registerWebRelevanceTools(manager)
	s.registerBrowserActTool(manager)
	manager.Register(newRecurringJobsTool(s))
	manager.Register(newMCPManageTool(s))
	manager.Register(newMCPListToolsTool(s))
	manager.Register(newMCPCallTool(s))
	manager.Register(newDelegateToSubAgentTool(s))
	manager.Register(newDelegateToAgentTool(s))
	manager.Register(&listAgentsTool{server: s})
	manager.Register(newDelegateToExternalAgentTool(s))
	manager.Register(newDiscoverExternalAgentsTool(s))
	manager.Register(newChromeExtensionTool(s))
	manager.Register(newImportAgentDefinitionYAMLTool(s))
	manager.Register(newTasksTool(s))
	manager.Register(newCreateLocalDockerAgentsBulkTool(s))
	manager.Register(newCreateLocalDockerAgentsFromYAMLTool(s))
	if s.contextCompressor != nil {
		manager.Register(contextcompress.NewRetrieveTool(s.contextCompressor))
	}
	manager.RegisterQuestionTool(s.sessionManager)
	manager.RegisterSessionTaskProgressTool(s.sessionManager)
	manager.Register(tools.NewProjectSessionHistoryTool(s.store))
	manager.RegisterSQLQueryTool(s.store)
	if s.config != nil {
		openAIImageOutputDir := filepath.Join(strings.TrimSpace(s.config.DataPath), "generated", "openai")
		manager.Register(integrationtools.NewOpenAIGenerateImageTool(s.config, openAIImageOutputDir))
	}
	logging.Debug("Server-backed tools registered. Total tools: %d", len(manager.GetDefinitions()))
}

const disabledToolsSettingKey = "A2GENT_DISABLED_TOOLS"

const disableToolsByDefaultSettingKey = "A2GENT_DISABLE_TOOLS_BY_DEFAULT"

const disableToolsByDefaultAppliedSettingKey = "A2GENT_DISABLE_TOOLS_BY_DEFAULT_APPLIED"

const syncDisabledToolsFromEnvSettingKey = "A2GENT_SYNC_DISABLED_TOOLS_FROM_ENV"

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (s *Server) bootstrapDisabledToolsByDefault() {
	syncExplicitPolicy := envBool(syncDisabledToolsFromEnvSettingKey)
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(disableToolsByDefaultSettingKey)))
	if !syncExplicitPolicy && (raw == "" || raw == "0" || raw == "false" || raw == "off" || raw == "no") {
		return
	}

	settings, err := s.store.GetSettings()
	if err != nil {
		logging.Warn("Failed to load settings for disabled-tools bootstrap: %v", err)
		return
	}
	if settings == nil {
		settings = map[string]string{}
	}
	envDisabledTools := strings.TrimSpace(os.Getenv(disabledToolsSettingKey))
	if syncExplicitPolicy {
		previous := make(map[string]string, len(settings))
		for key, value := range settings {
			previous[key] = value
		}
		if envDisabledTools == "" {
			delete(settings, disabledToolsSettingKey)
		} else {
			settings[disabledToolsSettingKey] = envDisabledTools
		}
		settings[disableToolsByDefaultAppliedSettingKey] = time.Now().UTC().Format(time.RFC3339)
		if err := s.store.SaveSettings(settings); err != nil {
			logging.Warn("Failed to sync disabled-tools policy from environment: %v", err)
			return
		}
		syncSettingsToEnv(previous, settings)
		return
	}
	if strings.TrimSpace(settings[disableToolsByDefaultAppliedSettingKey]) != "" {
		if envDisabledTools != "" && s.disabledToolsSettingDisablesAllTools(settings[disabledToolsSettingKey]) {
			previous := make(map[string]string, len(settings))
			for key, value := range settings {
				previous[key] = value
			}
			// WHY: Older Docker sub-agent data dirs may already contain the old
			// bootstrap fallback that disabled every tool. When the parent supplies
			// an explicit env policy, migrate that stale value without touching
			// user-customized partial disabled-tool settings.
			settings[disabledToolsSettingKey] = envDisabledTools
			if err := s.store.SaveSettings(settings); err != nil {
				logging.Warn("Failed to repair disabled-tools bootstrap setting: %v", err)
				return
			}
			syncSettingsToEnv(previous, settings)
		}
		return
	}

	previous := make(map[string]string, len(settings))
	for key, value := range settings {
		previous[key] = value
	}

	if strings.TrimSpace(settings[disabledToolsSettingKey]) == "" {
		if envDisabledTools != "" {
			// WHY: Docker sub-agents pass their allow/deny policy via env on first
			// boot. Persist that explicit policy instead of replacing it with the
			// safe fallback that disables every tool.
			settings[disabledToolsSettingKey] = envDisabledTools
		} else {
			defs := s.toolManager.GetDefinitions()
			names := make([]string, 0, len(defs))
			for _, def := range defs {
				name := strings.TrimSpace(def.Name)
				if name != "" {
					names = append(names, name)
				}
			}
			sort.Strings(names)
			encoded, err := json.Marshal(names)
			if err != nil {
				logging.Warn("Failed to encode disabled tools bootstrap value: %v", err)
				return
			}
			settings[disabledToolsSettingKey] = string(encoded)
		}
	}

	settings[disableToolsByDefaultAppliedSettingKey] = time.Now().UTC().Format(time.RFC3339)
	if err := s.store.SaveSettings(settings); err != nil {
		logging.Warn("Failed to save disabled-tools bootstrap setting: %v", err)
		return
	}
	syncSettingsToEnv(previous, settings)
}

func (s *Server) disabledToolsSettingDisablesAllTools(raw string) bool {
	if s == nil || s.toolManager == nil || strings.TrimSpace(raw) == "" {
		return false
	}
	disabled := resolveDisabledToolNames(map[string]string{disabledToolsSettingKey: raw})
	if len(disabled) == 0 {
		return false
	}
	defs := s.toolManager.GetDefinitions()
	if len(defs) == 0 {
		return false
	}
	for _, def := range defs {
		name := strings.TrimSpace(def.Name)
		if name == "" {
			continue
		}
		if _, ok := disabled[name]; !ok {
			return false
		}
	}
	return true
}

func resolveDisabledToolNames(settings map[string]string) map[string]struct{} {
	disabled := make(map[string]struct{})
	if settings == nil {
		return disabled
	}

	raw := strings.TrimSpace(settings[disabledToolsSettingKey])
	if raw == "" {
		return disabled
	}

	entries := make([]string, 0)
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		entries = strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == '\n'
		})
	}

	for _, entry := range entries {
		name := strings.TrimSpace(entry)
		if name == "" {
			continue
		}
		disabled[name] = struct{}{}
	}
	return disabled
}

func (s *Server) handleListToolDefinitions(w http.ResponseWriter, r *http.Request) {
	defs := s.toolManager.GetDefinitions()
	resp := make([]ToolDefinitionResponse, len(defs))
	for i, d := range defs {
		resp[i] = ToolDefinitionResponse{
			Name:        d.Name,
			Description: d.Description,
		}
	}
	sort.Slice(resp, func(i, j int) bool {
		return resp[i].Name < resp[j].Name
	})
	s.jsonResponse(w, http.StatusOK, resp)
}
