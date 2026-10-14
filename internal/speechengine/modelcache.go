package speechengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrModelCacheBusy = errors.New("engine is running; cancel the test before deleting its model")

// engineModelDir returns the folder where an engine keeps its downloaded model, or "" when the engine
// has no downloadable model. It is derived only from server-side config, never from client input.
func engineModelDir(engine, model string) string {
	model = strings.TrimSpace(model)
	switch normalizeEngineID(engine) {
	case EngineParakeet, EngineMoonshine, EngineKokoro, EngineQwen3TTS:
		return hfModelWatch(model).root
	case EngineWhisperKit:
		root := whisperKitModelRoot()
		if root == "" || model == "" {
			return ""
		}
		return filepath.Join(root, "openai_whisper-"+model)
	}
	return ""
}

// engineModelRepo returns the Hugging Face repo and optional sub-folder holding the engine's model.
func engineModelRepo(engine, model string) (repo, subdir string) {
	model = strings.TrimSpace(model)
	switch normalizeEngineID(engine) {
	case EngineParakeet, EngineMoonshine, EngineKokoro, EngineQwen3TTS:
		if strings.Contains(model, "/") {
			return model, ""
		}
	case EngineWhisperKit:
		if model != "" {
			return "argmaxinc/whisperkit-coreml", "openai_whisper-" + model
		}
	}
	return "", ""
}

// cacheSize sums the model folder. Interrupted downloads leave several *.incomplete files for the same
// blob (one per attempt); only the largest of each group is counted so retries do not inflate the total.
func cacheSize(root string) int64 {
	var total int64
	incomplete := map[string]int64{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".incomplete") {
			key := strings.TrimSuffix(name, ".incomplete")
			if dot := strings.LastIndex(key, "."); dot > 0 {
				key = key[:dot]
			}
			if info.Size() > incomplete[key] {
				incomplete[key] = info.Size()
			}
			return nil
		}
		total += info.Size()
		return nil
	})
	for _, size := range incomplete {
		total += size
	}
	return total
}

// ModelCacheInfo reports where an engine's model lives and how much disk it uses.
func ModelCacheInfo(engine, model string) (path string, sizeBytes int64) {
	dir := engineModelDir(engine, model)
	if dir == "" {
		return "", 0
	}
	if _, err := os.Stat(dir); err != nil {
		return dir, 0
	}
	return dir, cacheSize(dir)
}

// DeleteModelCache removes an engine's downloaded model (finished or partial).
func DeleteModelCache(engine, model string) (string, error) {
	dir := engineModelDir(engine, model)
	if dir == "" {
		return "", fmt.Errorf("engine %q has no downloadable model", engine)
	}
	if progress, ok := CurrentProgress(engine); ok && progress.Active {
		return dir, ErrModelCacheBusy
	}
	// Guard: only ever delete a folder created by the model downloaders.
	base := filepath.Base(dir)
	if !strings.HasPrefix(base, "models--") && !strings.HasPrefix(base, "openai_whisper-") {
		return dir, fmt.Errorf("refusing to delete unexpected path %q", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return dir, err
	}
	return dir, nil
}

var repoSizeCache = struct {
	mu    sync.Mutex
	sizes map[string]int64
}{sizes: map[string]int64{}}

// repoDownloadSize asks Hugging Face for the total size of a repo (or one folder of it). Best effort:
// it returns 0 when offline, so callers must treat 0 as "unknown".
func repoDownloadSize(ctx context.Context, repo, subdir string) int64 {
	if repo == "" {
		return 0
	}
	key := repo + "|" + subdir
	repoSizeCache.mu.Lock()
	if size, ok := repoSizeCache.sizes[key]; ok {
		repoSizeCache.mu.Unlock()
		return size
	}
	repoSizeCache.mu.Unlock()

	endpoint := "https://huggingface.co/api/models/" + repo + "/tree/main"
	if subdir != "" {
		endpoint += "/" + url.PathEscape(subdir)
	}
	endpoint += "?recursive=true"
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var entries []struct {
		Type string `json:"type"`
		Size int64  `json:"size"`
		LFS  *struct {
			Size int64 `json:"size"`
		} `json:"lfs"`
	}
	if json.NewDecoder(resp.Body).Decode(&entries) != nil {
		return 0
	}
	size := sumRepoEntries(entries)
	if size > 0 {
		repoSizeCache.mu.Lock()
		repoSizeCache.sizes[key] = size
		repoSizeCache.mu.Unlock()
	}
	return size
}

func sumRepoEntries(entries []struct {
	Type string `json:"type"`
	Size int64  `json:"size"`
	LFS  *struct {
		Size int64 `json:"size"`
	} `json:"lfs"`
}) int64 {
	var total int64
	for _, entry := range entries {
		if entry.Type != "file" {
			continue
		}
		if entry.LFS != nil && entry.LFS.Size > 0 {
			total += entry.LFS.Size
		} else {
			total += entry.Size
		}
	}
	return total
}

// ConfiguredModel returns the model id currently configured for an engine.
func ConfiguredModel(engine string) string {
	cfg := loadRuntimeConfigModels("")
	switch normalizeEngineID(engine) {
	case EngineParakeet:
		return cfg.parakeetModel
	case EngineMoonshine:
		return cfg.moonshineModel
	case EngineKokoro:
		return cfg.kokoroModel
	case EngineQwen3TTS:
		return cfg.qwen3TTSModel
	case EngineWhisperKit:
		return cfg.whisperKitModel
	}
	return ""
}

// removeStaleIncomplete drops leftovers of interrupted downloads. Hugging Face names every attempt's
// partial file differently, so they are never resumed; they only waste disk and inflate progress.
func removeStaleIncomplete(root string) {
	if root == "" {
		return
	}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(entry.Name(), ".incomplete") {
			_ = os.Remove(path)
		}
		return nil
	})
}
