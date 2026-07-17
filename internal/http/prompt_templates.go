package http

import (
	"strings"

	"github.com/A2gent/brute/internal/logging"
)

const gitPRDescriptionPromptTemplateSettingKey = "AAGENT_GIT_PR_DESCRIPTION_PROMPT_TEMPLATE"
const gitReviewOverlayPromptTemplateSettingKey = "AAGENT_GIT_REVIEW_OVERLAY_PROMPT_TEMPLATE"
const workflowNodePromptTemplateSettingKey = "AAGENT_WORKFLOW_NODE_PROMPT_TEMPLATE"
const workflowReviewLoopWorkerPromptSettingKey = "AAGENT_WORKFLOW_REVIEW_LOOP_WORKER_PROMPT"
const workflowReviewLoopReviewerPromptSettingKey = "AAGENT_WORKFLOW_REVIEW_LOOP_REVIEWER_PROMPT"
const workflowReviewLoopReviewerSuffixPromptSettingKey = "AAGENT_WORKFLOW_REVIEW_LOOP_REVIEWER_SUFFIX_PROMPT"
const workflowBareStatusRetryPromptSettingKey = "AAGENT_WORKFLOW_BARE_STATUS_RETRY_PROMPT"
const scheduleToCronPromptTemplateSettingKey = "AAGENT_SCHEDULE_TO_CRON_PROMPT_TEMPLATE"
const scheduleToCronSystemPromptSettingKey = "AAGENT_SCHEDULE_TO_CRON_SYSTEM_PROMPT"
const sessionSummaryPromptTemplateSettingKey = "AAGENT_SESSION_SUMMARY_PROMPT_TEMPLATE"
const meetingSummaryPromptTemplateSettingKey = "AAGENT_MEETING_SUMMARY_PROMPT_TEMPLATE"

type serverPromptTemplates struct {
	GitCommitPromptTemplate               string
	GitPRDescriptionPromptTemplate        string
	GitReviewOverlayPromptTemplate        string
	WorkflowNodePromptTemplate            string
	WorkflowReviewLoopWorkerPrompt        string
	WorkflowReviewLoopReviewerPrompt      string
	WorkflowReviewLoopReviewerSuffix      string
	WorkflowBareStatusRetryPromptTemplate string
	ScheduleToCronPromptTemplate          string
	ScheduleToCronSystemPrompt            string
	SessionSummaryPromptTemplate          string
	MeetingSummaryPromptTemplate          string
}

func defaultServerPromptTemplates() serverPromptTemplates {
	return serverPromptTemplates{
		GitCommitPromptTemplate:               defaultGitCommitPromptTemplate,
		GitPRDescriptionPromptTemplate:        defaultGitPRDescriptionPromptTemplate,
		GitReviewOverlayPromptTemplate:        defaultGitReviewOverlayPromptTemplate,
		WorkflowNodePromptTemplate:            defaultWorkflowNodePromptTemplate,
		WorkflowReviewLoopWorkerPrompt:        defaultWorkflowReviewLoopWorkerPrompt,
		WorkflowReviewLoopReviewerPrompt:      defaultWorkflowReviewLoopReviewerPrompt,
		WorkflowReviewLoopReviewerSuffix:      defaultWorkflowReviewLoopReviewerSuffixPrompt,
		WorkflowBareStatusRetryPromptTemplate: defaultWorkflowBareStatusRetryPromptTemplate,
		ScheduleToCronPromptTemplate:          defaultScheduleToCronPromptTemplate,
		ScheduleToCronSystemPrompt:            defaultScheduleToCronSystemPrompt,
		SessionSummaryPromptTemplate:          defaultSessionSummaryPromptTemplate,
		MeetingSummaryPromptTemplate:          defaultMeetingSummaryPromptTemplate,
	}
}

func serverPromptTemplatesFromSettings(settings map[string]string) serverPromptTemplates {
	defaults := defaultServerPromptTemplates()
	return serverPromptTemplates{
		GitCommitPromptTemplate:               promptTemplateValue(settings, gitCommitPromptTemplateSettingKey, defaults.GitCommitPromptTemplate),
		GitPRDescriptionPromptTemplate:        promptTemplateValue(settings, gitPRDescriptionPromptTemplateSettingKey, defaults.GitPRDescriptionPromptTemplate),
		GitReviewOverlayPromptTemplate:        promptTemplateValue(settings, gitReviewOverlayPromptTemplateSettingKey, defaults.GitReviewOverlayPromptTemplate),
		WorkflowNodePromptTemplate:            promptTemplateValue(settings, workflowNodePromptTemplateSettingKey, defaults.WorkflowNodePromptTemplate),
		WorkflowReviewLoopWorkerPrompt:        promptTemplateValue(settings, workflowReviewLoopWorkerPromptSettingKey, defaults.WorkflowReviewLoopWorkerPrompt),
		WorkflowReviewLoopReviewerPrompt:      promptTemplateValue(settings, workflowReviewLoopReviewerPromptSettingKey, defaults.WorkflowReviewLoopReviewerPrompt),
		WorkflowReviewLoopReviewerSuffix:      promptTemplateValue(settings, workflowReviewLoopReviewerSuffixPromptSettingKey, defaults.WorkflowReviewLoopReviewerSuffix),
		WorkflowBareStatusRetryPromptTemplate: promptTemplateValue(settings, workflowBareStatusRetryPromptSettingKey, defaults.WorkflowBareStatusRetryPromptTemplate),
		ScheduleToCronPromptTemplate:          promptTemplateValue(settings, scheduleToCronPromptTemplateSettingKey, defaults.ScheduleToCronPromptTemplate),
		ScheduleToCronSystemPrompt:            promptTemplateValue(settings, scheduleToCronSystemPromptSettingKey, defaults.ScheduleToCronSystemPrompt),
		SessionSummaryPromptTemplate:          promptTemplateValue(settings, sessionSummaryPromptTemplateSettingKey, defaults.SessionSummaryPromptTemplate),
		MeetingSummaryPromptTemplate:          promptTemplateValue(settings, meetingSummaryPromptTemplateSettingKey, defaults.MeetingSummaryPromptTemplate),
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
		gitCommitPromptTemplateSettingKey:                defaults.GitCommitPromptTemplate,
		gitPRDescriptionPromptTemplateSettingKey:         defaults.GitPRDescriptionPromptTemplate,
		gitReviewOverlayPromptTemplateSettingKey:         defaults.GitReviewOverlayPromptTemplate,
		workflowNodePromptTemplateSettingKey:             defaults.WorkflowNodePromptTemplate,
		workflowReviewLoopWorkerPromptSettingKey:         defaults.WorkflowReviewLoopWorkerPrompt,
		workflowReviewLoopReviewerPromptSettingKey:       defaults.WorkflowReviewLoopReviewerPrompt,
		workflowReviewLoopReviewerSuffixPromptSettingKey: defaults.WorkflowReviewLoopReviewerSuffix,
		workflowBareStatusRetryPromptSettingKey:          defaults.WorkflowBareStatusRetryPromptTemplate,
		scheduleToCronPromptTemplateSettingKey:           defaults.ScheduleToCronPromptTemplate,
		scheduleToCronSystemPromptSettingKey:             defaults.ScheduleToCronSystemPrompt,
		sessionSummaryPromptTemplateSettingKey:           defaults.SessionSummaryPromptTemplate,
		meetingSummaryPromptTemplateSettingKey:           defaults.MeetingSummaryPromptTemplate,
	}
}

func renderPromptTemplate(template string, values map[string]string) string {
	rendered := template
	for key, value := range values {
		rendered = strings.ReplaceAll(rendered, "{{"+key+"}}", value)
	}
	return strings.TrimSpace(rendered)
}
