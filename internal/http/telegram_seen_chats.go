package http

// Remembers the chats a Telegram bot has received messages from. The duplex poller consumes updates, so the
// chat discovery endpoint cannot rely on getUpdates once an integration is enabled.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/A2gent/brute/internal/storage"
)

const (
	telegramSeenChatsConfigKey = "seen_chats"
	maxSeenTelegramChats       = 50
)

func telegramChatCandidateFromPayload(chat telegramChatPayload) TelegramChatCandidate {
	return TelegramChatCandidate{
		ChatID:    fmt.Sprintf("%d", chat.ID),
		Type:      chat.Type,
		Title:     chat.Title,
		Username:  chat.Username,
		FirstName: chat.FirstName,
		LastName:  chat.LastName,
	}
}

func telegramSeenChats(integration *storage.Integration) []TelegramChatCandidate {
	if integration == nil {
		return nil
	}
	raw := strings.TrimSpace(integration.Config[telegramSeenChatsConfigKey])
	if raw == "" {
		return nil
	}
	var chats []TelegramChatCandidate
	if err := json.Unmarshal([]byte(raw), &chats); err != nil {
		return nil
	}
	return chats
}

// recordSeenTelegramChat stores the chat in the integration config (not saved). It reports whether anything changed.
func recordSeenTelegramChat(integration *storage.Integration, chat telegramChatPayload) bool {
	if integration == nil || chat.ID == 0 {
		return false
	}
	candidate := telegramChatCandidateFromPayload(chat)
	chats := telegramSeenChats(integration)
	for i, existing := range chats {
		if existing.ChatID != candidate.ChatID {
			continue
		}
		if existing == candidate {
			return false
		}
		chats[i] = candidate
		return storeSeenTelegramChats(integration, chats)
	}
	chats = append(chats, candidate)
	if len(chats) > maxSeenTelegramChats {
		chats = chats[len(chats)-maxSeenTelegramChats:]
	}
	return storeSeenTelegramChats(integration, chats)
}

func storeSeenTelegramChats(integration *storage.Integration, chats []TelegramChatCandidate) bool {
	raw, err := json.Marshal(chats)
	if err != nil {
		return false
	}
	if integration.Config == nil {
		integration.Config = map[string]string{}
	}
	integration.Config[telegramSeenChatsConfigKey] = string(raw)
	return true
}

// mergeTelegramChatCandidates combines lists, keeping the first occurrence of each chat id, sorted by id.
func mergeTelegramChatCandidates(lists ...[]TelegramChatCandidate) []TelegramChatCandidate {
	byID := map[string]TelegramChatCandidate{}
	for _, list := range lists {
		for _, candidate := range list {
			if _, exists := byID[candidate.ChatID]; !exists {
				byID[candidate.ChatID] = candidate
			}
		}
	}
	merged := make([]TelegramChatCandidate, 0, len(byID))
	for _, candidate := range byID {
		merged = append(merged, candidate)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].ChatID < merged[j].ChatID })
	return merged
}
