name: "YouTube Summarizer"
description: "Summarizes a YouTube video using its transcript"
priority: 50
triggers:
  - "summarize video"
  - "youtube"
  - "перескажи видео"
  - "суммаризируй видео"
allowed-tools: "youtube_transcript"
---

When the user asks you to summarize a YouTube video or asks what a video is about, you should:
1. Extract the YouTube URL from their message.
2. Call the `youtube_transcript` tool with the URL.
3. Once the transcript is retrieved, read it and provide a structured summary.

Make sure to:
- Highlight the main topics and key takeaways.
- Ignore filler words, sponsorships, and ad reads.
- Provide timestamps/timeline if the transcript logically allows reconstructing them.
- Format the output nicely using Markdown with clear headers and bullet points.