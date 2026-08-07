package http

import (
	"testing"

	"github.com/A2gent/brute/internal/config"
)

func TestServerPromptTemplatesFromSettingsUsesCustomValues(t *testing.T) {
	templates := serverPromptTemplatesFromSettings(map[string]string{
		gitPRDescriptionPromptTemplateSettingKey: "custom pr {{files}}",
		scheduleToCronSystemPromptSettingKey:     "custom scheduler",
	})

	if templates.GitPRDescriptionPromptTemplate != "custom pr {{files}}" {
		t.Fatalf("unexpected PR template: %q", templates.GitPRDescriptionPromptTemplate)
	}
	if templates.ScheduleToCronSystemPrompt != "custom scheduler" {
		t.Fatalf("unexpected scheduler prompt: %q", templates.ScheduleToCronSystemPrompt)
	}
}

func TestServerPromptTemplatesFromSettingsFallsBackOnBlankValues(t *testing.T) {
	templates := serverPromptTemplatesFromSettings(map[string]string{
		gitPRDescriptionPromptTemplateSettingKey: "   ",
	})

	if templates.GitPRDescriptionPromptTemplate != defaultGitPRDescriptionPromptTemplate {
		t.Fatalf("expected default PR description template")
	}
}

func TestDefaultPromptTemplateSettingsIncludesSessionAndMeetingSummary(t *testing.T) {
	t.Parallel()

	defaults := defaultPromptTemplateSettings()
	if defaults[sessionSummaryPromptTemplateSettingKey] == "" {
		t.Fatal("expected default session summary prompt template")
	}
	if defaults[meetingSummaryPromptTemplateSettingKey] == "" {
		t.Fatal("expected default meeting summary prompt template")
	}
	custom := serverPromptTemplatesFromSettings(map[string]string{
		sessionSummaryPromptTemplateSettingKey: "custom summary {{initial_user_message}}",
		meetingSummaryPromptTemplateSettingKey: "meeting summary {{transcript}}",
	})
	if custom.SessionSummaryPromptTemplate != "custom summary {{initial_user_message}}" {
		t.Fatalf("unexpected session summary template: %q", custom.SessionSummaryPromptTemplate)
	}
	if custom.MeetingSummaryPromptTemplate != "meeting summary {{transcript}}" {
		t.Fatalf("unexpected meeting summary template: %q", custom.MeetingSummaryPromptTemplate)
	}
}

func TestResolvePromptLLMTargetUsesPerPromptProviderAndModel(t *testing.T) {
	t.Parallel()

	server := &Server{config: config.DefaultConfig()}
	settings := map[string]string{
		promptLLMSettingsSettingKey: `{
			"git_pr_description": {"provider":"google", "model":"gemini-custom"},
			"session_summary": {"provider":"cursor"}
		}`,
	}

	prTarget := server.resolvePromptLLMTarget(settings, promptLLMCaseGitPRDescription)
	if prTarget.ProviderType != config.ProviderGoogle || prTarget.Model != "gemini-custom" {
		t.Fatalf("unexpected PR target: %#v", prTarget)
	}

	summaryTarget := server.resolvePromptLLMTarget(settings, promptLLMCaseSessionSummary)
	if summaryTarget.ProviderType != config.ProviderCursor || summaryTarget.Model != "composer-2.5" {
		t.Fatalf("unexpected session summary target: %#v", summaryTarget)
	}
}

func TestResolvePromptLLMTargetFallsBackToLegacyGitCommitProvider(t *testing.T) {
	t.Parallel()

	server := &Server{config: config.DefaultConfig()}
	settings := map[string]string{gitCommitProviderSettingKey: "openrouter"}

	target := server.resolvePromptLLMTarget(settings, promptLLMCaseGitCommit)
	if target.ProviderType != config.ProviderOpenRouter || target.Model != "openrouter/auto" {
		t.Fatalf("unexpected legacy git commit target: %#v", target)
	}
}
