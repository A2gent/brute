package http

// Diagnoses the groups a bot has seen so the user can see why topics or messages do not work.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type telegramGroupCheck struct {
	ChatID         string `json:"chat_id"`
	Title          string `json:"title"`
	IsForum        bool   `json:"is_forum"`
	BotStatus      string `json:"bot_status"` // creator, administrator, member, ...
	CanManageTopic bool   `json:"can_manage_topics"`
	Error          string `json:"error,omitempty"`
}

type telegramGroupCheckResponse struct {
	// ReadsAllMessages is false while the bot's privacy mode is on: in groups it then only sees commands and mentions
	// unless it is an administrator.
	ReadsAllMessages bool                 `json:"reads_all_messages"`
	Groups           []telegramGroupCheck `json:"groups"`
}

// telegramAPIGet calls a Telegram Bot API method with query parameters and decodes the "result".
func telegramAPIGet(ctx context.Context, botToken, method string, params url.Values, out interface{}) error {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/%s?%s", botToken, method, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("%s", sanitizeTelegramError(err))
	}
	defer resp.Body.Close()
	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("could not read Telegram's answer: %w", err)
	}
	if !envelope.OK {
		return fmt.Errorf("%s", strings.TrimSpace(envelope.Description))
	}
	return json.Unmarshal(envelope.Result, out)
}

// handleTelegramCheckGroups checks every group the bot has already received messages from.
func (s *Server) handleTelegramCheckGroups(w http.ResponseWriter, r *http.Request) {
	var req TelegramChatDiscoveryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	botToken := s.resolveTelegramDiscoveryToken(req)
	if botToken == "" || botToken == "***" {
		s.errorResponse(w, http.StatusBadRequest, "bot_token is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var me struct {
		ID                     int64 `json:"id"`
		CanReadAllGroupMessage bool  `json:"can_read_all_group_messages"`
	}
	if err := telegramAPIGet(ctx, botToken, "getMe", url.Values{}, &me); err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Telegram API request failed: "+err.Error())
		return
	}

	resp := telegramGroupCheckResponse{ReadsAllMessages: me.CanReadAllGroupMessage, Groups: []telegramGroupCheck{}}
	if id := strings.TrimSpace(req.IntegrationID); id != "" {
		if integration, err := s.store.GetIntegration(id); err == nil && integration != nil && integration.Provider == "telegram" {
			for _, chat := range telegramSeenChats(integration) {
				if chat.Type != "group" && chat.Type != "supergroup" {
					continue
				}
				resp.Groups = append(resp.Groups, checkTelegramGroup(ctx, botToken, me.ID, chat))
			}
		}
	}
	s.jsonResponse(w, http.StatusOK, resp)
}

func checkTelegramGroup(ctx context.Context, botToken string, botID int64, chat TelegramChatCandidate) telegramGroupCheck {
	check := telegramGroupCheck{ChatID: chat.ChatID, Title: chat.Title}
	var info struct {
		Title   string `json:"title"`
		IsForum bool   `json:"is_forum"`
	}
	if err := telegramAPIGet(ctx, botToken, "getChat", url.Values{"chat_id": {chat.ChatID}}, &info); err != nil {
		check.Error = err.Error()
		return check
	}
	if info.Title != "" {
		check.Title = info.Title
	}
	check.IsForum = info.IsForum

	var member struct {
		Status         string `json:"status"`
		CanManageTopic bool   `json:"can_manage_topics"`
	}
	params := url.Values{"chat_id": {chat.ChatID}, "user_id": {fmt.Sprintf("%d", botID)}}
	if err := telegramAPIGet(ctx, botToken, "getChatMember", params, &member); err != nil {
		check.Error = err.Error()
		return check
	}
	check.BotStatus = member.Status
	// The group owner has every right; Telegram only reports the flag for administrators.
	check.CanManageTopic = member.Status == "creator" || member.CanManageTopic
	return check
}
