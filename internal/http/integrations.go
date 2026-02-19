package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/A2gent/brute/internal/agent"
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

var supportedIntegrationProviders = map[string]struct{}{
	"telegram":        {},
	"slack":           {},
	"discord":         {},
	"whatsapp":        {},
	"webhook":         {},
	"x":               {},
	"elevenlabs":      {},
	"google_calendar": {},
	"perplexity":      {},
	"brave_search":    {},
	"exa":             {},
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
	"x":               {"api_key", "api_secret", "access_token", "access_token_secret"},
	"elevenlabs":      {"api_key"},
	"google_calendar": {"client_id", "client_secret", "refresh_token"},
	"perplexity":      {"api_key"},
	"brave_search":    {"api_key"},
	"exa":             {"api_key"},
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
const telegramNextPollAtConfigKey = "next_poll_at_unix"
const telegramSyncedMessageCountMetadataKey = "telegram_synced_message_count"
const myMindProjectName = "My Mind"
const telegramMaxMessageRunes = 3900

var telegramBotTokenPattern = regexp.MustCompile(`bot[0-9]{5,}:[A-Za-z0-9_-]{20,}`)

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

type telegramGetMePayload struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		IsBot    bool   `json:"is_bot"`
	} `json:"result"`
}

type telegramWebhookInfoPayload struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      struct {
		URL                string `json:"url"`
		HasCustomCert      bool   `json:"has_custom_certificate"`
		PendingUpdateCount int    `json:"pending_update_count"`
		LastErrorDate      int64  `json:"last_error_date"`
		LastErrorMessage   string `json:"last_error_message"`
		MaxConnections     int    `json:"max_connections"`
	} `json:"result"`
}

type telegramCreateForumTopicPayload struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      struct {
		MessageThreadID int64 `json:"message_thread_id"`
	} `json:"result"`
}

type telegramBasicResponsePayload struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
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

	if integration.Provider == "telegram" {
		ok, message := s.testTelegramIntegration(r.Context(), integration)
		status := http.StatusOK
		if !ok {
			status = http.StatusBadGateway
		}
		s.jsonResponse(w, status, IntegrationTestResponse{Success: ok, Message: message})
		return
	}

	s.jsonResponse(w, http.StatusOK, IntegrationTestResponse{Success: true, Message: "Configuration is valid. Live provider connectivity checks are not yet implemented."})
}

func (s *Server) testTelegramIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	if integration == nil {
		return false, "integration is nil"
	}
	botToken := strings.TrimSpace(integration.Config["bot_token"])
	if botToken == "" {
		return false, "missing bot_token"
	}

	client := &http.Client{Timeout: 15 * time.Second}

	getMeReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		fmt.Sprintf("https://api.telegram.org/bot%s/getMe", botToken),
		nil,
	)
	if err != nil {
		return false, "failed to build Telegram getMe request: " + err.Error()
	}
	getMeResp, err := client.Do(getMeReq)
	if err != nil {
		return false, "failed to call Telegram getMe: " + sanitizeTelegramError(err)
	}
	defer getMeResp.Body.Close()

	var getMe telegramGetMePayload
	if err := json.NewDecoder(getMeResp.Body).Decode(&getMe); err != nil {
		return false, "failed to decode Telegram getMe response: " + err.Error()
	}
	if getMeResp.StatusCode != http.StatusOK || !getMe.OK {
		msg := strings.TrimSpace(getMe.Description)
		if msg == "" {
			msg = getMeResp.Status
		}
		return false, "Telegram getMe failed: " + msg
	}

	webhookReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		fmt.Sprintf("https://api.telegram.org/bot%s/getWebhookInfo", botToken),
		nil,
	)
	if err != nil {
		return false, "failed to build Telegram getWebhookInfo request: " + err.Error()
	}
	webhookResp, err := client.Do(webhookReq)
	if err != nil {
		return false, "failed to call Telegram getWebhookInfo: " + sanitizeTelegramError(err)
	}
	defer webhookResp.Body.Close()

	var webhookInfo telegramWebhookInfoPayload
	if err := json.NewDecoder(webhookResp.Body).Decode(&webhookInfo); err != nil {
		return false, "failed to decode Telegram webhook response: " + err.Error()
	}
	if webhookResp.StatusCode != http.StatusOK || !webhookInfo.OK {
		msg := strings.TrimSpace(webhookInfo.Description)
		if msg == "" {
			msg = webhookResp.Status
		}
		return false, "Telegram getWebhookInfo failed: " + msg
	}

	username := strings.TrimSpace(getMe.Result.Username)
	if username == "" {
		username = fmt.Sprintf("%d", getMe.Result.ID)
	}
	webhookURL := strings.TrimSpace(webhookInfo.Result.URL)
	if webhookURL != "" {
		return true, fmt.Sprintf(
			"Telegram reachable as @%s, but webhook is set (%s). This backend uses getUpdates polling, so clear webhook with Bot API deleteWebhook.",
			username,
			webhookURL,
		)
	}

	updates, _, err := s.fetchTelegramUpdates(ctx, botToken, 0)
	if err != nil {
		return false, fmt.Sprintf(
			"Telegram reachable as @%s, webhook disabled, but failed to inspect recent chats for outbound test: %s",
			username,
			sanitizeTelegramError(err),
		)
	}

	privateChatID := ""
	for i := len(updates) - 1; i >= 0; i-- {
		msg := primaryTelegramMessage(updates[i])
		if msg == nil || msg.Chat.ID == 0 || msg.From.IsBot {
			continue
		}
		chatType := strings.ToLower(strings.TrimSpace(msg.Chat.Type))
		if chatType != "private" {
			continue
		}
		privateChatID = strconv.FormatInt(msg.Chat.ID, 10)
		break
	}

	if privateChatID == "" {
		lastUpdateID := strings.TrimSpace(integration.Config[telegramLastUpdateIDConfigKey])
		return false, fmt.Sprintf(
			"Telegram reachable as @%s and webhook is disabled, but no private chat was found for direct-message test. Open a private chat with the bot, send /start, then click Test again. last_update_id=%s",
			username,
			lastUpdateID,
		)
	}

	testText := "✅ Telegram test message from A2gent WebApp integration check."
	if err := s.sendTelegramMessage(ctx, botToken, privateChatID, 0, testText); err != nil {
		return false, fmt.Sprintf(
			"Telegram reachable as @%s, but direct-message test failed for private chat %s: %s",
			username,
			privateChatID,
			sanitizeTelegramError(err),
		)
	}

	lastUpdateID := strings.TrimSpace(integration.Config[telegramLastUpdateIDConfigKey])
	return true, fmt.Sprintf(
		"Telegram reachable as @%s. Webhook is disabled. Polling should work. Direct-message test sent to private chat %s. last_update_id=%s pending_webhook_updates=%d",
		username,
		privateChatID,
		lastUpdateID,
		webhookInfo.Result.PendingUpdateCount,
	)
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
		if botToken == "" {
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
			logging.Warn("Telegram poll failed for integration %s: %s", integration.ID, sanitizeTelegramError(err))
			continue
		}
		if len(updates) == 0 {
			logging.Debug(
				"Telegram poll no updates: integration=%s offset=%d next_offset=%d",
				integration.ID,
				offset,
				nextOffset,
			)
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
				logging.Warn("Telegram duplex handling failed for integration %s: %s", integration.ID, sanitizeTelegramError(err))
				failureReply := telegramInboundFailureReply(err)
				if sendErr := s.sendTelegramMessage(ctx, botToken, messageChatID, message.MessageThreadID, failureReply); sendErr != nil {
					logging.Warn("Telegram failure reply send failed for integration %s: %s", integration.ID, sanitizeTelegramError(sendErr))
				}
				continue
			}

			reply = strings.TrimSpace(reply)
			if reply == "" {
				logging.Debug("Telegram reply skipped for integration %s: empty reply", integration.ID)
				continue
			}
			if err := s.sendTelegramMessage(ctx, botToken, messageChatID, message.MessageThreadID, reply); err != nil {
				logging.Warn("Telegram reply send failed for integration %s: %s", integration.ID, sanitizeTelegramError(err))
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
	if err := s.assignTelegramSessionToMyMindProject(sess); err != nil {
		logging.Warn("Failed to assign My Mind project for Telegram session %s: %v", sess.ID, err)
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
		SystemPrompt:  s.buildSystemPromptForSession(sess),
		MaxSteps:      s.config.MaxSteps,
		Temperature:   s.config.Temperature,
		ContextWindow: target.ContextWindow,
	}
	ag := agent.New(agentConfig, target.Client, s.toolManagerForSession(sess), s.sessionManager)

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

func (s *Server) assignTelegramSessionToMyMindProject(sess *session.Session) error {
	project, err := s.ensureMyMindProject()
	if err != nil {
		return err
	}
	if project == nil {
		return nil
	}
	sess.ProjectID = &project.ID
	return s.sessionManager.Save(sess)
}

func (s *Server) ensureMyMindProject() (*storage.Project, error) {
	settings, err := s.store.GetSettings()
	if err != nil {
		return nil, err
	}

	expectedFolder := ""
	if root := strings.TrimSpace(settings[mindRootFolderSettingKey]); root != "" {
		expectedFolder = root
	}

	projects, err := s.store.ListProjects()
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		if project == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(project.Name), myMindProjectName) {
			continue
		}
		currentFolder := ""
		if project.Folder != nil {
			currentFolder = *project.Folder
		}
		if currentFolder != expectedFolder {
			if expectedFolder == "" {
				project.Folder = nil
			} else {
				project.Folder = &expectedFolder
			}
			project.UpdatedAt = time.Now()
			if err := s.store.SaveProject(project); err != nil {
				return nil, err
			}
		}
		return project, nil
	}

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
		return nil, err
	}
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

func (s *Server) createTelegramForumTopic(ctx context.Context, botToken string, chatID string, name string) (int64, error) {
	payload := map[string]interface{}{
		"chat_id": chatID,
		"name":    name,
	}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("failed to encode createForumTopic payload: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf("https://api.telegram.org/bot%s/createForumTopic", botToken),
		bytes.NewReader(jsonBody),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to build createForumTopic request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("createForumTopic request failed: %w", err)
	}
	defer resp.Body.Close()

	var result telegramCreateForumTopicPayload
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("failed to decode createForumTopic response: %w", err)
	}

	if resp.StatusCode != http.StatusOK || !result.OK {
		msg := strings.TrimSpace(result.Description)
		if msg == "" {
			msg = resp.Status
		}
		return 0, fmt.Errorf("telegram createForumTopic failed: %s", msg)
	}
	if result.Result.MessageThreadID <= 0 {
		return 0, fmt.Errorf("telegram createForumTopic succeeded but returned empty message_thread_id")
	}
	return result.Result.MessageThreadID, nil
}

func (s *Server) deleteTelegramForumTopic(ctx context.Context, botToken string, chatID string, threadID int64) error {
	payload := map[string]interface{}{
		"chat_id":           chatID,
		"message_thread_id": threadID,
	}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to encode deleteForumTopic payload: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf("https://api.telegram.org/bot%s/deleteForumTopic", botToken),
		bytes.NewReader(jsonBody),
	)
	if err != nil {
		return fmt.Errorf("failed to build deleteForumTopic request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("deleteForumTopic request failed: %w", err)
	}
	defer resp.Body.Close()

	var result telegramBasicResponsePayload
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode deleteForumTopic response: %w", err)
	}

	if resp.StatusCode != http.StatusOK || !result.OK {
		msg := strings.TrimSpace(result.Description)
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("telegram deleteForumTopic failed: %s", msg)
	}

	return nil
}

func (s *Server) editTelegramForumTopicName(ctx context.Context, botToken string, chatID string, threadID int64, name string) error {
	payload := map[string]interface{}{
		"chat_id":           chatID,
		"message_thread_id": threadID,
		"name":              name,
	}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to encode editForumTopicName payload: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf("https://api.telegram.org/bot%s/editForumTopicName", botToken),
		bytes.NewReader(jsonBody),
	)
	if err != nil {
		return fmt.Errorf("failed to build editForumTopicName request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("editForumTopicName request failed: %w", err)
	}
	defer resp.Body.Close()

	var result telegramBasicResponsePayload
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode editForumTopicName response: %w", err)
	}

	if resp.StatusCode != http.StatusOK || !result.OK {
		msg := strings.TrimSpace(result.Description)
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("telegram editForumTopicName failed: %s", msg)
	}

	return nil
}

func (s *Server) deleteTelegramTopicForSession(ctx context.Context, sess *session.Session) error {
	if sess == nil || sess.Metadata == nil {
		return nil
	}
	if metadataString(sess.Metadata["integration_provider"]) != "telegram" {
		return nil
	}

	chatID := metadataString(sess.Metadata["telegram_chat_id"])
	if chatID == "" {
		return nil
	}

	threadID := telegramThreadIDFromSession(sess)
	if threadID <= 0 {
		return nil
	}

	integrationID := metadataString(sess.Metadata["integration_id"])
	if integrationID == "" {
		return fmt.Errorf("missing telegram integration_id in session metadata")
	}

	integration, err := s.store.GetIntegration(integrationID)
	if err != nil {
		return fmt.Errorf("failed to load integration %s: %w", integrationID, err)
	}
	if integration == nil || integration.Provider != "telegram" {
		return fmt.Errorf("integration %s is not a telegram integration", integrationID)
	}

	botToken := strings.TrimSpace(integration.Config["bot_token"])
	if botToken == "" {
		return fmt.Errorf("telegram integration %s missing bot_token", integrationID)
	}

	return s.deleteTelegramForumTopic(ctx, botToken, chatID, threadID)
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
	if chatID == "" {
		chatID = s.inferTelegramChatIDForIntegration(selected.ID)
	}
	if chatID == "" {
		logging.Info("Telegram outbound sync skipped for session %s: no default_chat_id and no inferred chat for integration %s", sessionID, selected.ID)
		return
	}

	threadID := int64(0)
	scope := strings.ToLower(strings.TrimSpace(selected.Config["session_scope"]))
	if scope != "chat" {
		topicName := telegramTopicNameForSession(sess, initialTask)
		createdThreadID, createErr := s.createTelegramForumTopic(ctx, botToken, chatID, topicName)
		if createErr != nil {
			logging.Warn("Telegram topic create failed for session %s: %s", sessionID, sanitizeTelegramError(createErr))
		} else {
			threadID = createdThreadID
		}
	}

	sessionURL := s.telegramSessionURL(selected, sessionID)
	announce := telegramSessionAnnouncement(sess, initialTask, threadID > 0, sessionURL)
	sendErr := s.sendTelegramMessage(ctx, botToken, chatID, threadID, announce)
	if sendErr != nil && threadID > 0 {
		logging.Warn("Telegram topic send failed for session %s (thread=%d): %s", sessionID, threadID, sanitizeTelegramError(sendErr))
		sendErr = s.sendTelegramMessage(ctx, botToken, chatID, 0, announce)
		threadID = 0
	}
	if sendErr != nil {
		logging.Warn("Telegram outbound sync failed for session %s: %s", sessionID, sanitizeTelegramError(sendErr))
		return
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

func (s *Server) inferTelegramChatIDForIntegration(integrationID string) string {
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
		if chatID == "" || sess.UpdatedAt.After(latest) {
			chatID = candidate
			latest = sess.UpdatedAt
		}
	}
	return chatID
}

func telegramTopicNameForSession(sess *session.Session, initialTask string) string {
	if sess == nil {
		return "Session"
	}

	base := strings.TrimSpace(sess.Title)
	if base == "" {
		base = strings.TrimSpace(initialTask)
	}
	if base == "" {
		id := strings.TrimSpace(sess.ID)
		if len(id) >= 8 {
			base = "Session " + id[:8]
		} else if id != "" {
			base = "Session " + id
		} else {
			base = "Session"
		}
	}

	base = strings.Join(strings.Fields(base), " ")
	if base == "" {
		base = "Session"
	}
	runes := []rune(base)
	if len(runes) > 120 {
		base = strings.TrimSpace(string(runes[:120]))
	}
	if base == "" {
		base = "Session"
	}
	return base
}

func telegramSessionAnnouncement(sess *session.Session, initialTask string, inTopic bool, sessionURL string) string {
	title := ""
	if sess != nil {
		title = strings.TrimSpace(sess.Title)
	}
	if title == "" {
		title = strings.TrimSpace(initialTask)
	}
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		title = "New session"
	}
	if runes := []rune(title); len(runes) > 180 {
		title = strings.TrimSpace(string(runes[:180])) + "..."
	}

	lines := []string{fmt.Sprintf("New Web App session: %s", title)}

	sessionID := ""
	if sess != nil {
		sessionID = strings.TrimSpace(sess.ID)
	}
	if link := strings.TrimSpace(sessionURL); link != "" {
		lines = append(lines, "Open: "+link)
	} else if sessionID != "" {
		lines = append(lines, "Session ID: "+sessionID)
	}

	if inTopic {
		lines = append(lines, "Reply in this topic to continue.")
	} else {
		lines = append(lines, "Reply here to continue.")
	}
	return strings.Join(lines, "\n")
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
		syncedCount = i + 1
		sess.Metadata[telegramSyncedMessageCountMetadataKey] = syncedCount
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist Telegram synced message count for session %s: %v", sess.ID, err)
		}
	}

	return nil
}

func telegramPartsForSessionMessage(msg session.Message) []string {
	parts := make([]string, 0, 3)
	content := strings.TrimSpace(msg.Content)

	switch strings.ToLower(strings.TrimSpace(msg.Role)) {
	case "user":
		if content != "" {
			parts = append(parts, "You: "+content)
		}
	case "assistant":
		if content != "" {
			parts = append(parts, "Agent: "+content)
		}
		if len(msg.ToolCalls) > 0 {
			lines := make([]string, 0, len(msg.ToolCalls)+1)
			lines = append(lines, "Agent tool calls:")
			for _, tc := range msg.ToolCalls {
				name := strings.TrimSpace(tc.Name)
				if name == "" {
					name = "tool"
				}
				input := compactTelegramJSON(tc.Input)
				if input != "" {
					lines = append(lines, fmt.Sprintf("- %s %s", name, truncateRunes(input, 500)))
				} else {
					lines = append(lines, "- "+name)
				}
			}
			parts = append(parts, strings.Join(lines, "\n"))
		}
	case "tool":
		if content != "" {
			parts = append(parts, "Tool: "+content)
		}
	}

	if len(msg.ToolResults) > 0 {
		lines := make([]string, 0, len(msg.ToolResults)+1)
		lines = append(lines, "Tool results:")
		for _, tr := range msg.ToolResults {
			status := "ok"
			if tr.IsError {
				status = "error"
			}
			body := truncateRunes(strings.TrimSpace(tr.Content), 1200)
			if body == "" {
				lines = append(lines, fmt.Sprintf("- [%s]", status))
				continue
			}
			lines = append(lines, fmt.Sprintf("- [%s] %s", status, body))
		}
		parts = append(parts, strings.Join(lines, "\n"))
	}

	return parts
}

func compactTelegramJSON(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	var out bytes.Buffer
	if err := json.Compact(&out, []byte(trimmed)); err == nil {
		return out.String()
	}
	return trimmed
}

func truncateRunes(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func splitTelegramText(text string, maxRunes int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxRunes <= 0 {
		return []string{text}
	}

	runes := []rune(text)
	if len(runes) <= maxRunes {
		return []string{text}
	}

	parts := make([]string, 0, (len(runes)/maxRunes)+1)
	for start := 0; start < len(runes); start += maxRunes {
		end := start + maxRunes
		if end > len(runes) {
			end = len(runes)
		}
		parts = append(parts, strings.TrimSpace(string(runes[start:end])))
	}
	return parts
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

func telegramInboundFailureReply(err error) string {
	base := "I couldn't process that request."
	if err == nil {
		return base + " Check integration and provider setup in WebApp."
	}

	msg := sanitizeTelegramError(err)
	if msg == "" {
		return base + " Check integration and provider setup in WebApp."
	}

	const maxErrChars = 350
	runes := []rune(msg)
	if len(runes) > maxErrChars {
		msg = string(runes[:maxErrChars]) + "..."
	}
	return fmt.Sprintf("%s %s", base, msg)
}

func sanitizeTelegramError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return ""
	}
	return telegramBotTokenPattern.ReplaceAllString(text, "bot<redacted>")
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
	if integration.Provider == "telegram" {
		if integration.Config == nil {
			integration.Config = map[string]string{}
		}
		// Telegram integration now always operates in all-groups mode.
		integration.Config["allow_all_group_chats"] = "true"
		delete(integration.Config, "chat_id")
		delete(integration.Config, "project_scope")
		delete(integration.Config, "group_project_map")
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
	if integration.Provider == "x" && integration.Mode == "duplex" {
		return fmt.Errorf("x currently supports notify_only mode")
	}

	requiredFields := requiredConfigFields[integration.Provider]
	for _, field := range requiredFields {
		if strings.TrimSpace(integration.Config[field]) == "" {
			return fmt.Errorf("missing required config field: %s", field)
		}
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
	case "x":
		return "X"
	case "google_calendar":
		return "Google Calendar"
	case "elevenlabs":
		return "ElevenLabs"
	case "perplexity":
		return "Perplexity"
	case "brave_search":
		return "Brave Search"
	default:
		return provider
	}
}
