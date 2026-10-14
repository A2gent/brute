package http

import (
	"strings"
	"sync"
	"time"
)

// telegramTopicWarningInterval limits how often the same chat gets the "cannot create a topic" notice.
const telegramTopicWarningInterval = 30 * time.Minute

var telegramTopicWarnings sync.Map // chat id -> time of the last notice

func (s *Server) shouldWarnTelegramTopic(chatID string) bool {
	now := time.Now()
	if last, ok := telegramTopicWarnings.Load(chatID); ok {
		if previous, ok := last.(time.Time); ok && now.Sub(previous) < telegramTopicWarningInterval {
			return false
		}
	}
	telegramTopicWarnings.Store(chatID, now)
	return true
}

// telegramTopicFixAdvice turns Telegram's createForumTopic error into advice the user can act on.
func telegramTopicFixAdvice(err error) string {
	text := ""
	if err != nil {
		text = strings.ToLower(err.Error())
	}
	switch {
	case strings.Contains(text, "not enough rights"), strings.Contains(text, "admin"), strings.Contains(text, "forbidden"):
		return "Fix: make the bot an administrator and turn on its \"Manage topics\" right (group > Edit > Administrators > the bot)."
	case strings.Contains(text, "not a forum"), strings.Contains(text, "topics are not enabled"):
		return "Fix: turn on Topics in the group (group > Edit > Topics)."
	case strings.Contains(text, "chat not found"):
		return "Fix: the bot is not in this group, add it first."
	default:
		return "Reason: " + sanitizeTelegramError(err)
	}
}

// telegramTopicFailureHint is the notice posted in the chat when a topic could not be created.
func telegramTopicFailureHint(err error) string {
	return "I could not create a new topic, so I am answering here and this message starts a separate conversation. " + telegramTopicFixAdvice(err)
}
