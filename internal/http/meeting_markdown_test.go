package http

import (
	"os"
	"path/filepath"
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
	updated, err := updateMeetingTitleInMarkdown(original, "Product review", "")
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

func TestUpdateMeetingTitleInMarkdownPreservesBodyAudioLinks(t *testing.T) {
	original := sampleMeetingMarkdown("Weekly sync")
	withoutFrontmatterAudio := strings.Replace(
		original,
		"audio_files:\n  - '/tmp/audio/2026-07-17_10-00-00-old-title-me.webm'",
		"audio_files: []",
		1,
	)

	updated, err := updateMeetingTitleInMarkdown(withoutFrontmatterAudio, "Product review", "")
	if err != nil {
		t.Fatalf("updateMeetingTitleInMarkdown() error = %v", err)
	}
	if !isGeneratedMeetingMarkdown(updated) {
		t.Fatalf("rename should keep audio links from note body, got:\n%s", updated)
	}
	if !strings.Contains(updated, "/tmp/audio/2026-07-17_10-00-00-old-title-me.webm") {
		t.Fatalf("audio link should be preserved, got:\n%s", updated)
	}
}

func TestDiscoverMeetingMarkdownFilesIncludesNestedNotes(t *testing.T) {
	dir := t.TempDir()
	nestedDir := filepath.Join(dir, "03-diary")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	notePath := filepath.Join(nestedDir, "2026-07-17_08-00-00-meeting.md")
	if err := os.WriteFile(notePath, []byte("# placeholder\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	paths, err := discoverMeetingMarkdownFiles(dir)
	if err != nil {
		t.Fatalf("discoverMeetingMarkdownFiles() error = %v", err)
	}
	if len(paths) != 1 || paths[0] != notePath {
		t.Fatalf("discoverMeetingMarkdownFiles() = %#v, want [%q]", paths, notePath)
	}
}

func TestUpdateMeetingTitleInMarkdownRejectsInvalidNote(t *testing.T) {
	_, err := updateMeetingTitleInMarkdown("# Plain note\n\nNo frontmatter.", "New title", "")
	if err == nil {
		t.Fatal("expected error for non-generated meeting markdown")
	}
}

func TestIsGeneratedMeetingMarkdownAcceptsFrontmatterAudio(t *testing.T) {
	original := sampleMeetingMarkdown("Weekly sync")
	withoutBodyAudioLinks := strings.Replace(
		original,
		"- [2026-07-17_10-00-00-old-title-me.webm](/tmp/audio/2026-07-17_10-00-00-old-title-me.webm)",
		"- No audio files saved.",
		1,
	)
	if !isGeneratedMeetingMarkdown(withoutBodyAudioLinks) {
		t.Fatalf("expected frontmatter audio_files to keep note listable:\n%s", withoutBodyAudioLinks)
	}
}

func TestUpdateMeetingTitleInMarkdownSyncsNotesPath(t *testing.T) {
	original := sampleMeetingMarkdown("Weekly sync")
	newPath := "/tmp/notes/03-diary/2026-07-17_09-12-34-11-meeting.md"
	updated, err := updateMeetingTitleInMarkdown(original, "Дневник Совещания", newPath)
	if err != nil {
		t.Fatalf("updateMeetingTitleInMarkdown() error = %v", err)
	}
	if !strings.Contains(updated, "notes_path: '/tmp/notes/03-diary/2026-07-17_09-12-34-11-meeting.md'") {
		t.Fatalf("notes_path should be synced to the current file path, got:\n%s", updated)
	}
}

func TestUpdateMeetingTitleInMarkdownDoesNotDuplicateAudioSections(t *testing.T) {
	current := sampleMeetingMarkdown("Weekly sync")
	for _, title := range []string{"Product review", "Дневник Совещания"} {
		updated, err := updateMeetingTitleInMarkdown(current, title, "")
		if err != nil {
			t.Fatalf("updateMeetingTitleInMarkdown(%q) error = %v", title, err)
		}
		if strings.Count(updated, "## Audio Recordings") != 1 {
			t.Fatalf("expected a single audio section after rename to %q, got:\n%s", title, updated)
		}
		current = updated
	}
}

func TestUpdateMeetingTranscriptPreservesSummary(t *testing.T) {
	original := sampleMeetingMarkdown("Weekly sync")
	withSummary, err := updateMeetingSummaryInMarkdown(original, "Decided to ship the release on Friday.")
	if err != nil {
		t.Fatalf("updateMeetingSummaryInMarkdown() error = %v", err)
	}

	updated, err := updateMeetingTranscriptInMarkdown(withSummary, "- [00:00:00] **Me:** replacement transcript")
	if err != nil {
		t.Fatalf("updateMeetingTranscriptInMarkdown() error = %v", err)
	}
	item := parseMeetingHistoryFromMarkdown(updated)
	if item.SummaryMarkdown != "Decided to ship the release on Friday." {
		t.Fatalf("summary was not preserved: %q", item.SummaryMarkdown)
	}
	if item.TranscriptMarkdown != "- [00:00:00] **Me:** replacement transcript" {
		t.Fatalf("unexpected transcript: %q", item.TranscriptMarkdown)
	}
}

func TestRenderMeetingSummaryPromptUsesEntireTranscript(t *testing.T) {
	prompt := renderMeetingSummaryPrompt(defaultMeetingSummaryPromptTemplate, meetingHistoryItem{
		Title:              "Weekly sync",
		StartedAt:          "2026-07-17T10:00:00Z",
		EndedAt:            "2026-07-17T10:30:00Z",
		TranscriptMarkdown: "- [00:00:01] **Me:** first topic\n- [00:29:00] **Them:** final decision",
	})

	for _, expected := range []string{"Weekly sync", "first topic", "final decision"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected prompt to contain %q, got:\n%s", expected, prompt)
		}
	}
}
