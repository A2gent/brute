package session

import (
	"regexp"
	"strings"
	"unicode"
)

const maxSessionSummaryLength = 120

var markdownSummaryPrefixRE = regexp.MustCompile(`^\s*(#{1,6}\s+|[-*+]\s+|\d+[.)]\s+|>\s+)`)

// SummaryFromContent converts a prompt or final answer into one compact sentence
// for dense session lists. It intentionally avoids another LLM call: this runs on
// every session create/finish path and must remain fast and deterministic.
func SummaryFromContent(content string) string {
	text := normalizeSummaryText(content)
	if text == "" {
		return ""
	}
	text = firstSummarySentence(text)
	return truncateSummary(text, maxSessionSummaryLength)
}

// RefreshSummary updates the persisted one-line summary from the best available
// session signal: terminal assistant output first, otherwise the initial prompt.
func (s *Session) RefreshSummary() {
	if s == nil {
		return
	}
	candidate := ""
	if s.Status == StatusCompleted || s.Status == StatusFailed {
		candidate = lastMeaningfulMessageContent(s.Messages, "assistant")
	}
	if candidate == "" {
		candidate = firstMeaningfulMessageContent(s.Messages, "user")
	}
	summary := SummaryFromContent(candidate)
	if summary == "" {
		return
	}
	s.SetSummary(summary)
}

func normalizeSummaryText(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = markdownSummaryPrefixRE.ReplaceAllString(line, "")
		line = strings.Trim(line, "`*_~ ")
		if line == "" {
			continue
		}
		parts = append(parts, line)
	}
	return strings.Join(strings.Fields(strings.Join(parts, " - ")), " ")
}

func firstSummarySentence(text string) string {
	runes := []rune(text)
	for i, r := range runes {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		if i+1 < len(runes) && !unicode.IsSpace(runes[i+1]) {
			continue
		}
		return strings.TrimSpace(string(runes[:i+1]))
	}
	return text
}

func truncateSummary(text string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	if maxRunes <= 1 {
		return string(runes[:maxRunes])
	}
	limit := maxRunes - 1
	cut := limit
	for i := limit; i >= 0; i-- {
		if unicode.IsSpace(runes[i]) {
			cut = i
			break
		}
	}
	if cut < maxRunes/2 {
		cut = limit
	}
	return strings.TrimSpace(string(runes[:cut])) + "…"
}

func firstMeaningfulMessageContent(messages []Message, role string) string {
	for _, msg := range messages {
		if msg.Role != role {
			continue
		}
		if strings.TrimSpace(msg.Content) != "" {
			return msg.Content
		}
	}
	return ""
}

func lastMeaningfulMessageContent(messages []Message, role string) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != role {
			continue
		}
		if strings.TrimSpace(msg.Content) != "" {
			return msg.Content
		}
	}
	return ""
}
