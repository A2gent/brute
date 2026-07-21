package storage

import "strings"

// Managed app settings are consumed by Brute itself and must stay out of
// process env. Everything else that legacy clients saved into app_settings is
// treated as custom env for agent/tool commands.
func isManagedAppSettingKey(key string) bool {
	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return true
	}

	exact := map[string]struct{}{
		"AAGENT_AGENT_DEFINITIONS_FOLDER":                     {},
		"AAGENT_CONTEXT_COMPACTION_PROMPT":                    {},
		"AAGENT_CONTEXT_COMPACTION_TRIGGER_PERCENT":           {},
		"AAGENT_GIT_COMMIT_PROVIDER":                          {},
		"AAGENT_GIT_COMMIT_PROMPT_TEMPLATE":                   {},
		"AAGENT_GIT_PR_DESCRIPTION_PROMPT_TEMPLATE":           {},
		"AAGENT_LLM_RETRIES":                                  {},
		"AAGENT_MY_MIND_ROOT_FOLDER":                          {},
		"AAGENT_NAME":                                         {},
		"AAGENT_SCHEDULE_TO_CRON_PROMPT_TEMPLATE":             {},
		"AAGENT_SCHEDULE_TO_CRON_SYSTEM_PROMPT":               {},
		"AAGENT_SESSIONS_FOLDER":                              {},
		"AAGENT_SESSION_SUMMARY_PROMPT_TEMPLATE":              {},
		"AAGENT_SYSTEM_PROMPT_APPEND":                         {},
		"AAGENT_WORKFLOW_BARE_STATUS_RETRY_PROMPT":            {},
		"AAGENT_WORKFLOW_NODE_PROMPT_TEMPLATE":                {},
		"AAGENT_WORKFLOW_REVIEW_LOOP_REVIEWER_PROMPT":         {},
		"AAGENT_WORKFLOW_REVIEW_LOOP_REVIEWER_SUFFIX_PROMPT":  {},
		"AAGENT_WORKFLOW_REVIEW_LOOP_WORKER_PROMPT":           {},
		"A2GENT_AGENT_BASE_SYSTEM_PROMPT":                     {},
		"A2GENT_AGENT_INSTRUCTION_BLOCKS":                     {},
		"A2GENT_DISABLED_TOOLS":                               {},
		"A2GENT_DISABLE_TOOLS_BY_DEFAULT":                     {},
		"A2GENT_DISABLE_TOOLS_BY_DEFAULT_APPLIED":             {},
		"A2GENT_EXTERNAL_MARKDOWN_DISABLED_SKILLS":            {},
		"A2GENT_FILE_INDEXING_ENABLED":                        {},
		"A2GENT_LLM_PROVIDER_PROXY_ENABLED":                   {},
		"A2GENT_PROMPT_LLM_SETTINGS":                          {},
		"A2GENT_PROJECT_AGENT_DEFINITIONS_DIRECTORY":          {},
		"A2GENT_PROJECT_BRANCH_TASK_DOC_DIRECTORY":            {},
		"A2GENT_PROJECT_BRANCH_TASK_DOC_MODE":                 {},
		"A2GENT_PROJECT_INSTRUCTION_BLOCKS":                   {},
		"A2GENT_SKILL_CAMERA_INDEX":                           {},
		"A2GENT_SKILL_CAMERA_OUTPUT_DIR":                      {},
		"A2GENT_SKILL_SCREENSHOT_DISPLAY_INDEX":               {},
		"A2GENT_SKILL_SCREENSHOT_OUTPUT_DIR":                  {},
		"A2GENT_SYNC_DISABLED_TOOLS_FROM_ENV":                 {},
		"A2GENT_TOOL_RESULT_COMPRESSION_ENABLED":              {},
		"A2GENT_CLAUDE_RUNTIME_REASONING_PERSISTENCE_ENABLED": {},
	}
	if _, ok := exact[trimmedKey]; ok {
		return true
	}

	managedPrefixes := []string{
		"A2A_",
		"A2GENT_MEETINGS_",
		"A2GENT_PROJECT_BRANCH_TASK_DOC_DIRECTORY.",
		"A2GENT_PROJECT_BRANCH_TASK_DOC_MODE.",
	}
	for _, prefix := range managedPrefixes {
		if strings.HasPrefix(trimmedKey, prefix) {
			return true
		}
	}
	return false
}
