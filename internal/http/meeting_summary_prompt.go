package http

const defaultMeetingSummaryPromptTemplate = `Summarize the complete meeting transcript for future reference.
Return concise Markdown with these sections when supported by the transcript:
- Overview
- Key decisions
- Action items, including owner and due date when stated
- Open questions
Do not invent facts, owners, deadlines, or decisions. Omit empty sections.
Return only the summary Markdown without a preface or code fence.

Meeting title: {{title}}
Started at: {{started_at}}
Ended at: {{ended_at}}

Complete transcript:
{{transcript}}

Summary:`
