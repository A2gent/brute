package http

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sort"
	"strings"
)

// The global system prompt applies to the main agent and every child agent,
// unlike A2GENT_AGENT_BASE_SYSTEM_PROMPT, which is the main agent's own role.
// Children have their own empty settings, so the parent passes it at spawn
// through the environment variable of the same name.
const (
	globalSystemPromptSettingKey    = "A2GENT_GLOBAL_SYSTEM_PROMPT"
	globalSystemPromptBlockType     = "global_prompt"
	globalPromptFingerprintLabelKey = "a2gent.global_prompt_fingerprint"
)

// inheritedPromptSettingKeys are Caesar-edited prompt settings that children
// compose their own prompts from, so they travel to children like the global prompt.
var inheritedPromptSettingKeys = []string{
	globalSystemPromptSettingKey,
	subAgentIdentityPromptSettingKey,
	subAgentBaseSystemPromptSettingKey,
}

// inheritedPromptSetting reads a prompt from local settings, then from the
// environment a parent passed at spawn.
func inheritedPromptSetting(settings map[string]string, key string) string {
	if configured := strings.TrimSpace(settings[key]); configured != "" {
		return configured
	}
	return strings.TrimSpace(os.Getenv(key))
}

func resolveGlobalSystemPrompt(settings map[string]string) string {
	return inheritedPromptSetting(settings, globalSystemPromptSettingKey)
}

// currentInheritedPrompts is what children spawned now should receive. A
// child that spawns its own children passes on the prompts it inherited.
func (s *Server) currentInheritedPrompts() map[string]string {
	settings := map[string]string{}
	if s != nil && s.store != nil {
		if loaded, err := s.store.GetSettings(); err == nil && loaded != nil {
			settings = loaded
		}
	}
	prompts := map[string]string{}
	for _, key := range inheritedPromptSettingKeys {
		if value := inheritedPromptSetting(settings, key); value != "" {
			prompts[key] = value
		}
	}
	return prompts
}

func inheritedPromptsFingerprint(prompts map[string]string) string {
	keys := make([]string, 0, len(prompts))
	for key, value := range prompts {
		if strings.TrimSpace(value) != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		hash.Write([]byte(key + "=" + strings.TrimSpace(prompts[key]) + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil)[:8])
}

func applyChildGlobalSystemPrompt(labels, env map[string]string, prompts map[string]string) {
	for key, value := range prompts {
		if value = strings.TrimSpace(value); value != "" {
			env[key] = value
		}
	}
	labels[globalPromptFingerprintLabelKey] = inheritedPromptsFingerprint(prompts)
}

// globalSystemPromptStale reports whether a warm child was spawned with
// different inherited prompts; it must restart to pick up the edited settings.
func globalSystemPromptStale(labels map[string]string, current map[string]string) bool {
	return labels[globalPromptFingerprintLabelKey] != inheritedPromptsFingerprint(current)
}
