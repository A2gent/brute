package agent

import (
	"encoding/json"
	"strings"

	"github.com/A2gent/brute/internal/session"
)

// Summary calls are text-only. Persisted messages also contain image payloads,
// admission caches and provider signatures, none of which describe completed work.
// Build a separate view so filtering never mutates the archived transcript.
func transitionSummaryHistory(messages []session.Message) ([]byte, error) {
	type message struct {
		Role        string                    `json:"role"`
		Content     string                    `json:"content"`
		Images      []session.ImageAttachment `json:"images,omitempty"`
		ToolCalls   []session.ToolCall        `json:"tool_calls,omitempty"`
		ToolResults []session.ToolResult      `json:"tool_results,omitempty"`
	}
	view := make([]message, 0, len(messages))
	for _, msg := range messages {
		m := message{Role: msg.Role, Content: msg.Content}
		for _, img := range msg.Images {
			// Keep references for the handoff, never send encoded pixels as text.
			img.DataBase64, img.Root = "", ""
			if strings.HasPrefix(img.URL, "data:") {
				img.URL = ""
			}
			m.Images = append(m.Images, img)
		}
		for _, call := range msg.ToolCalls {
			call.ThoughtSignature = ""
			m.ToolCalls = append(m.ToolCalls, call)
		}
		for _, result := range msg.ToolResults {
			result.Metadata = nil
			m.ToolResults = append(m.ToolResults, result)
		}
		view = append(view, m)
	}
	return json.Marshal(view)
}

// JSON escaping can expand history again inside the request's content string.
// Size the encoded chunk, not just its raw bytes, to respect the target window.
func transitionSummaryChunkSize(text string, budget int) int {
	low, high := 0, min(len(text), budget)
	for low < high {
		mid := low + (high-low+1)/2
		encoded, _ := json.Marshal(text[:mid])
		if len(encoded) <= budget {
			low = mid
		} else {
			high = mid - 1
		}
	}
	for low > 0 && low < len(text) && (text[low]&0xc0) == 0x80 {
		low--
	}
	return low
}
