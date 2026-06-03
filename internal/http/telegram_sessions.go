package http

// Telegram session/project synchronization helpers used by inbound and outbound flows.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/google/uuid"
)

const myMindProjectName = "My Mind"

func (s *Server) findTelegramSession(integrationID string, chatID string, scopeKey string, threadID int64) (*session.Session, error) {
	sessions, err := s.sessionManager.List()
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	for _, sess := range sessions {
		if sess == nil || sess.Metadata == nil {
			continue
		}
		if metadataString(sess.Metadata["integration_provider"]) != "telegram" {
			continue
		}
		if metadataString(sess.Metadata["integration_id"]) != integrationID {
			continue
		}
		if metadataString(sess.Metadata["telegram_chat_id"]) != chatID {
			continue
		}
		if scopeKey != "" {
			existingScope := metadataString(sess.Metadata["telegram_scope_key"])
			if existingScope != "" && existingScope != scopeKey {
				continue
			}
			if existingScope == "" {
				existingThread := metadataString(sess.Metadata["telegram_thread_id"])
				if threadID > 0 && existingThread != strconv.FormatInt(threadID, 10) {
					continue
				}
				if threadID == 0 && existingThread != "" {
					continue
				}
			}
		}
		fullSess, getErr := s.sessionManager.Get(sess.ID)
		if getErr != nil {
			return nil, fmt.Errorf("failed to load matched telegram session %s: %w", sess.ID, getErr)
		}
		return fullSess, nil
	}
	return nil, nil
}

func getConfigKeys(config map[string]string) []string {
	keys := make([]string, 0, len(config))
	for k := range config {
		keys = append(keys, k)
	}
	return keys
}

func metadataString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

func telegramSessionScopeKey(integration *storage.Integration, chatID string, threadID int64) string {
	scope := strings.ToLower(strings.TrimSpace(integration.Config["session_scope"]))
	if scope == "chat" || threadID <= 0 {
		return chatID
	}
	return fmt.Sprintf("%s:%d", chatID, threadID)
}

func (s *Server) assignTelegramSessionToProject(sess *session.Session, chatTitle string) error {
	chatTitle = strings.TrimSpace(chatTitle)
	logging.Info("Attempting to assign project to session %s (chat title: %q)", sess.ID, chatTitle)

	// Step 1: Try to find project by chat title
	if chatTitle != "" {
		projects, err := s.store.ListProjects()
		if err != nil {
			logging.Warn("Failed to list projects for matching: %v", err)
		} else {
			for _, project := range projects {
				if project == nil {
					continue
				}
				if strings.EqualFold(strings.TrimSpace(project.Name), chatTitle) {
					logging.Info("Found matching project by chat title: id=%s name=%s", project.ID, project.Name)
					sess.ProjectID = &project.ID
					err = s.sessionManager.Save(sess)
					if err != nil {
						logging.Warn("Failed to save session with project assignment: %v", err)
						return err
					}
					logging.Info("Successfully assigned session %s to project %s (%s)", sess.ID, project.ID, project.Name)
					return nil
				}
			}
			logging.Info("No project found matching chat title %q", chatTitle)
		}
	}

	// Step 2: Fallback to Knowledge Base project
	logging.Info("Looking for Knowledge Base project as fallback")
	project, err := s.ensureKnowledgeBaseProject()
	if err != nil {
		logging.Warn("ensureKnowledgeBaseProject failed: %v", err)
		return err
	}
	if project == nil {
		logging.Info("No Knowledge Base project available, skipping project assignment")
		return nil
	}

	logging.Info("Assigning session %s to Knowledge Base project %s (%s)", sess.ID, project.ID, project.Name)
	sess.ProjectID = &project.ID
	err = s.sessionManager.Save(sess)
	if err != nil {
		logging.Warn("Failed to save session with project assignment: %v", err)
		return err
	}
	logging.Info("Successfully assigned session %s to project %s", sess.ID, project.ID)
	return nil
}

func (s *Server) ensureKnowledgeBaseProject() (*storage.Project, error) {
	const knowledgeBaseProjectName = "Knowledge Base"

	projects, err := s.store.ListProjects()
	if err != nil {
		logging.Warn("Failed to list projects for Knowledge Base: %v", err)
		return nil, err
	}

	// Look for existing Knowledge Base project
	for _, project := range projects {
		if project == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(project.Name), knowledgeBaseProjectName) {
			logging.Info("Found existing Knowledge Base project: id=%s name=%s", project.ID, project.Name)
			return project, nil
		}
	}

	// Create Knowledge Base project if not found
	logging.Info("Knowledge Base project not found, creating new one")
	now := time.Now()
	project := &storage.Project{
		ID:        uuid.New().String(),
		Name:      knowledgeBaseProjectName,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.SaveProject(project); err != nil {
		logging.Warn("Failed to create Knowledge Base project: %v", err)
		return nil, err
	}
	logging.Info("Successfully created Knowledge Base project: id=%s", project.ID)
	return project, nil
}

func (s *Server) ensureMyMindProject() (*storage.Project, error) {
	settings, err := s.store.GetSettings()
	if err != nil {
		logging.Warn("Failed to get settings for My Mind project: %v", err)
		return nil, err
	}

	expectedFolder := ""
	if root := strings.TrimSpace(settings[mindRootFolderSettingKey]); root != "" {
		expectedFolder = root
		logging.Info("My Mind expected folder: %s", expectedFolder)
	} else {
		logging.Info("My Mind folder not configured in settings")
	}

	projects, err := s.store.ListProjects()
	if err != nil {
		logging.Warn("Failed to list projects for My Mind project: %v", err)
		return nil, err
	}

	logging.Info("Looking for existing My Mind project (total projects: %d)", len(projects))
	for _, project := range projects {
		if project == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(project.Name), myMindProjectName) {
			continue
		}
		logging.Info("Found existing My Mind project: id=%s name=%s", project.ID, project.Name)
		currentFolder := ""
		if project.Folder != nil {
			currentFolder = *project.Folder
		}
		if currentFolder != expectedFolder {
			logging.Info("Updating My Mind project folder from %s to %s", currentFolder, expectedFolder)
			if expectedFolder == "" {
				project.Folder = nil
			} else {
				project.Folder = &expectedFolder
			}
			project.UpdatedAt = time.Now()
			if err := s.store.SaveProject(project); err != nil {
				logging.Warn("Failed to update My Mind project folder: %v", err)
				return nil, err
			}
		}
		return project, nil
	}

	logging.Info("My Mind project not found, creating new one")
	now := time.Now()
	project := &storage.Project{
		ID:        uuid.New().String(),
		Name:      myMindProjectName,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if expectedFolder != "" {
		project.Folder = &expectedFolder
	}
	if err := s.store.SaveProject(project); err != nil {
		logging.Warn("Failed to save new My Mind project: %v", err)
		return nil, err
	}
	logging.Info("Successfully created My Mind project: id=%s", project.ID)
	return project, nil
}

func sameStringSets(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left := append([]string(nil), a...)
	right := append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (s *Server) syncHTTPCreatedSessionToTelegram(ctx context.Context, sessionID string, initialTask string) {
	sess, err := s.sessionManager.Get(sessionID)
	if err != nil {
		logging.Warn("Telegram outbound sync skipped: failed to load session %s: %v", sessionID, err)
		return
	}

	integrations, err := s.store.ListIntegrations()
	if err != nil {
		logging.Warn("Telegram outbound sync skipped: failed to list integrations: %v", err)
		return
	}

	var selected *storage.Integration
	for _, integration := range integrations {
		if integration == nil || !integration.Enabled || integration.Provider != "telegram" || integration.Mode != "duplex" {
			continue
		}
		selected = integration
		break
	}
	if selected == nil {
		return
	}

	botToken := strings.TrimSpace(selected.Config["bot_token"])
	if botToken == "" {
		logging.Warn("Telegram outbound sync skipped for session %s: integration %s missing bot_token", sessionID, selected.ID)
		return
	}

	chatID := strings.TrimSpace(selected.Config["default_chat_id"])
	scope := strings.ToLower(strings.TrimSpace(selected.Config["session_scope"]))
	if chatID == "" {
		chatID = s.inferTelegramChatIDForIntegration(selected.ID, scope)
	}
	if chatID == "" {
		logging.Info("Telegram outbound sync skipped for session %s: no default_chat_id and no inferred chat for integration %s", sessionID, selected.ID)
		return
	}

	threadID := int64(0)
	if scope != "chat" {
		topicName := telegramTopicNameForSession(sess, initialTask)
		createdThreadID, createErr := s.createTelegramForumTopic(ctx, botToken, chatID, topicName)
		if createErr != nil {
			logging.Warn("Telegram topic create failed for session %s: %s", sessionID, sanitizeTelegramError(createErr))
			return
		} else {
			threadID = createdThreadID
		}
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	scopeKey := telegramSessionScopeKey(selected, chatID, threadID)
	sess.Metadata["integration_provider"] = "telegram"
	sess.Metadata["integration_id"] = selected.ID
	sess.Metadata["telegram_chat_id"] = chatID
	sess.Metadata["telegram_scope_key"] = scopeKey
	if threadID > 0 {
		sess.Metadata["telegram_thread_id"] = strconv.FormatInt(threadID, 10)
		topicName := telegramTopicNameForSession(sess, initialTask)
		sess.Metadata["telegram_topic_name"] = topicName
	} else {
		delete(sess.Metadata, "telegram_thread_id")
		delete(sess.Metadata, "telegram_topic_name")
	}
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist Telegram outbound metadata for session %s: %v", sessionID, err)
	}

	if err := s.syncSessionMessagesToTelegram(ctx, sess, botToken, chatID, threadID); err != nil {
		logging.Warn("Telegram outbound message sync failed for session %s: %s", sessionID, sanitizeTelegramError(err))
	}
}

func (s *Server) inferTelegramChatIDForIntegration(integrationID string, scope string) string {
	sessions, err := s.sessionManager.List()
	if err != nil {
		return ""
	}
	latest := time.Time{}
	chatID := ""
	for _, sess := range sessions {
		if sess == nil || sess.Metadata == nil {
			continue
		}
		if metadataString(sess.Metadata["integration_provider"]) != "telegram" {
			continue
		}
		if metadataString(sess.Metadata["integration_id"]) != integrationID {
			continue
		}
		candidate := metadataString(sess.Metadata["telegram_chat_id"])
		if candidate == "" {
			continue
		}
		if scope != "chat" {
			if metadataString(sess.Metadata["telegram_thread_id"]) == "" {
				continue
			}
		}
		if chatID == "" || sess.UpdatedAt.After(latest) {
			chatID = candidate
			latest = sess.UpdatedAt
		}
	}
	return chatID
}

func (s *Server) telegramSessionURL(integration *storage.Integration, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}

	base := ""
	if integration != nil {
		base = strings.TrimSpace(integration.Config["web_app_base_url"])
	}
	if base == "" {
		base = strings.TrimSpace(os.Getenv("A2GENT_WEBAPP_BASE_URL"))
	}
	if base == "" {
		return fmt.Sprintf("http://localhost:%d/chat/%s", s.port, sessionID)
	}

	if strings.Contains(base, "{session_id}") {
		return strings.ReplaceAll(base, "{session_id}", sessionID)
	}

	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/chat") {
		return base + "/" + sessionID
	}
	return base + "/chat/" + sessionID
}

func (s *Server) queueTelegramSessionMessageSync(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	go func(id string) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		s.syncSessionMessagesToTelegramBySessionID(ctx, id)
	}(sessionID)
}

func (s *Server) syncSessionMessagesToTelegramBySessionID(ctx context.Context, sessionID string) {
	sess, err := s.sessionManager.Get(sessionID)
	if err != nil || sess == nil || sess.Metadata == nil {
		return
	}
	if metadataString(sess.Metadata["integration_provider"]) != "telegram" {
		return
	}

	integrationID := metadataString(sess.Metadata["integration_id"])
	if integrationID == "" {
		return
	}
	integration, err := s.store.GetIntegration(integrationID)
	if err != nil || integration == nil {
		return
	}
	if !integration.Enabled || integration.Provider != "telegram" || integration.Mode != "duplex" {
		return
	}

	botToken := strings.TrimSpace(integration.Config["bot_token"])
	if botToken == "" {
		return
	}

	chatID := metadataString(sess.Metadata["telegram_chat_id"])
	if chatID == "" {
		return
	}
	threadID := telegramThreadIDFromSession(sess)

	// Update topic name if title changed
	if threadID > 0 && sess.Title != "" {
		currentTopicName := metadataString(sess.Metadata["telegram_topic_name"])
		expectedTopicName := telegramTopicNameForSession(sess, "")
		if currentTopicName != expectedTopicName {
			if err := s.editTelegramForumTopicName(ctx, botToken, chatID, threadID, expectedTopicName); err != nil {
				logging.Warn("Failed to update Telegram topic name for session %s: %s", sessionID, sanitizeTelegramError(err))
			} else {
				sess.Metadata["telegram_topic_name"] = expectedTopicName
				if err := s.sessionManager.Save(sess); err != nil {
					logging.Warn("Failed to persist updated topic name for session %s: %v", sessionID, err)
				}
			}
		}
	}

	if err := s.syncSessionMessagesToTelegram(ctx, sess, botToken, chatID, threadID); err != nil {
		logging.Warn("Telegram session message sync failed for session %s: %s", sessionID, sanitizeTelegramError(err))
	}
}

func (s *Server) syncSessionMessagesToTelegram(
	ctx context.Context,
	sess *session.Session,
	botToken string,
	chatID string,
	threadID int64,
) error {
	if sess == nil {
		return nil
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}

	syncedCount := metadataInt(sess.Metadata[telegramSyncedMessageCountMetadataKey])
	if syncedCount < 0 {
		syncedCount = 0
	}
	if syncedCount > len(sess.Messages) {
		syncedCount = len(sess.Messages)
	}

	for i := syncedCount; i < len(sess.Messages); i++ {
		parts := telegramPartsForSessionMessage(sess.Messages[i])
		for _, part := range parts {
			chunks := splitTelegramText(part, telegramMaxMessageRunes)
			for _, chunk := range chunks {
				if err := s.sendTelegramMessage(ctx, botToken, chatID, threadID, chunk); err != nil {
					return err
				}
			}
		}
		if err := s.sendTelegramImagesForSessionMessage(ctx, botToken, chatID, threadID, sess.Messages[i]); err != nil {
			return err
		}
		syncedCount = i + 1
		sess.Metadata[telegramSyncedMessageCountMetadataKey] = syncedCount
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist Telegram synced message count for session %s: %v", sess.ID, err)
		}
	}

	return nil
}

func metadataInt(value interface{}) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
		return 0
	default:
		return 0
	}
}

func telegramThreadIDFromSession(sess *session.Session) int64 {
	if sess == nil || sess.Metadata == nil {
		return 0
	}
	raw := metadataString(sess.Metadata["telegram_thread_id"])
	if raw == "" {
		return 0
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}
