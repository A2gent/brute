package http

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Meeting markdown helpers live outside meetings.go so the HTTP handlers stay focused
// on request validation and filesystem side effects while parsing/formatting remains local.
func buildMeetingBaseFileName(meetingID, title, startedAtRaw string) string {
	parsed := time.Now()
	if startedAtRaw != "" {
		if t, err := time.Parse(time.RFC3339, startedAtRaw); err == nil {
			parsed = t
		}
	}
	datePrefix := parsed.Format("2006-01-02_15-04-05")
	slug := sanitizeMeetingFilePart(title)
	if slug == "" {
		slug = sanitizeMeetingFilePart(meetingID)
	}
	if slug == "" {
		slug = "meeting"
	}
	return fmt.Sprintf("%s-%s", datePrefix, slug)
}

func sanitizeMeetingFilePart(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(trimmed) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || unicode.IsSpace(r):
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
		if b.Len() >= 64 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	return out
}

func enrichMeetingMarkdown(notesMarkdown string, item meetingHistoryItem) string {
	body := stripMeetingFrontmatter(strings.TrimSpace(notesMarkdown))
	body = injectMeetingAudioSection(body, item.AudioPaths)

	lines := []string{
		"---",
		fmt.Sprintf("meeting_id: %s", toYAMLScalar(item.MeetingID)),
		fmt.Sprintf("title: %s", toYAMLScalar(item.Title)),
		fmt.Sprintf("started_at: %s", toYAMLScalar(item.StartedAt)),
		fmt.Sprintf("ended_at: %s", toYAMLScalar(item.EndedAt)),
		fmt.Sprintf("notes_path: %s", toYAMLScalar(item.NotesPath)),
	}
	if len(item.AudioPaths) == 0 {
		lines = append(lines, "audio_files: []")
	} else {
		lines = append(lines, "audio_files:")
		for _, audioPath := range item.AudioPaths {
			lines = append(lines, fmt.Sprintf("  - %s", toYAMLScalar(audioPath)))
		}
	}
	lines = append(lines, "---", "", body)
	return strings.Join(lines, "\n")
}

func stripMeetingFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return content
	}
	end := strings.Index(content[4:], "\n---\n")
	if end < 0 {
		return content
	}
	bodyStart := 4 + end + len("\n---\n")
	if bodyStart >= len(content) {
		return ""
	}
	return strings.TrimSpace(content[bodyStart:])
}

func injectMeetingAudioSection(content string, audioPaths []string) string {
	content = strings.TrimSpace(content)
	audioLines := []string{"## Audio Recordings", ""}
	if len(audioPaths) == 0 {
		audioLines = append(audioLines, "- No audio files saved.")
	} else {
		for _, audioPath := range audioPaths {
			audioLines = append(audioLines, fmt.Sprintf("- [%s](%s)", filepath.Base(audioPath), audioPath))
		}
	}
	audioSection := strings.Join(audioLines, "\n")

	transcriptMarker := "## Transcript"
	idx := strings.Index(content, transcriptMarker)
	if idx < 0 {
		if content == "" {
			return audioSection
		}
		return content + "\n\n" + audioSection
	}
	prefix := strings.TrimSpace(content[:idx])
	suffix := strings.TrimSpace(content[idx:])
	if prefix == "" {
		return audioSection + "\n\n" + suffix
	}
	return prefix + "\n\n" + audioSection + "\n\n" + suffix
}

func toYAMLScalar(value string) string {
	escaped := strings.ReplaceAll(strings.TrimSpace(value), "'", "''")
	return "'" + escaped + "'"
}

func parseMeetingHistoryFromMarkdown(content string) meetingHistoryItem {
	item := meetingHistoryItem{
		AudioPaths: []string{},
	}

	frontmatter, body := parseMeetingFrontmatter(content)
	if frontmatter != nil {
		item.MeetingID = frontmatter["meeting_id"]
		item.Title = frontmatter["title"]
		item.StartedAt = frontmatter["started_at"]
		item.EndedAt = frontmatter["ended_at"]
		item.AudioPaths = parseFrontmatterList(frontmatter, "audio_files")
	}

	if item.Title == "" {
		item.Title = parseMeetingLineValue(body, "# Meeting:")
	}
	if item.StartedAt == "" {
		item.StartedAt = parseMeetingLineValue(body, "- Started:")
	}
	if item.EndedAt == "" {
		item.EndedAt = parseMeetingLineValue(body, "- Ended:")
	}
	item.TranscriptMarkdown = parseTranscriptSection(body)
	return item
}

func parseMeetingFrontmatter(content string) (map[string]string, string) {
	if !strings.HasPrefix(content, "---\n") {
		return nil, content
	}
	end := strings.Index(content[4:], "\n---\n")
	if end < 0 {
		return nil, content
	}
	frontmatterBody := content[4 : 4+end]
	body := strings.TrimSpace(content[4+end+len("\n---\n"):])

	parsed := make(map[string]string)
	currentKey := ""
	for _, line := range strings.Split(frontmatterBody, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") && currentKey != "" {
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			item = strings.Trim(item, "'")
			if existing := strings.TrimSpace(parsed[currentKey]); existing == "" {
				parsed[currentKey] = item
			} else {
				parsed[currentKey] = existing + "\n" + item
			}
			continue
		}

		sep := strings.Index(trimmed, ":")
		if sep <= 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:sep])
		value := strings.TrimSpace(trimmed[sep+1:])
		currentKey = key
		if value == "" || value == "[]" {
			parsed[key] = ""
			continue
		}
		parsed[key] = strings.Trim(value, "'")
	}
	return parsed, body
}

func parseFrontmatterList(frontmatter map[string]string, key string) []string {
	if frontmatter == nil {
		return nil
	}
	raw := strings.TrimSpace(frontmatter[key])
	if raw == "" {
		return nil
	}
	values := []string{}
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		values = append(values, trimmed)
	}
	return values
}

func parseMeetingLineValue(content, prefix string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		}
	}
	return ""
}

func parseTranscriptSection(content string) string {
	lines := strings.Split(content, "\n")
	start := -1
	for i, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), "## Transcript") {
			start = i + 1
			break
		}
	}
	if start < 0 || start >= len(lines) {
		return ""
	}
	return strings.TrimSpace(strings.Join(lines[start:], "\n"))
}

func discoverMeetingAudioByBaseName(audioFolder, baseName string) []string {
	if strings.TrimSpace(audioFolder) == "" || strings.TrimSpace(baseName) == "" {
		return nil
	}
	entries, err := os.ReadDir(audioFolder)
	if err != nil {
		return nil
	}
	prefix := baseName + "-"
	audioPaths := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, prefix) {
			audioPaths = append(audioPaths, filepath.Join(audioFolder, name))
		}
	}
	sort.Strings(audioPaths)
	return audioPaths
}

func parseStartedAtFromBaseName(baseName string, info fs.FileInfo) string {
	const layout = "2006-01-02_15-04-05"
	if len(baseName) >= len(layout) {
		if parsed, err := time.ParseInLocation(layout, baseName[:len(layout)], time.Local); err == nil {
			return parsed.Format(time.RFC3339)
		}
	}
	if info != nil {
		return info.ModTime().Format(time.RFC3339)
	}
	return ""
}

func parseMeetingTime(startedAt, updatedAt string) time.Time {
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(startedAt)); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(updatedAt)); err == nil {
		return t
	}
	return time.Time{}
}

func isGeneratedMeetingMarkdown(content string) bool {
	frontmatter, body := parseMeetingFrontmatter(content)
	if frontmatter == nil {
		return false
	}

	requiredNonEmpty := []string{"meeting_id", "started_at", "ended_at", "notes_path"}
	for _, key := range requiredNonEmpty {
		if strings.TrimSpace(frontmatter[key]) == "" {
			return false
		}
	}
	if _, hasAudioKey := frontmatter["audio_files"]; !hasAudioKey {
		return false
	}
	if !strings.Contains(body, "## Transcript") {
		return false
	}
	if !strings.Contains(body, "## Audio Recordings") {
		return false
	}
	return len(extractAudioLinksFromAudioSection(body)) > 0
}

func extractAudioLinksFromAudioSection(body string) []string {
	lines := strings.Split(body, "\n")
	start := -1
	for i, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), "## Audio Recordings") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil
	}

	links := make([]string, 0)
	for i := start; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "## ") {
			break
		}
		if !strings.HasPrefix(trimmed, "- [") {
			continue
		}
		openIdx := strings.Index(trimmed, "](")
		closeIdx := strings.LastIndex(trimmed, ")")
		if openIdx < 0 || closeIdx <= openIdx+2 {
			continue
		}
		link := strings.TrimSpace(trimmed[openIdx+2 : closeIdx])
		if link != "" {
			links = append(links, link)
		}
	}
	return links
}
