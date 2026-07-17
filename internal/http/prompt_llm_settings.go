package http

import (
	"encoding/json"
	"strings"

	"github.com/A2gent/brute/internal/config"
)

const promptLLMSettingsSettingKey = "A2GENT_PROMPT_LLM_SETTINGS"

const (
	promptLLMCaseGitCommit        = "git_commit"
	promptLLMCaseGitPRDescription = "git_pr_description"
	promptLLMCaseGitReviewOverlay = "git_review_overlay"
	promptLLMCaseScheduleToCron   = "schedule_to_cron"
	promptLLMCaseSessionSummary   = "session_summary"
	promptLLMCaseMeetingSummary   = "meeting_summary"
)

type promptLLMSetting struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

type resolvedPromptLLMTarget struct {
	ProviderType config.ProviderType
	Model        string
}

func normalizePromptLLMCase(raw string) string {
	return strings.TrimSpace(strings.ToLower(raw))
}

func parsePromptLLMSettings(raw string) map[string]promptLLMSetting {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]promptLLMSetting{}
	}
	var decoded map[string]promptLLMSetting
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return map[string]promptLLMSetting{}
	}
	normalized := make(map[string]promptLLMSetting, len(decoded))
	for rawCase, setting := range decoded {
		promptCase := normalizePromptLLMCase(rawCase)
		if promptCase == "" {
			continue
		}
		provider := config.NormalizeProviderRef(setting.Provider)
		model := strings.TrimSpace(setting.Model)
		if provider == "" && model == "" {
			continue
		}
		normalized[promptCase] = promptLLMSetting{Provider: provider, Model: model}
	}
	return normalized
}

func promptLLMSettingsFromSettings(settings map[string]string) map[string]promptLLMSetting {
	if settings == nil {
		return map[string]promptLLMSetting{}
	}
	return parsePromptLLMSettings(settings[promptLLMSettingsSettingKey])
}

func (s *Server) resolvePromptLLMTarget(settings map[string]string, promptCase string) resolvedPromptLLMTarget {
	activeProvider := config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))
	return s.resolvePromptLLMTargetWithFallback(settings, promptCase, activeProvider, s.resolveModelForProvider(activeProvider))
}

func (s *Server) resolvePromptLLMTargetWithFallback(settings map[string]string, promptCase string, fallbackProvider config.ProviderType, fallbackModel string) resolvedPromptLLMTarget {
	promptCase = normalizePromptLLMCase(promptCase)
	promptSettings := promptLLMSettingsFromSettings(settings)
	setting := promptSettings[promptCase]

	providerRef := config.NormalizeProviderRef(setting.Provider)
	if providerRef == "" && promptCase == promptLLMCaseGitCommit {
		// WHY: older installs stored only the Git commit provider override as an env-style
		// app setting. Keep reading it so existing SQLite settings migrate without users
		// reselecting a provider, while new writes use A2GENT_PROMPT_LLM_SETTINGS.
		providerRef = config.NormalizeProviderRef(settings[gitCommitProviderSettingKey])
	}
	if providerRef == "" {
		providerRef = config.NormalizeProviderRef(string(fallbackProvider))
	}
	providerType := config.ProviderType(providerRef)
	model := strings.TrimSpace(setting.Model)
	if model == "" && providerType == fallbackProvider {
		model = strings.TrimSpace(fallbackModel)
	}
	if model == "" {
		model = s.resolveModelForProvider(providerType)
	}
	return resolvedPromptLLMTarget{ProviderType: providerType, Model: model}
}
