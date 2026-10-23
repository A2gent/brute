package http

import (
	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
)

func sessionCurrentContextTokens(sess *session.Session) int {
	current := int(metadataNumber(sess.Metadata, "current_context_tokens"))
	lastInput := int(metadataNumber(sess.Metadata, "last_llm_input_tokens"))
	lastCached := int(metadataNumber(sess.Metadata, "last_llm_cached_input_tokens"))
	provider, _ := sess.Metadata["last_llm_provider"].(string)
	// Old Claude CLI results stored only the uncached suffix as context. Their
	// aggregate cache count cannot reconstruct the last internal turn; let the
	// UI estimate from the transcript instead of showing a false near-zero value.
	if config.IsClaudeProviderRef(provider) && lastCached > lastInput && current == lastInput {
		return 0
	}
	return current
}
