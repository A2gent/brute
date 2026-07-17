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
	// Replace any existing audio section so repeated renames do not stack stale sections
	// ahead of the canonical links isGeneratedMeetingMarkdown expects to find first.
	content = strings.TrimSpace(stripMeetingAudioSection(content))
	audioLines := []string{"## Audio Recordings", ""}
	if len(audioPaths) == 0 {
		audioLines = append(audioLines, "- No audio files saved.")
	} else {
		for _, audioPath := range audioPaths {
			audioLines = append(audioLines, fmt.Sprintf("- [%s](%s)", filepath.Base(audioPath), audioPath))
		}
	}
	audioSection := strings.Join(audioLines, "\n")

	sectionMarker := firstMeetingSectionMarker(content, "## Summary", "## Transcript")
	idx := strings.Index(content, sectionMarker)
	if sectionMarker == "" || idx < 0 {
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

func stripMeetingAudioSection(content string) string {
	lines := strings.Split(content, "\n")
	start := -1
	end := len(lines)
	for i, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), "## Audio Recordings") {
			start = i
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(strings.TrimSpace(lines[j]), "## ") {
					end = j
					break
				}
			}
			break
		}
	}
	if start < 0 {
		return content
	}

	parts := make([]string, 0, 2)
	if before := strings.TrimSpace(strings.Join(lines[:start], "\n")); before != "" {
		parts = append(parts, before)
	}
	if after := strings.TrimSpace(strings.Join(lines[end:], "\n")); after != "" {
		parts = append(parts, after)
	}
	return strings.Join(parts, "\n\n")
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
		item.NotesPath = frontmatter["notes_path"]
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
	item.SummaryMarkdown = parseMeetingSection(body, "Summary")
	item.TranscriptMarkdown = parseMeetingSection(body, "Transcript")
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
	return parseMeetingSection(content, "Transcript")
}

func parseMeetingSection(content, heading string) string {
	lines := strings.Split(content, "\n")
	marker := "## " + strings.TrimSpace(heading)
	start := -1
	for i, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), marker) {
			start = i + 1
			break
		}
	}
	if start < 0 || start >= len(lines) {
		return ""
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "## ") {
			end = i
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
}

func firstMeetingSectionMarker(content string, markers ...string) string {
	selected := ""
	selectedIndex := len(content) + 1
	for _, marker := range markers {
		if index := strings.Index(content, marker); index >= 0 && index < selectedIndex {
			selected = marker
			selectedIndex = index
		}
	}
	return selected
}

func replaceMeetingSection(content, heading, value string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	marker := "## " + strings.TrimSpace(heading)
	start := -1
	end := len(lines)
	for i, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), marker) {
			start = i
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(strings.TrimSpace(lines[j]), "## ") {
					end = j
					break
				}
			}
			break
		}
	}

	section := []string{marker}
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		section = append(section, "", trimmed)
	}
	if start < 0 {
		if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
			return strings.Join(section, "\n")
		}
		return strings.TrimSpace(content) + "\n\n" + strings.Join(section, "\n")
	}

	before := strings.TrimSpace(strings.Join(lines[:start], "\n"))
	after := strings.TrimSpace(strings.Join(lines[end:], "\n"))
	parts := make([]string, 0, 3)
	if before != "" {
		parts = append(parts, before)
	}
	parts = append(parts, strings.Join(section, "\n"))
	if after != "" {
		parts = append(parts, after)
	}
	return strings.Join(parts, "\n\n")
}

func updateMeetingSummaryInMarkdown(content, summary string) (string, error) {
	if !isGeneratedMeetingMarkdown(content) {
		return "", fmt.Errorf("not a generated meeting note")
	}
	item := parseMeetingHistoryFromMarkdown(content)
	_, body := parseMeetingFrontmatter(content)
	body = replaceMeetingSection(body, "Summary", summary)
	return enrichMeetingMarkdown(body, item), nil
}

func updateMeetingTranscriptInMarkdown(content, transcript string) (string, error) {
	if !isGeneratedMeetingMarkdown(content) {
		return "", fmt.Errorf("not a generated meeting note")
	}
	item := parseMeetingHistoryFromMarkdown(content)
	_, body := parseMeetingFrontmatter(content)
	body = replaceMeetingSection(body, "Transcript", transcript)
	return enrichMeetingMarkdown(body, item), nil
}

// discoverMeetingMarkdownFiles returns generated meeting note paths under notesFolder,
// including nested subfolders so notes organized by topic still appear in history.
func discoverMeetingMarkdownFiles(notesFolder string) ([]string, error) {
	trimmed := strings.TrimSpace(notesFolder)
	if trimmed == "" {
		return nil, nil
	}

	paths := make([]string, 0)
	err := filepath.WalkDir(trimmed, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
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

	requiredNonEmpty := []string{"meeting_id", "started_at", "ended_at"}
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
	return meetingHasAudioReferences(frontmatter, body)
}

func meetingHasAudioReferences(frontmatter map[string]string, body string) bool {
	if len(parseFrontmatterList(frontmatter, "audio_files")) > 0 {
		return true
	}
	return len(extractAllAudioLinksFromBody(body)) > 0
}

func updateMeetingTitleInMarkdown(content, newTitle, notesPath string) (string, error) {
	trimmedTitle := strings.TrimSpace(newTitle)
	if trimmedTitle == "" {
		trimmedTitle = "Meeting"
	}
	if !isGeneratedMeetingMarkdown(content) {
		return "", fmt.Errorf("not a generated meeting note")
	}

	item := parseMeetingHistoryFromMarkdown(content)
	_, body := parseMeetingFrontmatter(content)
	if len(item.AudioPaths) == 0 {
		// Keep audio links from the note body when frontmatter audio_files is empty so
		// rename does not replace real recordings with "No audio files saved.".
		item.AudioPaths = extractAllAudioLinksFromBody(body)
	}
	if strings.TrimSpace(notesPath) != "" {
		item.NotesPath = strings.TrimSpace(notesPath)
	}
	item.Title = trimmedTitle
	body = replaceMeetingHeadingTitle(body, trimmedTitle)
	return enrichMeetingMarkdown(body, item), nil
}

func replaceMeetingHeadingTitle(body, newTitle string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# Meeting:") {
			lines[i] = fmt.Sprintf("# Meeting: %s", newTitle)
			return strings.Join(lines, "\n")
		}
	}
	return body
}

func extractAudioLinksFromAudioSection(body string) []string {
	return extractAudioLinksFromSectionStartingAt(body, findMeetingAudioSectionStart(body))
}

func extractAllAudioLinksFromBody(body string) []string {
	lines := strings.Split(body, "\n")
	links := make([]string, 0)
	seen := make(map[string]struct{})
	for i, line := range lines {
		if !strings.EqualFold(strings.TrimSpace(line), "## Audio Recordings") {
			continue
		}
		for _, link := range extractAudioLinksFromSectionStartingAt(body, i+1) {
			if _, exists := seen[link]; exists {
				continue
			}
			seen[link] = struct{}{}
			links = append(links, link)
		}
	}
	return links
}

func findMeetingAudioSectionStart(body string) int {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), "## Audio Recordings") {
			return i + 1
		}
	}
	return -1
}

func extractAudioLinksFromSectionStartingAt(body string, start int) []string {
	if start < 0 {
		return nil
	}
	lines := strings.Split(body, "\n")
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
