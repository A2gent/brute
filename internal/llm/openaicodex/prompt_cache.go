package openaicodex

import (
	"strings"
	"time"
)

// EffectivePromptCacheRetention limits the legacy retention field to documented
// models. GPT-5.6+ uses prompt_cache_options instead; mini/nano variants are not
// implicitly supported. See https://developers.openai.com/api/docs/guides/prompt-caching.
func EffectivePromptCacheRetention(model, retention string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(model, "/"); idx >= 0 {
		model = model[idx+1:]
	}
	// Dated snapshots inherit the base model's capabilities.
	if len(model) > 11 {
		if _, err := time.Parse("2006-01-02", model[len(model)-10:]); err == nil && model[len(model)-11] == '-' {
			model = model[:len(model)-11]
		}
	}
	switch model {
	case "gpt-5.5", "gpt-5.5-pro", "gpt-5.4", "gpt-5.2", "gpt-5.1-codex-max",
		"gpt-5.1", "gpt-5.1-codex", "gpt-5.1-codex-mini", "gpt-5.1-chat-latest",
		"gpt-5", "gpt-5-codex", "gpt-4.1":
	default:
		return ""
	}
	retention = strings.TrimSpace(retention)
	switch retention {
	case "", "24h":
		// These models have no additional cache-write charge.
		return "24h"
	case "in_memory":
		if model != "gpt-5.5" && model != "gpt-5.5-pro" {
			return "in_memory"
		}
	}
	// Omit invalid or model-incompatible settings rather than send a rejected field.
	return ""
}
