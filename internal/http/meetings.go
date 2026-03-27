package http

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

const (
	maxMeetingMultipartMemory = 8 << 20
)

type saveMeetingArtifactsResponse struct {
	MeetingID string   `json:"meeting_id"`
	NotesPath string   `json:"notes_path"`
	AudioPath []string `json:"audio_paths"`
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
	if err := os.WriteFile(notesPath, []byte(notesMarkdown+"\n"), 0o644); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to write meeting notes: "+err.Error())
		return
	}

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

	s.jsonResponse(w, http.StatusOK, saveMeetingArtifactsResponse{
		MeetingID: meetingID,
		NotesPath: notesPath,
		AudioPath: audioPaths,
	})
	_ = endedAtRaw // reserved for future metadata enhancements
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
