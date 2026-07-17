package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
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
	SummaryMarkdown    string   `json:"summary_markdown"`
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

type renameMeetingArtifactsRequest struct {
	NotesPath string `json:"notes_path"`
	Title     string `json:"title"`
}

type renameMeetingArtifactsResponse struct {
	Meeting meetingHistoryItem `json:"meeting"`
}

type processMeetingRequest struct {
	NotesPath string `json:"notes_path"`
}

type processMeetingResponse struct {
	Meeting meetingHistoryItem `json:"meeting"`
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

	// Summary generation is best-effort so provider errors never discard the saved
	// recording, but completing it before the response lets the immediate history
	// refresh include the generated summary.
	summaryCtx, summaryCancel := context.WithTimeout(r.Context(), defaultMeetingProcessingTimeout)
	if _, err := s.summarizeMeetingNote(summaryCtx, notesPath); err != nil {
		logging.Warn("Failed to automatically summarize meeting %s: %v", notesPath, err)
	}
	summaryCancel()

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

	notePaths, err := discoverMeetingMarkdownFiles(notesFolder)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to read notes folder: "+err.Error())
		return
	}

	meetings := make([]meetingHistoryItem, 0)
	for _, notesPath := range notePaths {
		contentBytes, readErr := os.ReadFile(notesPath)
		if readErr != nil {
			continue
		}
		content := string(contentBytes)
		if !isGeneratedMeetingMarkdown(content) {
			continue
		}

		baseName := strings.TrimSuffix(filepath.Base(notesPath), filepath.Ext(notesPath))
		item := parseMeetingHistoryFromMarkdown(content)
		if item.Title == "" {
			item.Title = baseName
		}
		item.NotesPath = notesPath
		if len(item.AudioPaths) == 0 {
			_, body := parseMeetingFrontmatter(content)
			item.AudioPaths = extractAudioLinksFromAudioSection(body)
		}
		if len(item.AudioPaths) == 0 {
			item.AudioPaths = discoverMeetingAudioByBaseName(audioFolder, baseName)
		}

		info, infoErr := os.Stat(notesPath)
		if infoErr == nil {
			item.UpdatedAt = info.ModTime().Format(time.RFC3339)
			if item.StartedAt == "" {
				if startedAt := parseStartedAtFromBaseName(baseName, info); startedAt != "" {
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

func (s *Server) handleRenameMeetingArtifacts(w http.ResponseWriter, r *http.Request) {
	var req renameMeetingArtifactsRequest
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

	noteContentBytes, readErr := os.ReadFile(absNotesPath)
	if readErr != nil {
		s.errorResponse(w, http.StatusInternalServerError, "failed to read meeting note")
		return
	}

	updatedMarkdown, updateErr := updateMeetingTitleInMarkdown(string(noteContentBytes), req.Title, absNotesPath)
	if updateErr != nil {
		s.errorResponse(w, http.StatusBadRequest, updateErr.Error())
		return
	}

	if err := os.WriteFile(absNotesPath, []byte(strings.TrimSpace(updatedMarkdown)+"\n"), 0o644); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "failed to update meeting note")
		return
	}

	item := parseMeetingHistoryFromMarkdown(updatedMarkdown)
	item.NotesPath = absNotesPath
	item.UpdatedAt = time.Now().Format(time.RFC3339)

	s.jsonResponse(w, http.StatusOK, renameMeetingArtifactsResponse{Meeting: item})
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

	noteContentBytes, readErr := os.ReadFile(absNotesPath)
	if readErr != nil {
		s.errorResponse(w, http.StatusInternalServerError, "failed to read meeting note")
		return
	}
	noteContent := string(noteContentBytes)
	parsedMeeting := parseMeetingHistoryFromMarkdown(noteContent)
	_, noteBody := parseMeetingFrontmatter(noteContent)

	audioCandidates := make([]string, 0, len(req.AudioPath)+len(parsedMeeting.AudioPaths))
	audioCandidates = append(audioCandidates, req.AudioPath...)
	audioCandidates = append(audioCandidates, parsedMeeting.AudioPaths...)
	audioCandidates = append(audioCandidates, extractAudioLinksFromAudioSection(noteBody)...)
	baseName := strings.TrimSuffix(filepath.Base(absNotesPath), filepath.Ext(absNotesPath))
	seenAudioDirs := make(map[string]struct{})
	for _, candidate := range audioCandidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		absAudioPath, absErr := filepath.Abs(candidate)
		if absErr != nil {
			continue
		}
		dir := filepath.Dir(absAudioPath)
		if _, seen := seenAudioDirs[dir]; seen {
			continue
		}
		seenAudioDirs[dir] = struct{}{}
		audioCandidates = append(audioCandidates, discoverMeetingAudioByBaseName(dir, baseName)...)
	}

	deletedAudio := make([]string, 0, len(audioCandidates))
	seenAudio := make(map[string]struct{})
	for _, candidate := range audioCandidates {
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

func (s *Server) handleSummarizeMeeting(w http.ResponseWriter, r *http.Request) {
	var req processMeetingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), defaultMeetingProcessingTimeout)
	defer cancel()
	meeting, err := s.summarizeMeetingNote(ctx, req.NotesPath)
	if err != nil {
		s.errorResponse(w, meetingProcessingStatus(err), err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, processMeetingResponse{Meeting: meeting})
}

func (s *Server) handleRetranscribeMeeting(w http.ResponseWriter, r *http.Request) {
	var req processMeetingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), defaultMeetingProcessingTimeout)
	defer cancel()
	meeting, err := s.retranscribeMeetingNote(ctx, req.NotesPath)
	if err != nil {
		s.errorResponse(w, meetingProcessingStatus(err), err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, processMeetingResponse{Meeting: meeting})
}

func (s *Server) summarizeMeetingNote(ctx context.Context, notesPath string) (meetingHistoryItem, error) {
	absPath, content, meeting, err := readGeneratedMeetingNote(notesPath)
	if err != nil {
		return meetingHistoryItem{}, err
	}
	summary, err := s.generateMeetingSummary(ctx, meeting)
	if err != nil {
		return meetingHistoryItem{}, err
	}
	updated, err := updateMeetingSummaryInMarkdown(content, summary)
	if err != nil {
		return meetingHistoryItem{}, err
	}
	return writeProcessedMeetingNote(absPath, updated)
}

func (s *Server) retranscribeMeetingNote(ctx context.Context, notesPath string) (meetingHistoryItem, error) {
	absPath, content, meeting, err := readGeneratedMeetingNote(notesPath)
	if err != nil {
		return meetingHistoryItem{}, err
	}
	transcript, err := s.transcribeMeetingAudio(ctx, meeting)
	if err != nil {
		return meetingHistoryItem{}, err
	}
	updated, err := updateMeetingTranscriptInMarkdown(content, transcript)
	if err != nil {
		return meetingHistoryItem{}, err
	}
	meeting = parseMeetingHistoryFromMarkdown(updated)
	summary, err := s.generateMeetingSummary(ctx, meeting)
	if err != nil {
		return meetingHistoryItem{}, err
	}
	updated, err = updateMeetingSummaryInMarkdown(updated, summary)
	if err != nil {
		return meetingHistoryItem{}, err
	}
	return writeProcessedMeetingNote(absPath, updated)
}

func readGeneratedMeetingNote(notesPath string) (string, string, meetingHistoryItem, error) {
	trimmed := strings.TrimSpace(notesPath)
	if trimmed == "" {
		return "", "", meetingHistoryItem{}, fmt.Errorf("notes_path is required")
	}
	absPath, err := filepath.Abs(trimmed)
	if err != nil {
		return "", "", meetingHistoryItem{}, fmt.Errorf("invalid notes_path")
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", meetingHistoryItem{}, fmt.Errorf("meeting note not found")
		}
		return "", "", meetingHistoryItem{}, fmt.Errorf("failed to access meeting note")
	}
	if info.IsDir() {
		return "", "", meetingHistoryItem{}, fmt.Errorf("notes_path points to a directory")
	}
	payload, err := os.ReadFile(absPath)
	if err != nil {
		return "", "", meetingHistoryItem{}, fmt.Errorf("failed to read meeting note")
	}
	content := string(payload)
	if !isGeneratedMeetingMarkdown(content) {
		return "", "", meetingHistoryItem{}, fmt.Errorf("not a generated meeting note")
	}
	meeting := parseMeetingHistoryFromMarkdown(content)
	meeting.NotesPath = absPath
	return absPath, content, meeting, nil
}

func writeProcessedMeetingNote(notesPath, content string) (meetingHistoryItem, error) {
	if err := os.WriteFile(notesPath, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		return meetingHistoryItem{}, fmt.Errorf("failed to update meeting note")
	}
	meeting := parseMeetingHistoryFromMarkdown(content)
	meeting.NotesPath = notesPath
	meeting.UpdatedAt = time.Now().Format(time.RFC3339)
	return meeting, nil
}

func meetingProcessingStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "not found"):
		return http.StatusNotFound
	case strings.Contains(message, "required"), strings.Contains(message, "invalid"), strings.Contains(message, "empty"), strings.Contains(message, "no audio"), strings.Contains(message, "not a generated"):
		return http.StatusBadRequest
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return http.StatusGatewayTimeout
	default:
		return http.StatusBadGateway
	}
}
