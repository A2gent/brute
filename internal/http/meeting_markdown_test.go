package http

import (
	"strings"
	"testing"
)

func sampleMeetingMarkdown(title string) string {
	item := meetingHistoryItem{
		MeetingID:  "abc-123",
		Title:      title,
		StartedAt:  "2026-07-17T10:00:00Z",
		EndedAt:    "2026-07-17T10:30:00Z",
		NotesPath:  "/tmp/notes/2026-07-17_10-00-00-old-title.md",
		AudioPaths: []string{"/tmp/audio/2026-07-17_10-00-00-old-title-me.webm"},
	}
	body := strings.Join([]string{
		"# Meeting: " + title,
		"",
		"- Started: 2026-07-17T10:00:00Z",
		"- Ended: 2026-07-17T10:30:00Z",
		"",
		"## Audio Recordings",
		"",
		"- [2026-07-17_10-00-00-old-title-me.webm](/tmp/audio/2026-07-17_10-00-00-old-title-me.webm)",
		"",
		"## Transcript",
		"",
		"- [00:00:01] **Me:** hello",
	}, "\n")
	return enrichMeetingMarkdown(body, item)
}

func TestUpdateMeetingTitleInMarkdown(t *testing.T) {
	original := sampleMeetingMarkdown("Weekly sync")
	updated, err := updateMeetingTitleInMarkdown(original, "Product review")
	if err != nil {
		t.Fatalf("updateMeetingTitleInMarkdown() error = %v", err)
	}
	if !strings.Contains(updated, "title: 'Product review'") {
		t.Fatalf("updated frontmatter title missing, got:\n%s", updated)
	}
	if !strings.Contains(updated, "# Meeting: Product review") {
		t.Fatalf("updated meeting heading missing, got:\n%s", updated)
	}
	if !strings.Contains(updated, "meeting_id: 'abc-123'") {
		t.Fatalf("meeting_id should be preserved, got:\n%s", updated)
	}
	if !strings.Contains(updated, "## Transcript") {
		t.Fatalf("transcript section should be preserved, got:\n%s", updated)
	}
}

func TestUpdateMeetingTitleInMarkdownRejectsInvalidNote(t *testing.T) {
	_, err := updateMeetingTitleInMarkdown("# Plain note\n\nNo frontmatter.", "New title")
	if err == nil {
		t.Fatal("expected error for non-generated meeting markdown")
	}
}
