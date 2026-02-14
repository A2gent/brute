package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gratheon/aagent/internal/agent"
	"github.com/gratheon/aagent/internal/config"
	"github.com/gratheon/aagent/internal/logging"
	"github.com/gratheon/aagent/internal/session"
	"github.com/gratheon/aagent/internal/storage"
)

var supportedIntegrationProviders = map[string]struct{}{
	"telegram":        {},
	"slack":           {},
	"discord":         {},
	"whatsapp":        {},
	"webhook":         {},
	"elevenlabs":      {},
	"google_calendar": {},
}

var supportedIntegrationModes = map[string]struct{}{
	"notify_only": {},
	"duplex":      {},
}

var requiredConfigFields = map[string][]string{
	"telegram":        {"bot_token"},
	"slack":           {"bot_token", "channel_id"},
	"discord":         {"bot_token", "channel_id"},
	"whatsapp":        {"access_token", "phone_number_id", "recipient"},
	"webhook":         {"url"},
	"elevenlabs":      {"api_key"},
	"google_calendar": {"client_id", "client_secret", "refresh_token"},
}

type IntegrationRequest struct {
	Provider string            `json:"provider"`
	Name     string            `json:"name"`
	Mode     string            `json:"mode"`
	Enabled  *bool             `json:"enabled,omitempty"`
	Config   map[string]string `json:"config"`
}

type IntegrationResponse struct {
	ID        string            `json:"id"`
	Provider  string            `json:"provider"`
	Name      string            `json:"name"`
	Mode      string            `json:"mode"`
	Enabled   bool              `json:"enabled"`
	Config    map[string]string `json:"config"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type IntegrationTestResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type TelegramChatDiscoveryRequest struct {
	BotToken string `json:"bot_token"`
}

type TelegramChatDiscoveryResponse struct {
	Chats   []TelegramChatCandidate `json:"chats"`
	Message string                  `json:"message"`
}

type TelegramChatCandidate struct {
	ChatID    string `json:"chat_id"`
	Type      string `json:"type"`
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

const telegramLastUpdateIDConfigKey = "last_update_id"
const telegramProjectMapConfigKey = "group_project_map"
const telegramNextPollAtConfigKey = "next_poll_at_unix"

type telegramMessageAuthor struct {
	IsBot bool `json:"is_bot"`
}

type telegramMessagePayload struct {
	MessageID       int                   `json:"message_id"`
	MessageThreadID int64                 `json:"message_thread_id"`
	Text            string                `json:"text"`
	Chat            telegramChatPayload   `json:"chat"`
	From            telegramMessageAuthor `json:"from"`
}

type telegramChatPayload struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type telegramUpdatePayload struct {
	UpdateID          int                     `json:"update_id"`
	Message           *telegramMessagePayload `json:"message"`
	EditedMessage     *telegramMessagePayload `json:"edited_message"`
	ChannelPost       *telegramMessagePayload `json:"channel_post"`
	EditedChannelPost *telegramMessagePayload `json:"edited_channel_post"`
}

type telegramGetUpdatesPayload struct {
	OK          bool                    `json:"ok"`
	Description string                  `json:"description"`
	Result      []telegramUpdatePayload `json:"result"`
}

func (s *Server) handleListIntegrations(w http.ResponseWriter, r *http.Request) {
	integrations, err := s.store.ListIntegrations()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list integrations: "+err.Error())
		return
	}

	resp := make([]IntegrationResponse, len(integrations))
	for i, integration := range integrations {
		resp[i] = integrationToResponse(integration)
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleCreateIntegration(w http.ResponseWriter, r *http.Request) {
	var req IntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	integration, err := newIntegrationFromRequest(req)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	now := time.Now()
	integration.ID = uuid.New().String()
	integration.CreatedAt = now
	integration.UpdatedAt = now

	if err := s.store.SaveIntegration(integration); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save integration: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusCreated, integrationToResponse(integration))
}

func (s *Server) handleGetIntegration(w http.ResponseWriter, r *http.Request) {
	integrationID := chi.URLParam(r, "integrationID")

	integration, err := s.store.GetIntegration(integrationID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Integration not found: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, integrationToResponse(integration))
}

func (s *Server) handleUpdateIntegration(w http.ResponseWriter, r *http.Request) {
	integrationID := chi.URLParam(r, "integrationID")

	existing, err := s.store.GetIntegration(integrationID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Integration not found: "+err.Error())
		return
	}

	var req IntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	next, err := newIntegrationFromRequest(req)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	next.ID = existing.ID
	next.CreatedAt = existing.CreatedAt
	next.UpdatedAt = time.Now()

	if err := s.store.SaveIntegration(next); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update integration: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, integrationToResponse(next))
}

func (s *Server) handleDeleteIntegration(w http.ResponseWriter, r *http.Request) {
	integrationID := chi.URLParam(r, "integrationID")

	if err := s.store.DeleteIntegration(integrationID); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete integration: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestIntegration(w http.ResponseWriter, r *http.Request) {
	integrationID := chi.URLParam(r, "integrationID")

	integration, err := s.store.GetIntegration(integrationID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Integration not found: "+err.Error())
		return
	}

	if err := validateIntegration(*integration); err != nil {
		s.jsonResponse(w, http.StatusBadRequest, IntegrationTestResponse{Success: false, Message: err.Error()})
		return
	}

	s.jsonResponse(w, http.StatusOK, IntegrationTestResponse{Success: true, Message: "Configuration is valid. Live provider connectivity checks are not yet implemented."})
}

func (s *Server) handleDiscoverTelegramChats(w http.ResponseWriter, r *http.Request) {
	var req TelegramChatDiscoveryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	botToken := strings.TrimSpace(req.BotToken)
	if botToken == "" {
		s.errorResponse(w, http.StatusBadRequest, "bot_token is required")
		return
	}

	apiReq, err := http.NewRequestWithContext(
		r.Context(),
		http.MethodGet,
		fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?limit=100", botToken),
		nil,
	)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to build Telegram request: "+err.Error())
		return
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(apiReq)
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Failed to call Telegram API: "+err.Error())
		return
	}
	defer resp.Body.Close()

	var payload telegramGetUpdatesPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Failed to decode Telegram response: "+err.Error())
		return
	}

	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(payload.Description)
		if msg == "" {
			msg = resp.Status
		}
		s.errorResponse(w, http.StatusBadGateway, "Telegram API request failed: "+msg)
		return
	}

	if !payload.OK {
		msg := strings.TrimSpace(payload.Description)
		if msg == "" {
			msg = "unknown Telegram API error"
		}
		s.errorResponse(w, http.StatusBadGateway, "Telegram API request failed: "+msg)
		return
	}

	candidatesByID := map[string]TelegramChatCandidate{}
	for _, update := range payload.Result {
		messages := []*telegramMessagePayload{update.Message, update.EditedMessage, update.ChannelPost, update.EditedChannelPost}
		for _, message := range messages {
			if message == nil || message.Chat.ID == 0 {
				continue
			}
			chatID := fmt.Sprintf("%d", message.Chat.ID)
			if _, exists := candidatesByID[chatID]; exists {
				continue
			}
			candidatesByID[chatID] = TelegramChatCandidate{
				ChatID:    chatID,
				Type:      message.Chat.Type,
				Title:     message.Chat.Title,
				Username:  message.Chat.Username,
				FirstName: message.Chat.FirstName,
				LastName:  message.Chat.LastName,
			}
		}
	}

	candidates := make([]TelegramChatCandidate, 0, len(candidatesByID))
	for _, candidate := range candidatesByID {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ChatID < candidates[j].ChatID
	})

	message := fmt.Sprintf("Found %d chat(s) from recent Telegram updates.", len(candidates))
	if len(candidates) == 0 {
		message = "No chat IDs found yet. Send a message to your bot in Telegram, then try again."
	}

	s.jsonResponse(w, http.StatusOK, TelegramChatDiscoveryResponse{
		Chats:   candidates,
		Message: message,
	})
}

func (s *Server) runTelegramDuplexLoop(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processTelegramDuplexIntegrations(ctx)
		}
	}
}

func (s *Server) processTelegramDuplexIntegrations(ctx context.Context) {
	integrations, err := s.store.ListIntegrations()
	if err != nil {
		logging.Warn("Telegram duplex poll skipped: failed to list integrations: %v", err)
		return
	}

	for _, integration := range integrations {
		if integration == nil || !integration.Enabled || integration.Provider != "telegram" || integration.Mode != "duplex" {
			continue
		}

		botToken := strings.TrimSpace(integration.Config["bot_token"])
		configuredChatID := strings.TrimSpace(integration.Config["chat_id"])
		allowAllGroups := telegramAllowAllGroupChats(integration)
		if botToken == "" || (!allowAllGroups && configuredChatID == "") {
			continue
		}

		offset := 0
		if raw := strings.TrimSpace(integration.Config[telegramLastUpdateIDConfigKey]); raw != "" {
			if parsed, parseErr := strconv.Atoi(raw); parseErr == nil && parsed > 0 {
				offset = parsed
			}
		}
		if raw := strings.TrimSpace(integration.Config[telegramNextPollAtConfigKey]); raw != "" {
			if nextPollAt, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil && nextPollAt > time.Now().Unix() {
				continue
			}
		}

		updates, nextOffset, err := s.fetchTelegramUpdates(ctx, botToken, offset)
		if err != nil {
			if retryAfter := telegramRetryAfterSeconds(err); retryAfter > 0 {
				if integration.Config == nil {
					integration.Config = map[string]string{}
				}
				nextPollAt := time.Now().Add(time.Duration(retryAfter) * time.Second).Unix()
				integration.Config[telegramNextPollAtConfigKey] = strconv.FormatInt(nextPollAt, 10)
				integration.UpdatedAt = time.Now()
				if saveErr := s.store.SaveIntegration(integration); saveErr != nil {
					logging.Warn("Failed to persist Telegram retry-after for integration %s: %v", integration.ID, saveErr)
				}
			}
			logging.Warn("Telegram poll failed for integration %s: %v", integration.ID, err)
			continue
		}
		if len(updates) > 0 {
			logging.Info(
				"Telegram poll received updates: integration=%s count=%d offset=%d next_offset=%d",
				integration.ID,
				len(updates),
				offset,
				nextOffset,
			)
		}

		if nextOffset > offset {
			if integration.Config == nil {
				integration.Config = map[string]string{}
			}
			integration.Config[telegramLastUpdateIDConfigKey] = strconv.Itoa(nextOffset)
			delete(integration.Config, telegramNextPollAtConfigKey)
			integration.UpdatedAt = time.Now()
			if err := s.store.SaveIntegration(integration); err != nil {
				logging.Warn("Failed to persist Telegram offset for integration %s: %v", integration.ID, err)
			}
		}

		for _, update := range updates {
			message := primaryTelegramMessage(update)
			if message == nil {
				logging.Debug("Telegram update skipped for integration %s: no message payload", integration.ID)
				continue
			}
			if message.From.IsBot {
				logging.Debug("Telegram update skipped for integration %s: from bot", integration.ID)
				continue
			}
			text := strings.TrimSpace(message.Text)
			if text == "" {
				logging.Debug(
					"Telegram update skipped for integration %s: empty text (chat=%d type=%s thread=%d update=%d)",
					integration.ID,
					message.Chat.ID,
					message.Chat.Type,
					message.MessageThreadID,
					update.UpdateID,
				)
				continue
			}

			messageChatID := strconv.FormatInt(message.Chat.ID, 10)
			if allowAllGroups {
				chatType := strings.ToLower(strings.TrimSpace(message.Chat.Type))
				if chatType != "group" && chatType != "supergroup" {
					logging.Debug(
						"Telegram update skipped for integration %s: chat type filter (chat=%s type=%s update=%d)",
						integration.ID,
						messageChatID,
						chatType,
						update.UpdateID,
					)
					continue
				}
			} else if messageChatID != configuredChatID {
				logging.Debug(
					"Telegram update skipped for integration %s: chat_id mismatch (got=%s want=%s update=%d)",
					integration.ID,
					messageChatID,
					configuredChatID,
					update.UpdateID,
				)
				continue
			}
			logging.Info(
				"Telegram inbound accepted: integration=%s chat=%s type=%s thread=%d update=%d text_len=%d",
				integration.ID,
				messageChatID,
				message.Chat.Type,
				message.MessageThreadID,
				update.UpdateID,
				len([]rune(text)),
			)

			reply, err := s.handleTelegramInboundMessage(
				ctx,
				integration,
				message.Chat,
				message.MessageThreadID,
				text,
			)
			if err != nil {
				logging.Warn("Telegram duplex handling failed for integration %s: %v", integration.ID, err)
				continue
			}

			reply = strings.TrimSpace(reply)
			if reply == "" {
				logging.Debug("Telegram reply skipped for integration %s: empty reply", integration.ID)
				continue
			}
			if err := s.sendTelegramMessage(ctx, botToken, messageChatID, message.MessageThreadID, reply); err != nil {
				logging.Warn("Telegram reply send failed for integration %s: %v", integration.ID, err)
				continue
			}
			logging.Info(
				"Telegram reply sent: integration=%s chat=%s thread=%d reply_len=%d",
				integration.ID,
				messageChatID,
				message.MessageThreadID,
				len([]rune(reply)),
			)
		}
	}
}

func primaryTelegramMessage(update telegramUpdatePayload) *telegramMessagePayload {
	if update.Message != nil {
		return update.Message
	}
	if update.EditedMessage != nil {
		return update.EditedMessage
	}
	if update.ChannelPost != nil {
		return update.ChannelPost
	}
	if update.EditedChannelPost != nil {
		return update.EditedChannelPost
	}
	return nil
}

func (s *Server) fetchTelegramUpdates(ctx context.Context, botToken string, offset int) ([]telegramUpdatePayload, int, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?limit=100&timeout=10", botToken)
	if offset > 0 {
		url += "&offset=" + strconv.Itoa(offset)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, offset, fmt.Errorf("failed to build request: %w", err)
	}

	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, offset, fmt.Errorf("telegram request failed: %w", err)
	}
	defer resp.Body.Close()

	var payload telegramGetUpdatesPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, offset, fmt.Errorf("failed to decode Telegram updates: %w", err)
	}

	if resp.StatusCode != http.StatusOK || !payload.OK {
		msg := strings.TrimSpace(payload.Description)
		if msg == "" {
			msg = resp.Status
		}
		return nil, offset, fmt.Errorf("telegram API error: %s", msg)
	}

	nextOffset := offset
	for _, update := range payload.Result {
		if candidate := update.UpdateID + 1; candidate > nextOffset {
			nextOffset = candidate
		}
	}
	return payload.Result, nextOffset, nil
}

func telegramRetryAfterSeconds(err error) int {
	if err == nil {
		return 0
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	const needle = "retry after "
	idx := strings.Index(text, needle)
	if idx < 0 {
		return 0
	}
	raw := strings.TrimSpace(text[idx+len(needle):])
	for i, ch := range raw {
		if ch < '0' || ch > '9' {
			raw = raw[:i]
			break
		}
	}
	if raw == "" {
		return 0
	}
	n, parseErr := strconv.Atoi(raw)
	if parseErr != nil || n <= 0 {
		return 0
	}
	return n
}

func (s *Server) handleTelegramInboundMessage(
	ctx context.Context,
	integration *storage.Integration,
	chat telegramChatPayload,
	threadID int64,
	userMessage string,
) (string, error) {
	if handled, reply := handleTelegramSlashCommand(userMessage); handled {
		return reply, nil
	}

	chatID := strconv.FormatInt(chat.ID, 10)
	scopeKey := telegramSessionScopeKey(integration, chatID, threadID)
	sess, err := s.findTelegramSession(integration.ID, chatID, scopeKey, threadID)
	if err != nil {
		return "", err
	}

	if sess == nil {
		sess, err = s.sessionManager.Create("build")
		if err != nil {
			return "", fmt.Errorf("failed to create Telegram session: %w", err)
		}
		if sess.Metadata == nil {
			sess.Metadata = map[string]interface{}{}
		}
		providerType := config.NormalizeProviderRef(strings.TrimSpace(s.config.ActiveProvider))
		autoCfg := s.config.Providers[string(config.ProviderAutoRouter)]
		if s.autoRouterConfigured(autoCfg) {
			providerType = string(config.ProviderAutoRouter)
		}
		model := s.resolveModelForProvider(config.ProviderType(providerType))
		sess.Metadata["provider"] = providerType
		sess.Metadata["model"] = model
		sess.Metadata["integration_provider"] = "telegram"
		sess.Metadata["integration_id"] = integration.ID
		sess.Metadata["telegram_chat_id"] = chatID
		sess.Metadata["telegram_scope_key"] = scopeKey
		if threadID > 0 {
			sess.Metadata["telegram_thread_id"] = strconv.FormatInt(threadID, 10)
		}
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist new Telegram session metadata: %v", err)
		}
	}
	if err := s.assignTelegramProject(sess, integration, chat); err != nil {
		logging.Warn("Failed to assign Telegram project for session %s: %v", sess.ID, err)
	}

	sess.AddUserMessage(userMessage)

	providerType := s.resolveSessionProviderType(sess)
	model := s.resolveSessionModel(sess, providerType)
	target, err := s.resolveExecutionTarget(ctx, providerType, model, userMessage)
	if err != nil {
		sess.AddAssistantMessage(fmt.Sprintf("Unable to start request: %s", err.Error()), nil)
		sess.SetStatus(session.StatusFailed)
		_ = s.sessionManager.Save(sess)
		return "", fmt.Errorf("provider configuration error: %w", err)
	}

	agentConfig := agent.Config{
		Name:          sess.AgentID,
		Model:         target.Model,
		MaxSteps:      s.config.MaxSteps,
		Temperature:   s.config.Temperature,
		ContextWindow: target.ContextWindow,
	}
	ag := agent.New(agentConfig, target.Client, s.toolManager, s.sessionManager)

	response, _, err := ag.Run(ctx, sess, userMessage)
	if err != nil {
		sess.AddAssistantMessage(fmt.Sprintf("Request failed: %s", err.Error()), nil)
		sess.SetStatus(session.StatusFailed)
		_ = s.sessionManager.Save(sess)
		return "", fmt.Errorf("agent run failed: %w", err)
	}

	return response, nil
}

func handleTelegramSlashCommand(text string) (bool, string) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return false, ""
	}

	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		return true, "Send a normal text message to start an agent task."
	}

	cmd := strings.ToLower(parts[0])
	if at := strings.IndexByte(cmd, '@'); at >= 0 {
		cmd = cmd[:at]
	}

	switch cmd {
	case "/start", "/help":
		return true, "Telegram connected. Send a normal text message in this chat/topic to run an agent task."
	default:
		return true, "Command received. Send a normal text message to run an agent task."
	}
}

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
		return sess, nil
	}
	return nil, nil
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

func telegramAllowAllGroupChats(integration *storage.Integration) bool {
	raw := strings.ToLower(strings.TrimSpace(integration.Config["allow_all_group_chats"]))
	return raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func (s *Server) assignTelegramProject(sess *session.Session, integration *storage.Integration, chat telegramChatPayload) error {
	scope := strings.ToLower(strings.TrimSpace(integration.Config["project_scope"]))
	if scope != "group" {
		return nil
	}

	chatType := strings.ToLower(strings.TrimSpace(chat.Type))
	if chatType != "group" && chatType != "supergroup" {
		return nil
	}

	chatID := strconv.FormatInt(chat.ID, 10)
	projectMap := map[string]string{}
	if raw := strings.TrimSpace(integration.Config[telegramProjectMapConfigKey]); raw != "" {
		_ = json.Unmarshal([]byte(raw), &projectMap)
	}

	projectID := strings.TrimSpace(projectMap[chatID])
	if projectID != "" {
		if _, err := s.store.GetProject(projectID); err == nil {
			sess.ProjectID = &projectID
			return s.sessionManager.Save(sess)
		}
	}

	name := strings.TrimSpace(chat.Title)
	if name == "" {
		name = "Telegram Group " + chatID
	}
	project := &storage.Project{
		ID:        uuid.New().String(),
		Name:      name,
		Folders:   []string{},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.store.SaveProject(project); err != nil {
		return err
	}
	projectMap[chatID] = project.ID
	encoded, err := json.Marshal(projectMap)
	if err == nil {
		if integration.Config == nil {
			integration.Config = map[string]string{}
		}
		integration.Config[telegramProjectMapConfigKey] = string(encoded)
		integration.UpdatedAt = time.Now()
		if saveErr := s.store.SaveIntegration(integration); saveErr != nil {
			logging.Warn("Failed to save Telegram project map for integration %s: %v", integration.ID, saveErr)
		}
	}
	sess.ProjectID = &project.ID
	return s.sessionManager.Save(sess)
}

func (s *Server) sendTelegramMessage(ctx context.Context, botToken string, chatID string, threadID int64, text string) error {
	payload := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
	}
	if threadID > 0 {
		payload["message_thread_id"] = threadID
	}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to encode sendMessage payload: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken),
		bytes.NewReader(jsonBody),
	)
	if err != nil {
		return fmt.Errorf("failed to build sendMessage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sendMessage request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode sendMessage response: %w", err)
	}

	if resp.StatusCode != http.StatusOK || !result.OK {
		msg := strings.TrimSpace(result.Description)
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("telegram sendMessage failed: %s", msg)
	}
	return nil
}

func newIntegrationFromRequest(req IntegrationRequest) (*storage.Integration, error) {
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	name := strings.TrimSpace(req.Name)
	if req.Config == nil {
		req.Config = map[string]string{}
	}

	integration := &storage.Integration{
		Provider: provider,
		Name:     name,
		Mode:     mode,
		Enabled:  true,
		Config:   trimConfig(req.Config),
	}
	if req.Enabled != nil {
		integration.Enabled = *req.Enabled
	}

	if err := validateIntegration(*integration); err != nil {
		return nil, err
	}

	if integration.Name == "" {
		integration.Name = defaultIntegrationName(integration.Provider)
	}

	return integration, nil
}

func validateIntegration(integration storage.Integration) error {
	if integration.Provider == "" {
		return fmt.Errorf("provider is required")
	}
	if _, ok := supportedIntegrationProviders[integration.Provider]; !ok {
		return fmt.Errorf("unsupported provider: %s", integration.Provider)
	}

	if integration.Mode == "" {
		return fmt.Errorf("mode is required")
	}
	if _, ok := supportedIntegrationModes[integration.Mode]; !ok {
		return fmt.Errorf("unsupported mode: %s", integration.Mode)
	}
	if integration.Provider == "webhook" && integration.Mode == "duplex" {
		return fmt.Errorf("webhook currently supports notify_only mode")
	}

	requiredFields := requiredConfigFields[integration.Provider]
	for _, field := range requiredFields {
		if strings.TrimSpace(integration.Config[field]) == "" {
			return fmt.Errorf("missing required config field: %s", field)
		}
	}
	if integration.Provider == "telegram" && !telegramAllowAllGroupChats(&integration) && strings.TrimSpace(integration.Config["chat_id"]) == "" {
		return fmt.Errorf("missing required config field: chat_id (or enable allow_all_group_chats)")
	}

	if integration.Provider == "webhook" {
		url := strings.ToLower(strings.TrimSpace(integration.Config["url"]))
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			return fmt.Errorf("webhook url must start with http:// or https://")
		}
	}

	return nil
}

func trimConfig(config map[string]string) map[string]string {
	out := make(map[string]string, len(config))
	for key, value := range config {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		out[k] = strings.TrimSpace(value)
	}
	return out
}

func integrationToResponse(integration *storage.Integration) IntegrationResponse {
	configCopy := make(map[string]string, len(integration.Config))
	for key, value := range integration.Config {
		configCopy[key] = value
	}

	return IntegrationResponse{
		ID:        integration.ID,
		Provider:  integration.Provider,
		Name:      integration.Name,
		Mode:      integration.Mode,
		Enabled:   integration.Enabled,
		Config:    configCopy,
		CreatedAt: integration.CreatedAt,
		UpdatedAt: integration.UpdatedAt,
	}
}

func defaultIntegrationName(provider string) string {
	switch provider {
	case "telegram":
		return "Telegram"
	case "slack":
		return "Slack"
	case "discord":
		return "Discord"
	case "whatsapp":
		return "WhatsApp"
	case "webhook":
		return "Webhook"
	case "google_calendar":
		return "Google Calendar"
	case "elevenlabs":
		return "ElevenLabs"
	default:
		return provider
	}
}
