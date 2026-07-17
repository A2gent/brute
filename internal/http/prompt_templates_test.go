package http

import (
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
)

func TestServerPromptTemplatesFromSettingsUsesCustomValues(t *testing.T) {
	templates := serverPromptTemplatesFromSettings(map[string]string{
		gitPRDescriptionPromptTemplateSettingKey: "custom pr {{files}}",
		workflowBareStatusRetryPromptSettingKey:  "retry {{node_label}}",
		scheduleToCronSystemPromptSettingKey:     "custom scheduler",
	})

	if templates.GitPRDescriptionPromptTemplate != "custom pr {{files}}" {
		t.Fatalf("unexpected PR template: %q", templates.GitPRDescriptionPromptTemplate)
	}
	if templates.WorkflowBareStatusRetryPromptTemplate != "retry {{node_label}}" {
		t.Fatalf("unexpected retry template: %q", templates.WorkflowBareStatusRetryPromptTemplate)
	}
	if templates.ScheduleToCronSystemPrompt != "custom scheduler" {
		t.Fatalf("unexpected scheduler prompt: %q", templates.ScheduleToCronSystemPrompt)
	}
}

func TestServerPromptTemplatesFromSettingsFallsBackOnBlankValues(t *testing.T) {
	templates := serverPromptTemplatesFromSettings(map[string]string{
		gitReviewOverlayPromptTemplateSettingKey: "   ",
	})

	if templates.GitReviewOverlayPromptTemplate != defaultGitReviewOverlayPromptTemplate {
		t.Fatalf("expected default review overlay template")
	}
}

func TestComposeWorkflowNodePromptWithCustomTemplate(t *testing.T) {
	prompt := composeWorkflowNodePromptWithContextAndTemplate(
		&workflowDefinitionRuntime{Name: "Release"},
		workflowNodeRuntime{ID: "worker", Label: "Worker", Kind: "subagent"},
		"ship it",
		[]string{"upstream handoff"},
		"previous attempt",
		"parent context",
		true,
		true,
		"node={{node_label}}\nrequest={{user_request}}\ninputs={{upstream_outputs_section}}\nprevious={{previous_output_section}}\ntools={{implementation_tool_evidence_instruction}}",
	)

	for _, expected := range []string{
		"node=Worker",
		"request=ship it",
		"upstream handoff",
		"previous attempt",
		"For implementation nodes",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected prompt to include %q, got:\n%s", expected, prompt)
		}
	}
}

func TestWorkflowDefinitionFromMetadataUsesCustomReviewLoopPrompts(t *testing.T) {
	sess := session.New("workflow")
	sess.Metadata[workflowDefinitionMetadataKey] = map[string]interface{}{
		"id":   "wf",
		"name": "Review workflow",
		"nodes": []interface{}{
			map[string]interface{}{
				"id":   "loop",
				"kind": "review_loop",
			},
		},
	}
	templates := serverPromptTemplatesFromSettings(map[string]string{
		workflowReviewLoopWorkerPromptSettingKey:   "custom worker default",
		workflowReviewLoopReviewerPromptSettingKey: "custom reviewer default",
	})

	def, ok := workflowDefinitionFromMetadataWithTemplates(sess, templates)
	if !ok {
		t.Fatal("expected workflow definition")
	}

	instructions := map[string]string{}
	for _, node := range def.Nodes {
		instructions[node.ID] = node.Instruction
	}
	if instructions["loop__worker"] != "custom worker default" {
		t.Fatalf("unexpected worker instruction: %q", instructions["loop__worker"])
	}
	if instructions["loop__critic"] != "custom reviewer default" {
		t.Fatalf("unexpected reviewer instruction: %q", instructions["loop__critic"])
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
			"git_review_overlay": {"provider":"cursor"}
		}`,
	}

	prTarget := server.resolvePromptLLMTarget(settings, promptLLMCaseGitPRDescription)
	if prTarget.ProviderType != config.ProviderGoogle || prTarget.Model != "gemini-custom" {
		t.Fatalf("unexpected PR target: %#v", prTarget)
	}

	reviewTarget := server.resolvePromptLLMTarget(settings, promptLLMCaseGitReviewOverlay)
	if reviewTarget.ProviderType != config.ProviderCursor || reviewTarget.Model != "composer-2.5" {
		t.Fatalf("unexpected review target: %#v", reviewTarget)
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
