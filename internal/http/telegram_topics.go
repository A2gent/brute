package http

// Telegram forum topic lifecycle helpers are isolated from session orchestration.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

func (s *Server) createTelegramForumTopic(ctx context.Context, botToken string, chatID string, name string) (int64, error) {
	logging.Info("Creating Telegram forum topic: chatID=%s name=%s", chatID, name)
	payload := map[string]interface{}{
		"chat_id": chatID,
		"name":    name,
	}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		logging.Warn("Failed to encode createForumTopic payload: %v", err)
		return 0, fmt.Errorf("failed to encode createForumTopic payload: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf("https://api.telegram.org/bot%s/createForumTopic", botToken),
		bytes.NewReader(jsonBody),
	)
	if err != nil {
		logging.Warn("Failed to build createForumTopic request: %v", err)
		return 0, fmt.Errorf("failed to build createForumTopic request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		logging.Warn("createForumTopic HTTP request failed: %v", err)
		return 0, fmt.Errorf("createForumTopic request failed: %w", err)
	}
	defer resp.Body.Close()

	var result telegramCreateForumTopicPayload
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logging.Warn("Failed to decode createForumTopic response: %v", err)
		return 0, fmt.Errorf("failed to decode createForumTopic response: %w", err)
	}

	if resp.StatusCode != http.StatusOK || !result.OK {
		msg := strings.TrimSpace(result.Description)
		if msg == "" {
			msg = resp.Status
		}
		logging.Warn("Telegram createForumTopic failed: status=%d ok=%v description=%s", resp.StatusCode, result.OK, msg)
		return 0, fmt.Errorf("telegram createForumTopic failed: %s", msg)
	}
	if result.Result.MessageThreadID <= 0 {
		logging.Warn("Telegram createForumTopic succeeded but returned empty message_thread_id")
		return 0, fmt.Errorf("telegram createForumTopic succeeded but returned empty message_thread_id")
	}
	logging.Info("Successfully created Telegram forum topic: threadID=%d chatID=%s name=%s", result.Result.MessageThreadID, chatID, name)
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

func telegramTopicNameForSession(sess *session.Session, initialTask string) string {
	// Prioritize initialTask (first user prompt) for topic name
	base := strings.TrimSpace(initialTask)

	// Fallback to session title if no initialTask
	if base == "" && sess != nil {
		base = strings.TrimSpace(sess.Title)
	}

	// Final fallback to session ID
	if base == "" && sess != nil {
		id := strings.TrimSpace(sess.ID)
		if len(id) >= 8 {
			base = "Session " + id[:8]
		} else if id != "" {
			base = "Session " + id
		} else {
			base = "Session"
		}
	}

	if base == "" {
		base = "Session"
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
