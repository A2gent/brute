package http

// Telegram inbound message parsing, transcription, and agent handoff.

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechengine"
	"github.com/A2gent/brute/internal/storage"
)

func (s *Server) telegramPromptFromInboundMessage(
	ctx context.Context,
	botToken string,
	integration *storage.Integration,
	message *telegramMessagePayload,
) (*telegramInboundPrompt, error) {
	if message == nil {
		return &telegramInboundPrompt{}, nil
	}
	text := strings.TrimSpace(message.Text)
	if text != "" {
		return &telegramInboundPrompt{
			text: text,
			metadata: map[string]interface{}{
				"inbound_channel":      "telegram",
				"inbound_message_type": "text",
			},
		}, nil
	}

	caption := strings.TrimSpace(message.Caption)
	if photoFileID, ok := telegramBestPhotoFileID(message); ok {
		image, err := s.downloadTelegramPhotoAttachment(ctx, botToken, photoFileID, message.MessageID)
		if err != nil {
			return nil, fmt.Errorf("failed to download photo message: %w", err)
		}
		promptText := caption
		if promptText == "" {
			promptText = "Please analyze the attached image."
		}
		return &telegramInboundPrompt{
			text:   promptText,
			images: []session.ImageAttachment{image},
			metadata: map[string]interface{}{
				"inbound_channel":      "telegram",
				"inbound_message_type": "photo",
			},
		}, nil
	}
	fileID, mediaKind := telegramAudioFileIDForMessage(message)
	if fileID == "" {
		return &telegramInboundPrompt{
			text: caption,
			metadata: map[string]interface{}{
				"inbound_channel":      "telegram",
				"inbound_message_type": "caption",
			},
		}, nil
	}
	logging.Info("Telegram inbound %s detected: chat=%d thread=%d has_caption=%v", mediaKind, message.Chat.ID, message.MessageThreadID, caption != "")
	if !telegramVoiceTranscriptionEnabled(integration) {
		integrationID := ""
		if integration != nil {
			integrationID = integration.ID
		}
		logging.Info("Telegram inbound %s transcription disabled for integration %s", mediaKind, integrationID)
		return &telegramInboundPrompt{
			text: caption,
			metadata: map[string]interface{}{
				"inbound_channel":      "telegram",
				"inbound_message_type": mediaKind,
			},
		}, nil
	}

	audioPath, cleanup, err := s.downloadTelegramFile(ctx, botToken, fileID, telegramMaxInboundAudioBytes, "audio")
	if err != nil {
		return nil, fmt.Errorf("failed to download %s message: %w", mediaKind, err)
	}
	defer cleanup()

	normalizedPath, normalizedCleanup, err := convertAudioToWAVForWhisper(ctx, audioPath)
	if err != nil {
		return nil, fmt.Errorf("failed to normalize %s message audio: %w", mediaKind, err)
	}
	if normalizedCleanup != nil {
		defer normalizedCleanup()
	}
	if normalizedPath != audioPath {
		logging.Info("Telegram inbound %s converted to WAV for whisper: source=%s target=%s", mediaKind, audioPath, normalizedPath)
	}

	transcript, err := s.transcribeTelegramAudio(ctx, normalizedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to transcribe %s message: %w", mediaKind, err)
	}
	transcript = strings.TrimSpace(transcript)
	logging.Info("Telegram inbound %s transcription completed: chat=%d thread=%d transcript_len=%d", mediaKind, message.Chat.ID, message.MessageThreadID, len([]rune(transcript)))
	metadata, metadataErr := s.telegramInboundAudioMetadataForMessage(normalizedPath, mediaKind)
	if metadataErr != nil {
		logging.Warn("Telegram inbound %s audio clip cache failed: %v", mediaKind, metadataErr)
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	metadata["inbound_channel"] = "telegram"
	metadata["inbound_message_type"] = mediaKind
	if transcript == "" {
		return &telegramInboundPrompt{text: caption, metadata: metadata}, nil
	}
	if caption == "" {
		return &telegramInboundPrompt{text: transcript, metadata: metadata}, nil
	}
	return &telegramInboundPrompt{
		text:     caption + "\n\nVoice transcript:\n" + transcript,
		metadata: metadata,
	}, nil
}

func telegramAudioFileIDForMessage(message *telegramMessagePayload) (string, string) {
	if message == nil {
		return "", ""
	}
	if message.Voice != nil {
		if fileID := strings.TrimSpace(message.Voice.FileID); fileID != "" {
			return fileID, "voice"
		}
	}
	if message.Audio != nil {
		if fileID := strings.TrimSpace(message.Audio.FileID); fileID != "" {
			return fileID, "audio"
		}
	}
	if message.Document != nil {
		fileID := strings.TrimSpace(message.Document.FileID)
		mimeType := strings.TrimSpace(strings.ToLower(message.Document.MimeType))
		if fileID != "" && strings.HasPrefix(mimeType, "audio/") {
			return fileID, "audio document"
		}
	}
	return "", ""
}

func telegramBestPhotoFileID(message *telegramMessagePayload) (string, bool) {
	if message == nil || len(message.Photo) == 0 {
		return "", false
	}
	for i := len(message.Photo) - 1; i >= 0; i-- {
		fileID := strings.TrimSpace(message.Photo[i].FileID)
		if fileID != "" {
			return fileID, true
		}
	}
	return "", false
}

func telegramVoiceTranscriptionEnabled(integration *storage.Integration) bool {
	raw := ""
	if integration != nil {
		raw = strings.TrimSpace(strings.ToLower(integration.Config["transcribe_voice_messages"]))
	}
	switch raw {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// transcribeTelegramAudio uses the speech-to-text engine chosen in the Speech settings (AAGENT_STT_ENGINE, including
// cloud engines), the same one chat voice input uses. Language and translation follow the Speech settings too, so
// Telegram never transcribes differently from the agent.
func (s *Server) transcribeTelegramAudio(ctx context.Context, audioPath string) (string, error) {
	logging.Info("Telegram inbound transcription via the configured speech engine")
	transcript, err := s.transcribePreparedAudio(ctx, "", audioPath, filepath.Base(audioPath), speechengine.TranscribeOptions{})
	if err != nil {
		return "", err
	}
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		return "", fmt.Errorf("speech engine returned no transcript")
	}
	return transcript, nil
}

func (s *Server) handleTelegramInboundMessage(
	ctx context.Context,
	integration *storage.Integration,
	chat telegramChatPayload,
	threadID int64,
	userMessage string,
	userImages []session.ImageAttachment,
	userMessageMetadata map[string]interface{},
) (*telegramInboundResponse, error) {
	if handled, reply := handleTelegramSlashCommand(userMessage); handled {
		return &telegramInboundResponse{reply: reply}, nil
	}

	chatID := strconv.FormatInt(chat.ID, 10)
	scopeKey := telegramSessionScopeKey(integration, chatID, threadID)

	// For general chat (threadID == 0), always create new session
	// For topics (threadID > 0), reuse existing session
	var sess *session.Session
	var err error
	if threadID == 0 {
		logging.Info("General chat message (threadID=0), forcing new session creation")
		sess = nil // Force new session creation
	} else {
		sess, err = s.findTelegramSession(integration.ID, chatID, scopeKey, threadID)
		if err != nil {
			return nil, err
		}
		if sess != nil {
			logging.Info("Found existing session for topic %d: session=%s", threadID, sess.ID)
		}
	}

	newSession := sess == nil
	createdThreadID := int64(0)
	topicWarning := ""

	if sess == nil {
		logging.Info("Creating new Telegram session for chat=%s threadID=%d", chatID, threadID)
		sess, err = s.sessionManager.Create("build")
		if err != nil {
			return nil, fmt.Errorf("failed to create Telegram session: %w", err)
		}
		logging.Info("Created new session: id=%s", sess.ID)

		if sess.Metadata == nil {
			sess.Metadata = map[string]interface{}{}
		}
		providerType := s.defaultSessionProviderRef()
		model := s.resolveModelForProvider(config.ProviderType(providerType))
		sess.Metadata["provider"] = providerType
		sess.Metadata["model"] = model
		sess.Metadata["integration_provider"] = "telegram"
		sess.Metadata["integration_id"] = integration.ID
		sess.Metadata["telegram_chat_id"] = chatID

		// Create topic for new sessions from general chat
		scope := strings.ToLower(strings.TrimSpace(integration.Config["session_scope"]))
		logging.Info("Telegram integration config: session_scope=%q (all config keys: %v)", scope, getConfigKeys(integration.Config))
		logging.Info("Telegram session evaluation: scope=%q threadID=%d scope_not_chat=%v threadID_zero=%v will_create_topic=%v",
			scope, threadID, scope != "chat", threadID == 0, threadID == 0 && scope != "chat")

		if threadID == 0 && scope != "chat" {
			botToken := strings.TrimSpace(integration.Config["bot_token"])
			if botToken != "" {
				topicName := telegramTopicNameForSession(sess, userMessage)
				logging.Info("Attempting to create Telegram forum topic: name=%s", topicName)
				createdThreadID, err = s.createTelegramForumTopic(ctx, botToken, chatID, topicName)
				if err != nil {
					logging.Warn("Failed to create Telegram topic for new session from general chat: %s", sanitizeTelegramError(err))
					topicWarning = telegramTopicFailureHint(err)
				} else {
					logging.Info("Successfully created Telegram forum topic: threadID=%d name=%s", createdThreadID, topicName)
					threadID = createdThreadID
					scopeKey = telegramSessionScopeKey(integration, chatID, threadID)
					sess.Metadata["telegram_topic_name"] = topicName
				}
			} else {
				logging.Warn("Cannot create topic: bot_token is empty")
			}
		} else {
			if threadID == 0 {
				logging.Info("Skipping topic creation: scope=%s (need scope != 'chat')", scope)
			} else {
				logging.Info("Skipping topic creation: already in thread %d", threadID)
			}
		}

		sess.Metadata["telegram_scope_key"] = scopeKey
		if threadID > 0 {
			sess.Metadata["telegram_thread_id"] = strconv.FormatInt(threadID, 10)
			logging.Info("Session metadata updated with threadID=%d", threadID)
		}
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist new Telegram session metadata: %v", err)
		} else {
			logging.Info("Successfully saved session metadata for session %s", sess.ID)
		}
	}
	if err := s.assignTelegramSessionToProject(sess, chat.Title); err != nil {
		logging.Warn("Failed to assign project for Telegram session %s: %v", sess.ID, err)
	}
	if newSession {
		s.publishSessionCatalog("session_created", sess)
	}

	sess.AddUserMessageWithImages(userMessage, userImages)
	if len(userMessageMetadata) > 0 && len(sess.Messages) > 0 {
		last := len(sess.Messages) - 1
		if sess.Messages[last].Role == "user" {
			sess.Messages[last].Metadata = userMessageMetadata
		}
	}
	llmUserMessage := telegramAgentPromptContext(userMessage, userMessageMetadata)

	providerType := s.resolveSessionProviderType(sess)
	model := s.resolveSessionModel(sess, providerType)
	target, err := s.resolveExecutionTarget(ctx, providerType, model, llmUserMessage, sess)
	if err != nil {
		sess.AddAssistantMessage(fmt.Sprintf("Unable to start request: %s", err.Error()), nil)
		sess.SetStatus(session.StatusFailed)
		_ = s.sessionManager.Save(sess)
		return nil, fmt.Errorf("provider configuration error: %w", err)
	}
	if setSessionRoutedProviderAndModel(sess, providerType, target.ProviderType, target.Model, target.RoutingRule, target.RoutingReason) {
		if err := s.sessionManager.Save(sess); err != nil {
			logging.Warn("Failed to persist Telegram session routed target metadata: %v", err)
		}
	}
	if event := routerDecisionStreamEvent(providerType, target); event != nil {
		s.publishSessionEvent(sess.ID, *event)
	}

	agentConfig := s.agentConfigFromTarget(sess, target, s.buildSystemPromptForSession(sess), s.config.MaxSteps, s.config.Temperature)
	ag := s.newAgentFromConfig(agentConfig, target.Client, s.toolManagerForSession(sess))

	response, _, err := ag.Run(ctx, sess, llmUserMessage)
	if err != nil {
		addRequestFailedAssistantMessage(sess, err)
		sess.SetStatus(session.StatusFailed)
		_ = s.sessionManager.Save(sess)
		return nil, fmt.Errorf("agent run failed: %w", err)
	}

	result := &telegramInboundResponse{reply: response, sessionID: sess.ID, topicWarning: topicWarning}
	if newSession && createdThreadID > 0 {
		result.createdThread = createdThreadID
	}
	return result, nil
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
