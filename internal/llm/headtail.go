package llm

import "unicode/utf8"

// RouterPromptMaxBytes bounds classification state while preserving prompt intent.
const RouterPromptMaxBytes = 32 * 1024

const headTailMarker = "\n[truncated]\n"

// HeadTail caps text at maxBytes, keeping both ends because a prompt's intent is
// usually stated at its start and its final request at its end.
func HeadTail(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	budget := maxBytes - len(headTailMarker)
	if budget <= 0 {
		return ""
	}
	head, tail := text[:budget/2], text[len(text)-(budget-budget/2):]
	// Byte cuts can split a multi-byte rune at either boundary.
	for i := 0; i < utf8.UTFMax-1 && len(head) > 0; i++ {
		if r, size := utf8.DecodeLastRuneInString(head); r != utf8.RuneError || size != 1 {
			break
		}
		head = head[:len(head)-1]
	}
	for i := 0; i < utf8.UTFMax-1 && len(tail) > 0 && !utf8.RuneStart(tail[0]); i++ {
		tail = tail[1:]
	}
	return head + headTailMarker + tail
}
