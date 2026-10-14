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
	"github.com/A2gent/brute/internal/tgformat"
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
		parts := splitTelegramText(reply, telegramMaxRichMessageRunes)
		for _, part := range parts {
			if err := s.sendTelegramRichMessage(ctx, botToken, chatID, threadID, part); err != nil {
				return err
			}
		}
	}

	if sendVoice {
		audio, err := s.synthesizeTelegramReplyAudio(ctx, reply)
		if err != nil {
			logging.Warn("Telegram voice reply synthesis failed (mode=%s): %v", mode, err)
			if !sendText {
				parts := splitTelegramText(reply, telegramMaxRichMessageRunes)
				for _, part := range parts {
					if sendErr := s.sendTelegramRichMessage(ctx, botToken, chatID, threadID, part); sendErr != nil {
						return sendErr
					}
				}
			}
			return nil
		}
		if err := s.sendTelegramAudio(ctx, botToken, chatID, threadID, audio.data, telegramReplyAudioFilename(audio.contentType), ""); err != nil {
			logging.Warn("Telegram voice reply sendAudio failed: %v", err)
			if !sendText {
				parts := splitTelegramText(reply, telegramMaxRichMessageRunes)
				for _, part := range parts {
					if sendErr := s.sendTelegramRichMessage(ctx, botToken, chatID, threadID, part); sendErr != nil {
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

// synthesizeTelegramReplyAudio speaks the reply with the engine chosen in the Speech settings, the same one chat
// read-aloud uses.
func (s *Server) synthesizeTelegramReplyAudio(ctx context.Context, reply string) (*telegramReplyAudio, error) {
	result, synthErr := s.synthesizeSpeech(ctx, speechCompletionRequest{Text: reply})
	if synthErr != nil {
		return nil, synthErr
	}
	clipID := result.ClipID
	if clipID == "" && s.speechClips != nil {
		// Cloud engines return raw audio; cache it so the clip can be attached to the session like local ones.
		clipID = s.speechClips.Save(result.ContentType, result.Audio)
	}
	return &telegramReplyAudio{clipID: clipID, contentType: result.ContentType, data: result.Audio}, nil
}

// telegramReplyAudioFilename picks an extension that matches the audio so Telegram handles it correctly.
func telegramReplyAudioFilename(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])) {
	case "audio/mpeg", "audio/mp3":
		return "reply.mp3"
	case "audio/ogg", "audio/opus":
		return "reply.ogg"
	case "audio/mp4", "audio/aac", "audio/x-m4a":
		return "reply.m4a"
	default:
		return "reply.wav"
	}
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
		sess.MarkMessageChanged(sess.Messages[i].ID)
		_ = s.sessionManager.Save(sess)
		return
	}
}

func (s *Server) sendTelegramMessage(ctx context.Context, botToken string, chatID string, threadID int64, text string) error {
	return s.sendTelegramMessageMode(ctx, botToken, chatID, threadID, text, "")
}

// telegramParseErrorMarker appears in Telegram's error when it rejects markup it cannot parse.
const telegramParseErrorMarker = "can't parse entities"

// sendTelegramRichMessage sends Markdown as Telegram HTML so headings, bold and lists read cleanly on a phone.
// If Telegram refuses the markup, the original text is sent as-is so the message is never lost.
func (s *Server) sendTelegramRichMessage(ctx context.Context, botToken string, chatID string, threadID int64, markdown string) error {
	html := tgformat.MarkdownToHTML(markdown)
	if strings.TrimSpace(html) == "" {
		return nil
	}
	err := s.sendTelegramMessageMode(ctx, botToken, chatID, threadID, html, "HTML")
	if err != nil && strings.Contains(strings.ToLower(err.Error()), telegramParseErrorMarker) {
		logging.Warn("Telegram rejected formatted message, resending as plain text: %s", sanitizeTelegramError(err))
		return s.sendTelegramMessage(ctx, botToken, chatID, threadID, markdown)
	}
	return err
}

func (s *Server) sendTelegramMessageMode(ctx context.Context, botToken string, chatID string, threadID int64, text string, parseMode string) error {
	payload := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
		payload["link_preview_options"] = map[string]interface{}{"is_disabled": true}
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
