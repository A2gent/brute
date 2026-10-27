package http

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
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

func resolveGlobalSystemPrompt(settings map[string]string) string {
	if configured := strings.TrimSpace(settings[globalSystemPromptSettingKey]); configured != "" {
		return configured
	}
	return strings.TrimSpace(os.Getenv(globalSystemPromptSettingKey))
}

// currentGlobalSystemPrompt is what children spawned now should receive. A
// child that spawns its own children passes on the prompt it inherited.
func (s *Server) currentGlobalSystemPrompt() string {
	settings := map[string]string{}
	if s != nil && s.store != nil {
		if loaded, err := s.store.GetSettings(); err == nil && loaded != nil {
			settings = loaded
		}
	}
	return resolveGlobalSystemPrompt(settings)
}

func globalSystemPromptFingerprint(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:8])
}

func applyChildGlobalSystemPrompt(labels, env map[string]string, prompt string) {
	prompt = strings.TrimSpace(prompt)
	if prompt != "" {
		env[globalSystemPromptSettingKey] = prompt
	}
	labels[globalPromptFingerprintLabelKey] = globalSystemPromptFingerprint(prompt)
}

// globalSystemPromptStale reports whether a warm child was spawned with a
// different global prompt; it must restart to pick up the edited setting.
func globalSystemPromptStale(labels map[string]string, current string) bool {
	return labels[globalPromptFingerprintLabelKey] != globalSystemPromptFingerprint(current)
}
