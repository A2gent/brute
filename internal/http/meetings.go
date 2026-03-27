package http

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

const (
	maxMeetingMultipartMemory = 8 << 20
	maxMeetingAudioBytes      = 500 << 20
)

type saveMeetingArtifactsResponse struct {
	MeetingID string   `json:"meeting_id"`
	NotesPath string   `json:"notes_path"`
	AudioPath []string `json:"audio_paths"`
}

type listMeetingsResponse struct {
	Meetings []meetingHistoryItem `json:"meetings"`
}

type meetingHistoryItem struct {
	MeetingID          string   `json:"meeting_id,omitempty"`
	Title              string   `json:"title"`
	StartedAt          string   `json:"started_at,omitempty"`
	EndedAt            string   `json:"ended_at,omitempty"`
	NotesPath          string   `json:"notes_path"`
	AudioPaths         []string `json:"audio_paths"`
	TranscriptMarkdown string   `json:"transcript_markdown"`
	UpdatedAt          string   `json:"updated_at,omitempty"`
}

type deleteMeetingArtifactsRequest struct {
	NotesPath string   `json:"notes_path"`
	AudioPath []string `json:"audio_paths"`
}

type deleteMeetingArtifactsResponse struct {
	DeletedNotesPath string   `json:"deleted_notes_path"`
	DeletedAudioPath []string `json:"deleted_audio_paths"`
}

func (s *Server) handleSaveMeetingArtifacts(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxMeetingMultipartMemory); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid multipart request: "+err.Error())
		return
	}

	meetingID := strings.TrimSpace(r.FormValue("meeting_id"))
	if meetingID == "" {
		meetingID = uuid.NewString()
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = "Meeting"
	}
	startedAtRaw := strings.TrimSpace(r.FormValue("started_at"))
	endedAtRaw := strings.TrimSpace(r.FormValue("ended_at"))
	notesMarkdown := strings.TrimSpace(r.FormValue("notes_markdown"))
	if notesMarkdown == "" {
		s.errorResponse(w, http.StatusBadRequest, "notes_markdown is required")
		return
	}

	notesFolder, err := s.resolveMeetingStorageFolder(strings.TrimSpace(r.FormValue("notes_folder")))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid notes folder: "+err.Error())
		return
	}
	audioFolder, err := s.resolveMeetingStorageFolder(strings.TrimSpace(r.FormValue("audio_folder")))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid audio folder: "+err.Error())
		return
	}

	if err := os.MkdirAll(notesFolder, 0o755); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create notes folder: "+err.Error())
		return
	}
	if err := os.MkdirAll(audioFolder, 0o755); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create audio folder: "+err.Error())
		return
	}

	baseName := buildMeetingBaseFileName(meetingID, title, startedAtRaw)
	notesPath := filepath.Join(notesFolder, baseName+".md")

	audioPaths := make([]string, 0)
	if r.MultipartForm != nil {
		audioFiles := r.MultipartForm.File["audio"]
		for index, header := range audioFiles {
			src, openErr := header.Open()
			if openErr != nil {
				s.errorResponse(w, http.StatusBadRequest, "Failed to open uploaded audio: "+openErr.Error())
				return
			}

			ext := strings.TrimSpace(strings.ToLower(filepath.Ext(header.Filename)))
			if ext == "" || len(ext) > 10 {
				ext = ".webm"
			}
			stem := sanitizeMeetingFilePart(strings.TrimSuffix(filepath.Base(header.Filename), filepath.Ext(header.Filename)))
			if stem == "" {
				stem = fmt.Sprintf("speaker-%d", index+1)
			}
			audioPath := filepath.Join(audioFolder, fmt.Sprintf("%s-%s%s", baseName, stem, ext))

			dst, createErr := os.Create(audioPath)
			if createErr != nil {
				_ = src.Close()
				s.errorResponse(w, http.StatusInternalServerError, "Failed to create audio file: "+createErr.Error())
				return
			}

			if _, copyErr := io.Copy(dst, src); copyErr != nil {
				_ = dst.Close()
				_ = src.Close()
				s.errorResponse(w, http.StatusInternalServerError, "Failed to write audio file: "+copyErr.Error())
				return
			}

			if closeErr := dst.Close(); closeErr != nil {
				_ = src.Close()
				s.errorResponse(w, http.StatusInternalServerError, "Failed to finalize audio file: "+closeErr.Error())
				return
			}
			_ = src.Close()
			audioPaths = append(audioPaths, audioPath)
		}
	}

	finalMarkdown := enrichMeetingMarkdown(notesMarkdown, meetingHistoryItem{
		MeetingID:  meetingID,
		Title:      title,
		StartedAt:  startedAtRaw,
		EndedAt:    endedAtRaw,
		NotesPath:  notesPath,
		AudioPaths: audioPaths,
	})
	if err := os.WriteFile(notesPath, []byte(strings.TrimSpace(finalMarkdown)+"\n"), 0o644); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to write meeting notes: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, saveMeetingArtifactsResponse{
		MeetingID: meetingID,
		NotesPath: notesPath,
		AudioPath: audioPaths,
	})
}

func (s *Server) handleListMeetingArtifacts(w http.ResponseWriter, r *http.Request) {
	notesFolder, err := s.resolveMeetingStorageFolder(strings.TrimSpace(r.URL.Query().Get("notes_folder")))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid notes folder: "+err.Error())
		return
	}
	audioFolder, err := s.resolveMeetingStorageFolder(strings.TrimSpace(r.URL.Query().Get("audio_folder")))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid audio folder: "+err.Error())
		return
	}

	entries, err := os.ReadDir(notesFolder)
	if err != nil {
		if os.IsNotExist(err) {
			s.jsonResponse(w, http.StatusOK, listMeetingsResponse{Meetings: []meetingHistoryItem{}})
			return
		}
		s.errorResponse(w, http.StatusInternalServerError, "Failed to read notes folder: "+err.Error())
		return
	}

	meetings := make([]meetingHistoryItem, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			continue
		}
		notesPath := filepath.Join(notesFolder, entry.Name())
		contentBytes, readErr := os.ReadFile(notesPath)
		if readErr != nil {
			continue
		}
		content := string(contentBytes)
		if !isGeneratedMeetingMarkdown(content) {
			continue
		}

		item := parseMeetingHistoryFromMarkdown(content)
		if item.Title == "" {
			item.Title = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		}
		item.NotesPath = notesPath
		if len(item.AudioPaths) == 0 {
			item.AudioPaths = discoverMeetingAudioByBaseName(audioFolder, strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		}

		info, infoErr := entry.Info()
		if infoErr == nil {
			item.UpdatedAt = info.ModTime().Format(time.RFC3339)
			if item.StartedAt == "" {
				if startedAt := parseStartedAtFromBaseName(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())), info); startedAt != "" {
					item.StartedAt = startedAt
				}
			}
		}
		meetings = append(meetings, item)
	}

	sort.Slice(meetings, func(i, j int) bool {
		left := parseMeetingTime(meetings[i].StartedAt, meetings[i].UpdatedAt)
		right := parseMeetingTime(meetings[j].StartedAt, meetings[j].UpdatedAt)
		return left.After(right)
	})

	s.jsonResponse(w, http.StatusOK, listMeetingsResponse{Meetings: meetings})
}

func (s *Server) handleGetMeetingAudio(w http.ResponseWriter, r *http.Request) {
	rawPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if rawPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "path query parameter is required")
		return
	}

	absPath, err := filepath.Abs(rawPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid audio path")
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "audio file not found")
		return
	}
	if info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "path points to a directory")
		return
	}
	if info.Size() <= 0 {
		s.errorResponse(w, http.StatusBadRequest, "audio file is empty")
		return
	}
	if info.Size() > maxMeetingAudioBytes {
		s.errorResponse(w, http.StatusBadRequest, "audio file is too large")
		return
	}

	audioFile, err := os.Open(absPath)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "failed to open audio file")
		return
	}
	defer audioFile.Close()

	header := make([]byte, 512)
	n, _ := audioFile.Read(header)
	contentType := strings.TrimSpace(http.DetectContentType(header[:n]))
	if !strings.HasPrefix(contentType, "audio/") && contentType != "video/webm" && contentType != "application/ogg" {
		s.errorResponse(w, http.StatusBadRequest, "file is not a supported audio type")
		return
	}
	if _, seekErr := audioFile.Seek(0, io.SeekStart); seekErr != nil {
		s.errorResponse(w, http.StatusInternalServerError, "failed to read audio file")
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filepath.Base(absPath), info.ModTime(), audioFile)
}

func (s *Server) handleDeleteMeetingArtifacts(w http.ResponseWriter, r *http.Request) {
	var req deleteMeetingArtifactsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	notesPath := strings.TrimSpace(req.NotesPath)
	if notesPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "notes_path is required")
		return
	}

	absNotesPath, err := filepath.Abs(notesPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "invalid notes_path")
		return
	}

	notesInfo, err := os.Stat(absNotesPath)
	if err != nil {
		if os.IsNotExist(err) {
			s.errorResponse(w, http.StatusNotFound, "meeting note not found")
			return
		}
		s.errorResponse(w, http.StatusInternalServerError, "failed to access meeting note")
		return
	}
	if notesInfo.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "notes_path points to a directory")
		return
	}

	deletedAudio := make([]string, 0, len(req.AudioPath))
	seenAudio := make(map[string]struct{})
	for _, candidate := range req.AudioPath {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		absAudioPath, absErr := filepath.Abs(candidate)
		if absErr != nil {
			continue
		}
		if _, seen := seenAudio[absAudioPath]; seen {
			continue
		}
		seenAudio[absAudioPath] = struct{}{}

		audioInfo, statErr := os.Stat(absAudioPath)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			s.errorResponse(w, http.StatusInternalServerError, "failed to access meeting audio")
			return
		}
		if audioInfo.IsDir() {
			continue
		}
		if removeErr := os.Remove(absAudioPath); removeErr != nil && !os.IsNotExist(removeErr) {
			s.errorResponse(w, http.StatusInternalServerError, "failed to delete meeting audio")
			return
		}
		deletedAudio = append(deletedAudio, absAudioPath)
	}

	if err := os.Remove(absNotesPath); err != nil {
		if os.IsNotExist(err) {
			s.errorResponse(w, http.StatusNotFound, "meeting note not found")
			return
		}
		s.errorResponse(w, http.StatusInternalServerError, "failed to delete meeting note")
		return
	}

	s.jsonResponse(w, http.StatusOK, deleteMeetingArtifactsResponse{
		DeletedNotesPath: absNotesPath,
		DeletedAudioPath: deletedAudio,
	})
}

func (s *Server) resolveMeetingStorageFolder(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("folder path is required")
	}

	cleaned := filepath.Clean(trimmed)
	if !filepath.IsAbs(cleaned) {
		base := strings.TrimSpace(s.config.WorkDir)
		if base == "" {
			base = "."
		}
		cleaned = filepath.Clean(filepath.Join(base, cleaned))
	}
	return cleaned, nil
}

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
