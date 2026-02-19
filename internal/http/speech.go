package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/A2gent/brute/internal/stt/whispercpp"
)

const (
	elevenLabsVoicesURL      = "https://api.elevenlabs.io/v1/voices"
	maxTranscribeAudioBytes  = 25 * 1024 * 1024
	defaultTranscribeTimeout = 20 * time.Minute
)

var recommendedPiperVoices = []string{
	"en_US-lessac-medium",
	"en_US-ryan-high",
	"en_GB-alan-medium",
	"de_DE-thorsten-medium",
	"fr_FR-siwis-medium",
	"es_ES-sharvard-medium",
	"it_IT-riccardo-x_low",
	"pt_BR-faber-medium",
	"ru_RU-ruslan-medium",
	"uk_UA-lada-x_low",
	"ja_JP-kokoro-medium",
	"zh_CN-huayan-medium",
}

type speechCompletionRequest struct {
	Text string `json:"text"`
}

type speechTranscribeResponse struct {
	Text string `json:"text"`
}

type elevenLabsVoice struct {
	VoiceID    string `json:"voice_id"`
	Name       string `json:"name"`
	PreviewURL string `json:"preview_url,omitempty"`
}

type elevenLabsVoicesResponse struct {
	Voices []elevenLabsVoice `json:"voices"`
}

type elevenLabsTTSRequest struct {
	Text          string                  `json:"text"`
	ModelID       string                  `json:"model_id"`
	VoiceSettings elevenLabsVoiceSettings `json:"voice_settings,omitempty"`
}

type elevenLabsVoiceSettings struct {
	Speed float64 `json:"speed,omitempty"`
}

type piperVoiceOption struct {
	ID        string `json:"id"`
	Installed bool   `json:"installed"`
	ModelPath string `json:"model_path,omitempty"`
}

func (s *Server) handleListSpeechVoices(w http.ResponseWriter, r *http.Request) {
	apiKey := s.resolveElevenLabsAPIKey()
	if apiKey == "" {
		s.errorResponse(w, http.StatusBadRequest, "ElevenLabs API key is not configured. Add an enabled ElevenLabs integration in Integrations.")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, elevenLabsVoicesURL, nil)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to build ElevenLabs request: "+err.Error())
		return
	}
	req.Header.Set("xi-api-key", apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Failed to fetch ElevenLabs voices: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.proxyElevenLabsError(w, resp, "Failed to fetch ElevenLabs voices")
		return
	}

	var payload elevenLabsVoicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Failed to decode ElevenLabs voices response: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, payload.Voices)
}

func (s *Server) handleListPiperVoices(w http.ResponseWriter, r *http.Request) {
	dataDir := resolveAAgentDataDirForHTTP()
	modelsDir := filepath.Join(dataDir, "tts", "piper", "models")

	installed := map[string]string{}
	_ = filepath.WalkDir(modelsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".onnx") {
			return nil
		}
		id := strings.TrimSuffix(d.Name(), ".onnx")
		id = strings.TrimSpace(id)
		if id == "" {
			return nil
		}
		installed[id] = filepath.Clean(path)
		return nil
	})

	seen := map[string]struct{}{}
	out := make([]piperVoiceOption, 0, len(recommendedPiperVoices)+len(installed))

	for _, id := range recommendedPiperVoices {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		_, already := seen[trimmed]
		if already {
			continue
		}
		modelPath, ok := installed[trimmed]
		out = append(out, piperVoiceOption{
			ID:        trimmed,
			Installed: ok,
			ModelPath: modelPath,
		})
		seen[trimmed] = struct{}{}
	}

	installedIDs := make([]string, 0, len(installed))
	for id := range installed {
		if _, ok := seen[id]; ok {
			continue
		}
		installedIDs = append(installedIDs, id)
	}
	sort.Strings(installedIDs)
	for _, id := range installedIDs {
		out = append(out, piperVoiceOption{
			ID:        id,
			Installed: true,
			ModelPath: installed[id],
		})
	}

	s.jsonResponse(w, http.StatusOK, out)
}

func (s *Server) handleCompletionSpeech(w http.ResponseWriter, r *http.Request) {
	apiKey := s.resolveElevenLabsAPIKey()
	voiceID := strings.TrimSpace(os.Getenv("ELEVENLABS_VOICE_ID"))
	if apiKey == "" {
		s.errorResponse(w, http.StatusBadRequest, "ElevenLabs API key is not configured. Add an enabled ElevenLabs integration in Integrations.")
		return
	}
	if voiceID == "" {
		s.errorResponse(w, http.StatusBadRequest, "ELEVENLABS_VOICE_ID is not configured")
		return
	}

	var reqBody speechCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	text := strings.TrimSpace(reqBody.Text)
	if text == "" {
		s.errorResponse(w, http.StatusBadRequest, "text is required")
		return
	}

	ttsReq := elevenLabsTTSRequest{
		Text:    text,
		ModelID: "eleven_multilingual_v2",
	}
	if speedRaw := strings.TrimSpace(os.Getenv("ELEVENLABS_SPEED")); speedRaw != "" {
		if speed, err := strconv.ParseFloat(speedRaw, 64); err == nil && speed > 0 {
			ttsReq.VoiceSettings = elevenLabsVoiceSettings{Speed: speed}
		}
	}

	jsonBody, err := json.Marshal(ttsReq)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to build ElevenLabs request payload: "+err.Error())
		return
	}

	ttsURL := fmt.Sprintf("https://api.elevenlabs.io/v1/text-to-speech/%s", url.PathEscape(voiceID))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, ttsURL, bytes.NewReader(jsonBody))
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to build ElevenLabs request: "+err.Error())
		return
	}
	req.Header.Set("xi-api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, "Failed to call ElevenLabs: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.proxyElevenLabsError(w, resp, "ElevenLabs playback failed")
		return
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "audio/mpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, resp.Body); err != nil {
		// Client may disconnect mid-stream; nothing actionable for handler.
		return
	}
}

func (s *Server) handleGetSpeechClip(w http.ResponseWriter, r *http.Request) {
	clipID := strings.TrimSpace(chi.URLParam(r, "clipID"))
	if clipID == "" {
		s.errorResponse(w, http.StatusBadRequest, "clipID is required")
		return
	}
	if s.speechClips == nil {
		s.errorResponse(w, http.StatusNotFound, "Speech clip store is unavailable")
		return
	}

	contentType, payload, ok := s.speechClips.Load(clipID)
	if !ok {
		s.errorResponse(w, http.StatusNotFound, "Speech clip not found or expired")
		return
	}
	if contentType == "" {
		contentType = "audio/mpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(payload); err != nil {
		return
	}
}

func (s *Server) handleTranscribeSpeech(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), defaultTranscribeTimeout)
	defer cancel()

	if err := r.ParseMultipartForm(maxTranscribeAudioBytes); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid multipart request: "+err.Error())
		return
	}

	audioFile, _, err := r.FormFile("audio")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "audio form field is required")
		return
	}
	defer audioFile.Close()

	limited := io.LimitReader(audioFile, maxTranscribeAudioBytes+1)
	audioPayload, err := io.ReadAll(limited)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read audio payload: "+err.Error())
		return
	}
	if len(audioPayload) == 0 {
		s.errorResponse(w, http.StatusBadRequest, "audio payload is empty")
		return
	}
	if len(audioPayload) > maxTranscribeAudioBytes {
		s.errorResponse(w, http.StatusRequestEntityTooLarge, "audio payload exceeds 25MB limit")
		return
	}

	audioPath, cleanup, err := writeTempWAV(audioPayload)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to persist audio payload: "+err.Error())
		return
	}
	defer cleanup()

	transcript, err := whispercpp.TranscribeWithOptions(ctx, audioPath, r.FormValue("language"), parseOptionalBool(r.FormValue("translate_to_english")))
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			status = http.StatusGatewayTimeout
		}
		s.errorResponse(w, status, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, speechTranscribeResponse{Text: transcript})
}

func parseOptionalBool(raw string) *bool {
	value := strings.TrimSpace(strings.ToLower(raw))
	switch value {
	case "":
		return nil
	case "1", "true", "yes", "on":
		b := true
		return &b
	case "0", "false", "no", "off":
		b := false
		return &b
	default:
		return nil
	}
}

func writeTempWAV(payload []byte) (string, func(), error) {
	tmp, err := os.CreateTemp("", "aagent-stt-*.wav")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() {
		_ = os.Remove(tmp.Name())
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return tmp.Name(), cleanup, nil
}

func (s *Server) resolveElevenLabsAPIKey() string {
	integrations, err := s.store.ListIntegrations()
	if err == nil {
		for _, integration := range integrations {
			if integration == nil || !integration.Enabled || integration.Provider != "elevenlabs" {
				continue
			}
			apiKey := strings.TrimSpace(integration.Config["api_key"])
			if apiKey != "" {
				return apiKey
			}
		}
	}

	return strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY"))
}

func (s *Server) proxyElevenLabsError(w http.ResponseWriter, upstream *http.Response, fallback string) {
	body, _ := io.ReadAll(io.LimitReader(upstream.Body, 8192))
	statusDetail := strings.TrimSpace(string(body))
	if statusDetail == "" {
		statusDetail = upstream.Status
	}
	s.errorResponse(w, upstream.StatusCode, fmt.Sprintf("%s: %s", fallback, statusDetail))
}

func resolveAAgentDataDirForHTTP() string {
	if raw := strings.TrimSpace(os.Getenv("AAGENT_DATA_PATH")); raw != "" {
		return filepath.Clean(raw)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return filepath.Clean(filepath.Join(".", ".aagent-data"))
	}
	return filepath.Join(homeDir, ".local", "share", "aagent")
}
