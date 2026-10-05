package contextcompress

import (
	"fmt"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
)

const (
	DefaultAdmissionMaxTokens = 8000
	// Leave room for the retrieval notice and both excerpts even with tiny settings.
	MinAdmissionMaxTokens = 128
)

// AdmitToolResult bounds text before it enters the transcript, independently of
// optional request-time compression. Tokens use the project's rune/4 estimate;
// image payloads remain separate and have provider-specific vision costs.
func (c *Compressor) AdmitToolResult(sess *session.Session, tr llm.ToolResult, maxTokens int) llm.ToolResult {
	original := tr.Content
	tr = llm.SeparateToolResultImages(tr)
	if raw, ok := tr.Metadata["image_original_text"].(string); ok {
		original = raw
		metadata := make(map[string]interface{}, len(tr.Metadata))
		for key, value := range tr.Metadata {
			if key != "image_original_text" {
				metadata[key] = value
			}
		}
		tr.Metadata = metadata
	}
	if maxTokens <= 0 {
		maxTokens = DefaultAdmissionMaxTokens
	}
	if maxTokens < MinAdmissionMaxTokens {
		maxTokens = MinAdmissionMaxTokens
	}
	originalRunes := utf8.RuneCountInString(tr.Content)
	// Divide first to avoid overflow when configuration has a very large limit.
	if (originalRunes+3)/4 <= maxTokens {
		return tr
	}
	value := entry{SessionID: sess.ID, ToolName: tr.Name, ToolCallID: tr.ToolCallID, Original: original}
	value.Hash = c.store.put(sess.ID, value)
	notice := fmt.Sprintf("[brute-compressed kind=tool_result admission_cap hash=%s original_tokens_approx=%d]\nText exceeded the admission cap. Use context_retrieve with this hash and an optional query for omitted content.\n", value.Hash, (originalRunes+3)/4)
	separator := "\n... [middle omitted] ...\n"
	remaining := maxTokens*4 - utf8.RuneCountInString(notice+separator)
	head := runeHead(tr.Content, remaining/2)
	tail := runeTail(tr.Content, remaining-remaining/2)
	value.Compressed = notice + head + separator + tail
	c.store.put(sess.ID, value)
	// Use the same session metadata store as request-time compression. Mutating
	// the active session avoids a separate stale-session Save overwriting a turn.
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}
	stored := readSessionEntries(sess.Metadata)
	stored[value.Hash] = value
	sess.Metadata[sessionCCRMetadataKey] = encodeSessionEntries(stored)
	tr.Content = value.Compressed
	return tr
}

func runeHead(text string, count int) string {
	for i := range text {
		if count == 0 {
			return text[:i]
		}
		count--
	}
	return text
}

func runeTail(text string, count int) string {
	end := len(text)
	for count > 0 && end > 0 {
		_, size := utf8.DecodeLastRuneInString(text[:end])
		end -= size
		count--
	}
	return text[end:]
}
