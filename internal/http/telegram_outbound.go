package http

// Telegram outbound reply orchestration for text, audio, and assistant media.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools/integrationtools"
)

func telegramReplyMode(integration *storage.Integration) string {
	if integration == nil {
		return "text"
	}
	raw := strings.ToLower(strings.TrimSpace(integration.Config["reply_mode"]))
	switch raw {
	case "text", "voice", "both":
		return raw
	default:
		return "text"
	}
}

func (s *Server) sendTelegramConfiguredReply(
	ctx context.Context,
	integration *storage.Integration,
	botToken string,
	chatID string,
	threadID int64,
	reply string,
	sessionID string,
) error {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return nil
	}
	mode := telegramReplyMode(integration)

	sendText := mode == "text" || mode == "both"
	sendVoice := mode == "voice" || mode == "both"

	if sendText {
		parts := splitTelegramText(reply, telegramMaxMessageRunes)
		for _, part := range parts {
			if err := s.sendTelegramMessage(ctx, botToken, chatID, threadID, part); err != nil {
				return err
			}
		}
	}

	if sendVoice {
		audio, err := s.synthesizeTelegramReplyAudio(ctx, reply)
		if err != nil {
			logging.Warn("Telegram voice reply synthesis failed (mode=%s): %v", mode, err)
			if !sendText {
				parts := splitTelegramText(reply, telegramMaxMessageRunes)
				for _, part := range parts {
					if sendErr := s.sendTelegramMessage(ctx, botToken, chatID, threadID, part); sendErr != nil {
						return sendErr
					}
				}
			}
			return nil
		}
		if err := s.sendTelegramAudio(ctx, botToken, chatID, threadID, audio.data, "reply.wav", ""); err != nil {
			logging.Warn("Telegram voice reply sendAudio failed: %v", err)
			if !sendText {
				parts := splitTelegramText(reply, telegramMaxMessageRunes)
				for _, part := range parts {
					if sendErr := s.sendTelegramMessage(ctx, botToken, chatID, threadID, part); sendErr != nil {
						return sendErr
					}
				}
			}
			return nil
		}
		if sessionID != "" {
			s.attachTelegramReplyAudioMetadata(sessionID, audio.clipID, audio.contentType)
		}
	}

	if sessionID != "" {
		if err := s.sendLatestAssistantImagesToTelegram(ctx, sessionID, botToken, chatID, threadID); err != nil {
			logging.Warn("Telegram assistant image reply send failed for session %s: %v", sessionID, err)
		}
	}

	return nil
}

func (s *Server) sendLatestAssistantImagesToTelegram(
	ctx context.Context,
	sessionID string,
	botToken string,
	chatID string,
	threadID int64,
) error {
	sess, err := s.sessionManager.Get(strings.TrimSpace(sessionID))
	if err != nil || sess == nil || len(sess.Messages) == 0 {
		return nil
	}
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if sess.Messages[i].Role != "assistant" {
			continue
		}
		if len(sess.Messages[i].Images) == 0 {
			return nil
		}
		return s.sendTelegramImagesForSessionMessage(ctx, botToken, chatID, threadID, sess.Messages[i])
	}
	return nil
}

func (s *Server) synthesizeTelegramReplyAudio(ctx context.Context, reply string) (*telegramReplyAudio, error) {
	tool := integrationtools.NewPiperTTSTool(strings.TrimSpace(s.config.WorkDir), s.speechClips)
	params := map[string]interface{}{
		"text":            reply,
		"output_mode":     "stream",
		"auto_play_audio": false,
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to encode piper_tts payload: %w", err)
	}
	res, err := tool.Execute(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("piper_tts execution failed: %w", err)
	}
	if res == nil || !res.Success {
		msg := "piper_tts returned unsuccessful result"
		if res != nil && strings.TrimSpace(res.Error) != "" {
			msg = res.Error
		}
		return nil, fmt.Errorf("%s", msg)
	}
	audioMeta, ok := res.Metadata["audio_clip"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("piper_tts did not return audio_clip metadata")
	}
	clipID, _ := audioMeta["clip_id"].(string)
	clipID = strings.TrimSpace(clipID)
	if clipID == "" {
		return nil, fmt.Errorf("piper_tts returned empty audio clip id")
	}
	if s.speechClips == nil {
		return nil, fmt.Errorf("speech clip cache is unavailable")
	}
	contentType, data, found := s.speechClips.Load(clipID)
	if !found {
		return nil, fmt.Errorf("generated audio clip %s not found in cache", clipID)
	}
	return &telegramReplyAudio{
		clipID:      clipID,
		contentType: contentType,
		data:        data,
	}, nil
}

func (s *Server) attachTelegramReplyAudioMetadata(sessionID, clipID, contentType string) {
	sessionID = strings.TrimSpace(sessionID)
	clipID = strings.TrimSpace(clipID)
	if sessionID == "" || clipID == "" {
		return
	}
	sess, err := s.sessionManager.Get(sessionID)
	if err != nil || sess == nil || len(sess.Messages) == 0 {
		return
	}
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if sess.Messages[i].Role != "assistant" {
			continue
		}
		if sess.Messages[i].Metadata == nil {
			sess.Messages[i].Metadata = map[string]interface{}{}
		}
		sess.Messages[i].Metadata["audio_clip"] = map[string]interface{}{
			"clip_id":      clipID,
			"content_type": strings.TrimSpace(contentType),
			"source":       "telegram_reply",
		}
		_ = s.sessionManager.Save(sess)
		return
	}
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
