package http

import (
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/session"
)

func addRequestFailedAssistantMessage(sess *session.Session, err error) {
	if sess == nil || err == nil {
		return
	}
	errText := strings.TrimSpace(err.Error())
	if errText == "" {
		return
	}
	message := fmt.Sprintf("Request failed: %s", errText)
	if latestAssistantMessageMatches(sess, errText) || latestAssistantMessageMatches(sess, message) {
		return
	}
	sess.AddAssistantMessage(message, nil)
}

func latestAssistantMessageMatches(sess *session.Session, content string) bool {
	want := strings.TrimSpace(content)
	if sess == nil || want == "" {
		return false
	}
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		msg := sess.Messages[i]
		if msg.Role != "assistant" {
			continue
		}
		return strings.TrimSpace(msg.Content) == want
	}
	return false
}
