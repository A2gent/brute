package http

import (
	"strings"

	"github.com/A2gent/brute/internal/logging"
)

const gitPRDescriptionPromptTemplateSettingKey = "AAGENT_GIT_PR_DESCRIPTION_PROMPT_TEMPLATE"
const scheduleToCronPromptTemplateSettingKey = "AAGENT_SCHEDULE_TO_CRON_PROMPT_TEMPLATE"
const scheduleToCronSystemPromptSettingKey = "AAGENT_SCHEDULE_TO_CRON_SYSTEM_PROMPT"
const sessionSummaryPromptTemplateSettingKey = "AAGENT_SESSION_SUMMARY_PROMPT_TEMPLATE"
const meetingSummaryPromptTemplateSettingKey = "AAGENT_MEETING_SUMMARY_PROMPT_TEMPLATE"
const pronunciationSystemPromptSettingKey = "AAGENT_PRONUNCIATION_SYSTEM_PROMPT"

type serverPromptTemplates struct {
	PronunciationSystemPrompt      string
	GitCommitPromptTemplate        string
	GitPRDescriptionPromptTemplate string
	ScheduleToCronPromptTemplate   string
	ScheduleToCronSystemPrompt     string
	SessionSummaryPromptTemplate   string
	MeetingSummaryPromptTemplate   string
}

func defaultServerPromptTemplates() serverPromptTemplates {
	return serverPromptTemplates{
		PronunciationSystemPrompt:      defaultPronunciationSystemPrompt,
		GitCommitPromptTemplate:        defaultGitCommitPromptTemplate,
		GitPRDescriptionPromptTemplate: defaultGitPRDescriptionPromptTemplate,
		ScheduleToCronPromptTemplate:   defaultScheduleToCronPromptTemplate,
		ScheduleToCronSystemPrompt:     defaultScheduleToCronSystemPrompt,
		SessionSummaryPromptTemplate:   defaultSessionSummaryPromptTemplate,
		MeetingSummaryPromptTemplate:   defaultMeetingSummaryPromptTemplate,
	}
}

func serverPromptTemplatesFromSettings(settings map[string]string) serverPromptTemplates {
	defaults := defaultServerPromptTemplates()
	return serverPromptTemplates{
		PronunciationSystemPrompt:      promptTemplateValue(settings, pronunciationSystemPromptSettingKey, defaults.PronunciationSystemPrompt),
		GitCommitPromptTemplate:        promptTemplateValue(settings, gitCommitPromptTemplateSettingKey, defaults.GitCommitPromptTemplate),
		GitPRDescriptionPromptTemplate: promptTemplateValue(settings, gitPRDescriptionPromptTemplateSettingKey, defaults.GitPRDescriptionPromptTemplate),
		ScheduleToCronPromptTemplate:   promptTemplateValue(settings, scheduleToCronPromptTemplateSettingKey, defaults.ScheduleToCronPromptTemplate),
		ScheduleToCronSystemPrompt:     promptTemplateValue(settings, scheduleToCronSystemPromptSettingKey, defaults.ScheduleToCronSystemPrompt),
		SessionSummaryPromptTemplate:   promptTemplateValue(settings, sessionSummaryPromptTemplateSettingKey, defaults.SessionSummaryPromptTemplate),
		MeetingSummaryPromptTemplate:   promptTemplateValue(settings, meetingSummaryPromptTemplateSettingKey, defaults.MeetingSummaryPromptTemplate),
	}
}

func promptTemplateValue(settings map[string]string, key string, fallback string) string {
	if settings != nil {
		if value := strings.TrimSpace(settings[key]); value != "" {
			return value
		}
	}
	return strings.TrimSpace(fallback)
}

func (s *Server) loadPromptTemplates() serverPromptTemplates {
	if s == nil || s.store == nil {
		return defaultServerPromptTemplates()
	}
	settings, err := s.store.GetSettings()
	if err != nil {
		logging.Warn("Failed to load settings for prompt templates: %v", err)
		settings = map[string]string{}
	}
	return serverPromptTemplatesFromSettings(settings)
}

func defaultPromptTemplateSettings() map[string]string {
	defaults := defaultServerPromptTemplates()
	return map[string]string{
		pronunciationSystemPromptSettingKey:      defaults.PronunciationSystemPrompt,
		gitCommitPromptTemplateSettingKey:        defaults.GitCommitPromptTemplate,
		gitPRDescriptionPromptTemplateSettingKey: defaults.GitPRDescriptionPromptTemplate,
		scheduleToCronPromptTemplateSettingKey:   defaults.ScheduleToCronPromptTemplate,
		scheduleToCronSystemPromptSettingKey:     defaults.ScheduleToCronSystemPrompt,
		sessionSummaryPromptTemplateSettingKey:   defaults.SessionSummaryPromptTemplate,
		meetingSummaryPromptTemplateSettingKey:   defaults.MeetingSummaryPromptTemplate,
	}
}

func renderPromptTemplate(template string, values map[string]string) string {
	rendered := template
	for key, value := range values {
		rendered = strings.ReplaceAll(rendered, "{{"+key+"}}", value)
	}
	return strings.TrimSpace(rendered)
}
