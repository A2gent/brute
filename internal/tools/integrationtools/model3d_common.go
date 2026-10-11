package integrationtools

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/storage"
)

// Shared helpers for the async 3D generation providers (Meshy, Tripo). Both
// APIs follow the same shape: create a task, poll until it finishes, then
// download the model file and a preview thumbnail from signed URLs.

const (
	model3DDefaultTimeout      = 10 * time.Minute
	model3DDefaultPollInterval = 5 * time.Second
	model3DMaxDownloadBytes    = 500 * 1024 * 1024
	model3DMaxInputImageBytes  = 20 * 1024 * 1024
)

type model3DIntegration struct {
	APIKey  string
	BaseURL string
	Model   string
	Timeout time.Duration
}

func findModel3DIntegration(store storage.Store, provider string) (*model3DIntegration, error) {
	if store == nil {
		return nil, fmt.Errorf("%s integration is not configured", provider)
	}
	all, err := store.ListIntegrations()
	if err != nil {
		return nil, fmt.Errorf("failed to load integrations: %w", err)
	}
	for _, item := range all {
		if item == nil || item.Provider != provider || !item.Enabled {
			continue
		}
		key := strings.TrimSpace(item.Config["api_key"])
		if key == "" {
			continue
		}
		out := &model3DIntegration{
			APIKey:  key,
			BaseURL: strings.TrimRight(strings.TrimSpace(item.Config["base_url"]), "/"),
			Model:   strings.TrimSpace(item.Config["model"]),
			Timeout: model3DDefaultTimeout,
		}
		if secs, err := strconv.Atoi(strings.TrimSpace(item.Config["timeout_seconds"])); err == nil && secs > 0 {
			out.Timeout = time.Duration(secs) * time.Second
		}
		return out, nil
	}
	return nil, fmt.Errorf("no enabled %s integration with an api_key found; add one in Integrations", provider)
}

// model3DImageInput describes an input image either as a remote URL (passed
// through as-is) or as a local file read from disk for upload/inlining.
type model3DImageInput struct {
	URL      string
	Path     string
	Data     []byte
	MimeType string
}

func resolveModel3DImage(raw, workDir string) (*model3DImageInput, error) {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:") {
		return &model3DImageInput{URL: raw}, nil
	}
	p := raw
	if !filepath.IsAbs(p) && workDir != "" {
		p = filepath.Join(workDir, p)
	}
	info, err := os.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("image not found: %s", raw)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("image path is a directory: %s", raw)
	}
	if info.Size() > model3DMaxInputImageBytes {
		return nil, fmt.Errorf("image is larger than 20 MB: %s", raw)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("failed to read image: %w", err)
	}
	mimeType := http.DetectContentType(data)
	switch mimeType {
	case "image/png", "image/jpeg", "image/webp":
	default:
		return nil, fmt.Errorf("unsupported image type %s (use PNG, JPEG or WebP)", mimeType)
	}
	return &model3DImageInput{Path: p, Data: data, MimeType: mimeType}, nil
}

func (in *model3DImageInput) dataURI() string {
	return "data:" + in.MimeType + ";base64," + base64.StdEncoding.EncodeToString(in.Data)
}

var unsafeFileNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// model3DURLExt returns the lowercase file extension of rawURL's path, or def.
func model3DURLExt(rawURL, def string) string {
	if u, err := url.Parse(rawURL); err == nil {
		if ext := strings.ToLower(path.Ext(u.Path)); ext != "" && len(ext) <= 6 && !unsafeFileNameChars.MatchString(ext) {
			return ext
		}
	}
	return def
}

// downloadModel3DFile saves rawURL into dir as <baseName><ext>. baseName comes
// from remote task IDs, so it is reduced to a safe filename component, and the
// body is written to a temp file first so failures never leave partial models.
func downloadModel3DFile(ctx context.Context, client *http.Client, rawURL, dir, baseName, ext string) (string, error) {
	baseName = strings.Trim(unsafeFileNameChars.ReplaceAllString(baseName, "_"), ".")
	if baseName == "" {
		baseName = "model"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to build download request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download failed with status %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, "."+baseName+"-*.part")
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	written, err := io.Copy(tmp, io.LimitReader(resp.Body, model3DMaxDownloadBytes+1))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}
	if written > model3DMaxDownloadBytes {
		return "", fmt.Errorf("file exceeds %d MB limit", model3DMaxDownloadBytes/(1024*1024))
	}
	target := filepath.Join(dir, baseName+ext)
	if err := os.Rename(tmp.Name(), target); err != nil {
		return "", fmt.Errorf("failed to save file: %w", err)
	}
	return target, nil
}

// pollModel3DTask calls check until it reports done or the context/timeout
// expires. check returns (done, error).
func pollModel3DTask(ctx context.Context, interval time.Duration, check func(context.Context) (bool, error)) error {
	for {
		done, err := check(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for 3D generation: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}

func model3DResultMetadata(sourceTool, provider, taskID, modelPath, thumbnailPath string, files []string) map[string]interface{} {
	metadata := map[string]interface{}{
		"model_3d": map[string]interface{}{
			"provider":    provider,
			"task_id":     taskID,
			"path":        modelPath,
			"files":       files,
			"thumbnail":   thumbnailPath,
			"source_tool": sourceTool,
		},
	}
	if thumbnailPath != "" {
		// Reuse the image_file contract so Caesar previews the rendered thumbnail.
		metadata["image_file"] = map[string]interface{}{
			"path":        thumbnailPath,
			"source_tool": sourceTool,
		}
	}
	return metadata
}

func model3DResultText(provider, taskID, modelPath, thumbnailPath string, files []string) string {
	lines := []string{fmt.Sprintf("%s 3D generation %s completed.", provider, taskID)}
	if modelPath != "" {
		lines = append(lines, "Model file: "+modelPath)
	}
	if len(files) > 1 {
		lines = append(lines, "All downloaded files:")
		for _, f := range files {
			lines = append(lines, "- "+f)
		}
	}
	if thumbnailPath != "" {
		lines = append(lines, "Preview image: "+thumbnailPath)
	}
	return strings.Join(lines, "\n")
}
