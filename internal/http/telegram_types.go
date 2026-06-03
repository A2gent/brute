package http

// Telegram DTOs, constants, and transport payload types used across split files.

import (
	"regexp"

	"github.com/A2gent/brute/internal/session"
)

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

const telegramMaxMessageRunes = 3900

const telegramMaxInboundAudioBytes = 25 * 1024 * 1024

const telegramMaxInboundImageBytes = 15 * 1024 * 1024

const telegramMaxCaptionRunes = 1024

var telegramBotTokenPattern = regexp.MustCompile(`bot[0-9]{5,}:[A-Za-z0-9_-]{20,}`)

type telegramMessageAuthor struct {
	IsBot bool `json:"is_bot"`
}

type telegramMessagePayload struct {
	MessageID       int                    `json:"message_id"`
	MessageThreadID int64                  `json:"message_thread_id"`
	Text            string                 `json:"text"`
	Caption         string                 `json:"caption"`
	Photo           []telegramPhotoPayload `json:"photo"`
	Voice           *telegramVoicePayload  `json:"voice"`
	Audio           *telegramAudioPayload  `json:"audio"`
	Document        *telegramAudioPayload  `json:"document"`
	Chat            telegramChatPayload    `json:"chat"`
	From            telegramMessageAuthor  `json:"from"`
}

type telegramPhotoPayload struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type telegramVoicePayload struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	MimeType string `json:"mime_type"`
}

type telegramAudioPayload struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
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

type telegramGetFilePayload struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      struct {
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	} `json:"result"`
}

type telegramInboundResponse struct {
	reply         string
	createdThread int64 // if > 0, a new topic was created
	sessionID     string
}

type telegramInboundPrompt struct {
	text     string
	images   []session.ImageAttachment
	metadata map[string]interface{}
}

type telegramReplyAudio struct {
	clipID      string
	contentType string
	data        []byte
}
