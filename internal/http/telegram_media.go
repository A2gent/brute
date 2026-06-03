package http

// Telegram media transport helpers for audio, photos, downloads, and normalization.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

func (s *Server) telegramInboundAudioMetadataForMessage(audioPath string, mediaKind string) (map[string]interface{}, error) {
	if s.speechClips == nil {
		return nil, fmt.Errorf("speech clip cache is unavailable")
	}
	audio, err := os.ReadFile(audioPath)
	if err != nil {
		return nil, fmt.Errorf("failed reading inbound audio: %w", err)
	}
	if len(audio) == 0 {
		return nil, fmt.Errorf("inbound audio payload is empty")
	}
	contentType := strings.TrimSpace(http.DetectContentType(audio))
	if contentType == "" || !strings.HasPrefix(contentType, "audio/") {
		contentType = "audio/wav"
	}
	clipID := s.speechClips.Save(contentType, audio)
	if clipID == "" {
		return nil, fmt.Errorf("failed to cache inbound audio clip")
	}
	return map[string]interface{}{
		"inbound_audio_clip": map[string]interface{}{
			"clip_id":      clipID,
			"content_type": contentType,
			"source":       "telegram",
			"kind":         strings.TrimSpace(mediaKind),
		},
	}, nil
}

func (s *Server) sendTelegramAudio(
	ctx context.Context,
	botToken string,
	chatID string,
	threadID int64,
	audio []byte,
	filename string,
	caption string,
) error {
	if len(audio) == 0 {
		return fmt.Errorf("audio payload is empty")
	}
	if strings.TrimSpace(filename) == "" {
		filename = "reply.wav"
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", chatID)
	if threadID > 0 {
		_ = writer.WriteField("message_thread_id", strconv.FormatInt(threadID, 10))
	}
	caption = strings.TrimSpace(caption)
	if caption != "" {
		_ = writer.WriteField("caption", truncateRunes(caption, telegramMaxCaptionRunes))
	}
	part, err := writer.CreateFormFile("audio", filename)
	if err != nil {
		return fmt.Errorf("failed to create telegram audio multipart field: %w", err)
	}
	if _, err := part.Write(audio); err != nil {
		return fmt.Errorf("failed to write telegram audio payload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to finalize telegram audio multipart payload: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf("https://api.telegram.org/bot%s/sendAudio", botToken),
		&body,
	)
	if err != nil {
		return fmt.Errorf("failed to build sendAudio request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sendAudio request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode sendAudio response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !result.OK {
		msg := strings.TrimSpace(result.Description)
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("telegram sendAudio failed: %s", msg)
	}
	return nil
}

func (s *Server) downloadTelegramFile(
	ctx context.Context,
	botToken string,
	fileID string,
	maxBytes int64,
	payloadKind string,
) (string, func(), error) {
	if strings.TrimSpace(botToken) == "" {
		return "", func() {}, fmt.Errorf("missing bot token")
	}
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return "", func() {}, fmt.Errorf("missing file id")
	}
	if maxBytes <= 0 {
		maxBytes = telegramMaxInboundAudioBytes
	}
	payloadKind = strings.TrimSpace(payloadKind)
	if payloadKind == "" {
		payloadKind = "file"
	}

	getFileURL := fmt.Sprintf(
		"https://api.telegram.org/bot%s/getFile?file_id=%s",
		botToken,
		url.QueryEscape(fileID),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getFileURL, nil)
	if err != nil {
		return "", func() {}, fmt.Errorf("failed to build getFile request: %w", err)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", func() {}, fmt.Errorf("getFile request failed: %w", err)
	}
	defer resp.Body.Close()

	var getFile telegramGetFilePayload
	if err := json.NewDecoder(resp.Body).Decode(&getFile); err != nil {
		return "", func() {}, fmt.Errorf("failed to decode getFile response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !getFile.OK {
		msg := strings.TrimSpace(getFile.Description)
		if msg == "" {
			msg = resp.Status
		}
		return "", func() {}, fmt.Errorf("telegram getFile failed: %s", msg)
	}

	filePath := strings.TrimSpace(getFile.Result.FilePath)
	if filePath == "" {
		return "", func() {}, fmt.Errorf("telegram getFile returned empty file_path")
	}
	downloadURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", botToken, strings.TrimLeft(filePath, "/"))

	downloadReq, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", func() {}, fmt.Errorf("failed to build file download request: %w", err)
	}
	downloadResp, err := client.Do(downloadReq)
	if err != nil {
		return "", func() {}, fmt.Errorf("file download request failed: %w", err)
	}
	defer downloadResp.Body.Close()
	if downloadResp.StatusCode < http.StatusOK || downloadResp.StatusCode >= http.StatusMultipleChoices {
		return "", func() {}, fmt.Errorf("telegram file download failed: %s", downloadResp.Status)
	}

	limited := io.LimitReader(downloadResp.Body, maxBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return "", func() {}, fmt.Errorf("failed to read downloaded file: %w", err)
	}
	if len(payload) == 0 {
		return "", func() {}, fmt.Errorf("downloaded file is empty")
	}
	if int64(len(payload)) > maxBytes {
		return "", func() {}, fmt.Errorf("downloaded %s exceeds %d bytes", payloadKind, maxBytes)
	}

	ext := filepath.Ext(filePath)
	if ext == "" {
		ext = "." + payloadKind
	}
	tmp, err := os.CreateTemp("", "a2gent-telegram-"+payloadKind+"-*"+ext)
	if err != nil {
		return "", func() {}, fmt.Errorf("failed to create temporary %s file: %w", payloadKind, err)
	}
	cleanup := func() {
		_ = os.Remove(tmp.Name())
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("failed to write downloaded %s file: %w", payloadKind, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("failed to close downloaded %s file: %w", payloadKind, err)
	}
	return tmp.Name(), cleanup, nil
}

func (s *Server) downloadTelegramPhotoAttachment(
	ctx context.Context,
	botToken string,
	fileID string,
	messageID int,
) (session.ImageAttachment, error) {
	path, cleanup, err := s.downloadTelegramFile(ctx, botToken, fileID, telegramMaxInboundImageBytes, "photo")
	if err != nil {
		return session.ImageAttachment{}, err
	}
	defer cleanup()

	payload, err := os.ReadFile(path)
	if err != nil {
		return session.ImageAttachment{}, fmt.Errorf("failed to read downloaded photo: %w", err)
	}
	if len(payload) == 0 {
		return session.ImageAttachment{}, fmt.Errorf("downloaded photo is empty")
	}
	mediaType := strings.TrimSpace(http.DetectContentType(payload))
	if !strings.HasPrefix(strings.ToLower(mediaType), "image/") {
		mediaType = "image/jpeg"
	}
	name := fmt.Sprintf("telegram-%d", messageID)
	if ext := strings.TrimSpace(filepath.Ext(path)); ext != "" {
		name += ext
	}
	return session.ImageAttachment{
		Name:       name,
		MediaType:  mediaType,
		DataBase64: base64.StdEncoding.EncodeToString(payload),
	}, nil
}

func convertAudioToWAVForWhisper(ctx context.Context, inputPath string) (string, func(), error) {
	inputPath = strings.TrimSpace(inputPath)
	if inputPath == "" {
		return "", nil, fmt.Errorf("input audio path is empty")
	}

	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(inputPath)))
	if ext == ".wav" || ext == ".wave" {
		return inputPath, nil, nil
	}

	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		logging.Warn("ffmpeg not found in PATH; passing original audio file to whisper: %s", inputPath)
		return inputPath, nil, nil
	}

	tmp, err := os.CreateTemp("", "a2gent-telegram-audio-*.wav")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temporary wav file: %w", err)
	}
	outputPath := tmp.Name()
	_ = tmp.Close()
	cleanup := func() { _ = os.Remove(outputPath) }

	cmd := exec.CommandContext(
		ctx,
		ffmpegPath,
		"-y",
		"-i", inputPath,
		"-ac", "1",
		"-ar", "16000",
		"-f", "wav",
		outputPath,
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(output.String())
		if detail == "" {
			detail = err.Error()
		}
		cleanup()
		return "", nil, fmt.Errorf("ffmpeg conversion failed: %s", truncateRunes(detail, 1000))
	}

	if info, statErr := os.Stat(outputPath); statErr != nil || info.IsDir() || info.Size() == 0 {
		cleanup()
		if statErr != nil {
			return "", nil, fmt.Errorf("ffmpeg conversion produced invalid output: %v", statErr)
		}
		return "", nil, fmt.Errorf("ffmpeg conversion produced empty output")
	}

	return outputPath, cleanup, nil
}

func (s *Server) sendTelegramImagesForSessionMessage(
	ctx context.Context,
	botToken string,
	chatID string,
	threadID int64,
	msg session.Message,
) error {
	if len(msg.Images) == 0 {
		return nil
	}
	for _, img := range msg.Images {
		if err := s.sendTelegramPhoto(ctx, botToken, chatID, threadID, img); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) sendTelegramPhoto(
	ctx context.Context,
	botToken string,
	chatID string,
	threadID int64,
	img session.ImageAttachment,
) error {
	if strings.TrimSpace(img.DataBase64) != "" {
		return s.sendTelegramPhotoBytes(ctx, botToken, chatID, threadID, strings.TrimSpace(img.Name), strings.TrimSpace(img.DataBase64))
	}
	rawURL := strings.TrimSpace(img.URL)
	if rawURL == "" {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(rawURL), "data:") {
		decoded, err := decodeImageDataURI(rawURL)
		if err != nil {
			return err
		}
		return s.sendTelegramPhotoBytes(ctx, botToken, chatID, threadID, strings.TrimSpace(img.Name), decoded)
	}
	return s.sendTelegramPhotoByURL(ctx, botToken, chatID, threadID, rawURL)
}

func decodeImageDataURI(raw string) (string, error) {
	const marker = ";base64,"
	idx := strings.Index(strings.ToLower(raw), marker)
	if idx < 0 {
		return "", fmt.Errorf("unsupported data URI image encoding")
	}
	encoded := strings.TrimSpace(raw[idx+len(marker):])
	if encoded == "" {
		return "", fmt.Errorf("empty data URI image payload")
	}
	return encoded, nil
}

func (s *Server) sendTelegramPhotoByURL(
	ctx context.Context,
	botToken string,
	chatID string,
	threadID int64,
	photoURL string,
) error {
	payload := map[string]interface{}{
		"chat_id": chatID,
		"photo":   photoURL,
	}
	if threadID > 0 {
		payload["message_thread_id"] = threadID
	}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to encode sendPhoto payload: %w", err)
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", botToken),
		bytes.NewReader(jsonBody),
	)
	if err != nil {
		return fmt.Errorf("failed to build sendPhoto request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sendPhoto request failed: %w", err)
	}
	defer resp.Body.Close()

	var result telegramBasicResponsePayload
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode sendPhoto response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !result.OK {
		msg := strings.TrimSpace(result.Description)
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("telegram sendPhoto failed: %s", msg)
	}
	return nil
}

func (s *Server) sendTelegramPhotoBytes(
	ctx context.Context,
	botToken string,
	chatID string,
	threadID int64,
	name string,
	dataBase64 string,
) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(dataBase64))
	if err != nil {
		return fmt.Errorf("invalid photo base64 payload: %w", err)
	}
	if len(raw) == 0 {
		return fmt.Errorf("empty photo payload")
	}
	if name == "" {
		name = "image.jpg"
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", chatID)
	if threadID > 0 {
		_ = writer.WriteField("message_thread_id", strconv.FormatInt(threadID, 10))
	}
	part, err := writer.CreateFormFile("photo", name)
	if err != nil {
		return fmt.Errorf("failed to create telegram photo multipart field: %w", err)
	}
	if _, err := part.Write(raw); err != nil {
		return fmt.Errorf("failed to write telegram photo payload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to finalize telegram photo multipart payload: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", botToken),
		&body,
	)
	if err != nil {
		return fmt.Errorf("failed to build sendPhoto request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sendPhoto request failed: %w", err)
	}
	defer resp.Body.Close()

	var result telegramBasicResponsePayload
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode sendPhoto response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !result.OK {
		msg := strings.TrimSpace(result.Description)
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("telegram sendPhoto failed: %s", msg)
	}
	return nil
}
