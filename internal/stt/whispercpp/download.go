package whispercpp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	modelDownloadBaseURL   = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/"
	maxModelDownloadBytes  = 4 * 1024 * 1024 * 1024
	defaultDownloadTimeout = 30 * time.Minute
)

func ensureModelDownloaded(modelName string) (string, error) {
	dataDir := resolveDataDir()
	modelsDir := filepath.Join(dataDir, "speech", "whisper", "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create whisper model directory: %w", err)
	}
	path := filepath.Join(modelsDir, modelName)
	if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Size() > 0 {
		return path, nil
	}

	tmpPath := path + ".download"
	if err := downloadFileLimited(modelDownloadBaseURL+modelName, tmpPath, maxModelDownloadBytes); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to auto-download whisper model: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to finalize whisper model download: %w", err)
	}
	return path, nil
}

func downloadFileLimited(url string, destination string, maxBytes int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultDownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: defaultDownloadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected HTTP status: %s", resp.Status)
	}

	f, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer f.Close()

	limited := io.LimitReader(resp.Body, maxBytes+1)
	n, err := io.Copy(f, limited)
	if err != nil {
		return err
	}
	if n > maxBytes {
		return errors.New("download exceeded size limit")
	}
	return nil
}
