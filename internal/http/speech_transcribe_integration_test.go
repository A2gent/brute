package http

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSpeechTranscribeEndpointReturnsStructuredErrorWhenSTTUnavailable(t *testing.T) {
	t.Setenv("AAGENT_DATA_PATH", t.TempDir())
	t.Setenv("AAGENT_WHISPER_AUTO_SETUP", "0")
	t.Setenv("AAGENT_WHISPER_AUTO_DOWNLOAD", "0")
	t.Setenv("AAGENT_WHISPER_BIN", "")
	t.Setenv("AAGENT_WHISPER_MODEL", "")

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("audio", "recording.wav")
	if err != nil {
		t.Fatalf("CreateFormFile failed: %v", err)
	}
	if _, err := part.Write([]byte("fake-wav")); err != nil {
		t.Fatalf("writing multipart audio failed: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("closing multipart writer failed: %v", err)
	}

	s := &Server{}
	s.setupRoutes()

	req := httptest.NewRequest(http.MethodPost, "/speech/transcribe", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusBadGateway, rec.Code, rec.Body.String())
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed decoding error response: %v body=%s", err, rec.Body.String())
	}
	msg, _ := payload["error"].(string)
	if !strings.Contains(strings.ToLower(msg), "model not found") {
		t.Fatalf("expected model-not-found error, got: %q", msg)
	}
}
