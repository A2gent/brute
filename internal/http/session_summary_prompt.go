package http

const defaultSessionSummaryPromptTemplate = `Generate a concise specific one-sentence session summary.
Summarize it as either initial problem that user had, or task that was accomplished.
Return plain text only: no markdown, no quotes, no prefixes.
The summary must be 6-18 words and capture the concrete task/outcome.
Do not answer with bare generic status words such as "Done", "OK", "Completed", "Fixed", or "Success".
Prefer the final assistant outcome when it contains specifics; otherwise summarize the initial user request.

Session status: {{status}}
Existing title: {{title}}
Initial user request:
{{initial_user_message}}

Latest assistant response:
{{latest_assistant_message}}

Recent transcript:
{{transcript}}

Summary:`
