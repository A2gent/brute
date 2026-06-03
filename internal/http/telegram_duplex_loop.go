package http

// Telegram duplex polling loop and update transport helpers.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
)

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

			inboundPrompt, err := s.telegramPromptFromInboundMessage(ctx, botToken, integration, message)
			if err != nil {
				logging.Warn("Telegram inbound media processing failed for integration %s: %s", integration.ID, sanitizeTelegramError(err))
				failureReply := telegramInboundFailureReply(err)
				if sendErr := s.sendTelegramMessage(ctx, botToken, messageChatID, message.MessageThreadID, failureReply); sendErr != nil {
					logging.Warn("Telegram media failure reply send failed for integration %s: %s", integration.ID, sanitizeTelegramError(sendErr))
				}
				continue
			}
			if strings.TrimSpace(inboundPrompt.text) == "" && len(inboundPrompt.images) == 0 {
				logging.Debug(
					"Telegram update skipped for integration %s: no text/caption/audio/photo prompt (chat=%d type=%s thread=%d update=%d)",
					integration.ID,
					message.Chat.ID,
					message.Chat.Type,
					message.MessageThreadID,
					update.UpdateID,
				)
				continue
			}

			logging.Info(
				"Telegram inbound accepted: integration=%s chat=%s type=%s thread=%d update=%d prompt_len=%d",
				integration.ID,
				messageChatID,
				message.Chat.Type,
				message.MessageThreadID,
				update.UpdateID,
				len([]rune(inboundPrompt.text)),
			)

			result, err := s.handleTelegramInboundMessage(
				ctx,
				integration,
				message.Chat,
				message.MessageThreadID,
				inboundPrompt.text,
				inboundPrompt.images,
				inboundPrompt.metadata,
			)
			if err != nil {
				logging.Warn("Telegram duplex handling failed for integration %s: %s", integration.ID, sanitizeTelegramError(err))
				failureReply := telegramInboundFailureReply(err)
				if sendErr := s.sendTelegramMessage(ctx, botToken, messageChatID, message.MessageThreadID, failureReply); sendErr != nil {
					logging.Warn("Telegram failure reply send failed for integration %s: %s", integration.ID, sanitizeTelegramError(sendErr))
				}
				continue
			}

			reply := strings.TrimSpace(result.reply)
			if reply == "" {
				logging.Debug("Telegram reply skipped for integration %s: empty reply", integration.ID)
				continue
			}

			// If we created a new topic from general chat, send link to general chat and reply to topic
			if result.createdThread > 0 && message.MessageThreadID == 0 {
				topicLink := fmt.Sprintf("https://t.me/c/%s/%d", strings.TrimPrefix(messageChatID, "-100"), result.createdThread)
				generalChatReply := fmt.Sprintf("Moved to topic: %s", topicLink)
				logging.Info("Sending topic link to general chat: link=%s", topicLink)
				if sendErr := s.sendTelegramMessage(ctx, botToken, messageChatID, 0, generalChatReply); sendErr != nil {
					logging.Warn("Telegram topic link reply to general chat failed for integration %s: %s", integration.ID, sanitizeTelegramError(sendErr))
				} else {
					logging.Info("Successfully sent topic link to general chat")
				}

				// Send actual reply to the new topic
				logging.Info("Sending agent reply to new topic %d", result.createdThread)
				if err := s.sendTelegramConfiguredReply(ctx, integration, botToken, messageChatID, result.createdThread, reply, result.sessionID); err != nil {
					logging.Warn("Telegram reply send to new topic failed for integration %s: %s", integration.ID, sanitizeTelegramError(err))
					continue
				}
				logging.Info(
					"Telegram reply sent to new topic: integration=%s chat=%s thread=%d reply_len=%d",
					integration.ID,
					messageChatID,
					result.createdThread,
					len([]rune(reply)),
				)
			} else {
				// Normal reply to same thread
				if err := s.sendTelegramConfiguredReply(ctx, integration, botToken, messageChatID, message.MessageThreadID, reply, result.sessionID); err != nil {
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
