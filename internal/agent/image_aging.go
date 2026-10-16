package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

// Tool-call steps are not completed answers: a screenshot must remain available
// while the model is still acting on it, but not throughout a long text dialogue.
const imageAnswerLimit = 2

func imageAnswerAges(messages []session.Message) map[string]int {
	ages := make(map[string]int, len(messages))
	answers := 0
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		ages[m.ID] = answers
		if m.Role == "assistant" && len(m.ToolCalls) == 0 && (strings.TrimSpace(m.Content) != "" || len(m.Images) > 0) {
			answers++
		}
	}
	return ages
}

// Save originals independently of the outgoing context. Content-addressed names
// are stable across reloads and cannot escape the session's images directory.
func (a *Agent) saveContextImage(sess *session.Session, img llm.Image) string {
	if a.sessionManager == nil {
		return ""
	}
	logPath := a.sessionManager.JSONLPath(sess.ID)
	if logPath == "" {
		return ""
	}
	logPath, err := filepath.Abs(logPath)
	if err != nil {
		return ""
	}
	data := strings.TrimSpace(img.DataBase64)
	mediaType := img.MediaType
	if data == "" {
		if header, payload, ok := strings.Cut(img.URL, ";base64,"); ok && strings.HasPrefix(header, "data:image/") {
			data, mediaType = payload, strings.TrimPrefix(header, "data:")
		}
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(raw) == 0 {
		return ""
	}
	ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[mediaType]
	if ext == "" {
		ext = ".png"
	}
	sum := sha256.Sum256(raw)
	dir := filepath.Join(strings.TrimSuffix(logPath, ".jsonl"), "images")
	path := filepath.Join(dir, fmt.Sprintf("%x%s", sum, ext))
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		logging.Debug("Image retention: cannot create session image directory: %v", err)
		return ""
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		logging.Debug("Image retention: cannot save session image: %v", err)
		return ""
	}
	return path
}

func (a *Agent) imageOmission(sess *session.Session, img llm.Image, fallback string) string {
	path := a.saveContextImage(sess, img)
	if path == "" {
		return fallback
	}
	name := strings.TrimSpace(img.Name)
	if name == "" {
		name = "screenshot"
	}
	return fmt.Sprintf("[screenshot omitted from context: %s, saved at %s]", name, path)
}
