package agent

import (
	"encoding/json"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
)

// Keep encrypted provider state with the assistant turn so stateless tool loops
// and resumed sessions can replay it without exposing the model's reasoning.
func preserveReasoningItems(metadata map[string]interface{}, response *llm.ChatResponse, sess *session.Session) {
	if len(response.ReasoningItems) == 0 {
		return
	}
	if raw, err := json.Marshal(response.ReasoningItems); err == nil {
		metadata[messageMetadataReasoningItems] = string(raw)
		metadata["reasoning_context_epoch"], _ = sess.Metadata["provider_context_epoch"].(string)
	}
}
