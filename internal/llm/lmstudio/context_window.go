package lmstudio

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const contextCatalogTTL = 30 * time.Second

type contextCatalog struct {
	sync.Mutex
	expires time.Time
	windows map[string]int
}

var contextCatalogs sync.Map

// ContextWindow discovers the usable window from LM Studio's native API, not
// the OpenAI-compatible model list (which omits context limits). Cache by server
// and credentials, including failures, so session polling cannot flood an offline
// server. A short TTL also picks up models reloaded with a different context size.
func ContextWindow(baseURL, apiKey, model string) int {
	model = strings.TrimSpace(model)
	if model == "" {
		return 0
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	root := strings.TrimSuffix(baseURL, "/v1")
	key := struct {
		URL            string
		CredentialHash [32]byte
	}{root, sha256.Sum256([]byte(apiKey))}
	cached, _ := contextCatalogs.LoadOrStore(key, &contextCatalog{})
	catalog := cached.(*contextCatalog)
	catalog.Lock()
	defer catalog.Unlock()
	if time.Now().After(catalog.expires) {
		catalog.windows = fetchContextWindows(root, apiKey)
		catalog.expires = time.Now().Add(contextCatalogTTL)
	}
	return catalog.windows[model]
}

func fetchContextWindows(root, apiKey string) map[string]int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/api/v1/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var payload struct {
		Models []struct {
			Type             string   `json:"type"`
			Key              string   `json:"key"`
			MaxContextLength int      `json:"max_context_length"`
			Variants         []string `json:"variants"`
			LoadedInstances  []struct {
				ID     string `json:"id"`
				Config struct {
					ContextLength int `json:"context_length"`
				} `json:"config"`
			} `json:"loaded_instances"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload); err != nil {
		return nil
	}
	windows := make(map[string]int)
	for _, model := range payload.Models {
		if model.Type != "llm" {
			continue
		}
		window := model.MaxContextLength
		loadedWindow := 0
		for _, instance := range model.LoadedInstances {
			if n := instance.Config.ContextLength; n > 0 {
				// A model key can route to any loaded instance. Use the smallest
				// configured window, but retain exact limits for explicit instance IDs.
				if loadedWindow == 0 || n < loadedWindow {
					loadedWindow = n
				}
			}
		}
		if loadedWindow > 0 {
			window = loadedWindow
		}
		if window > 0 {
			windows[model.Key] = window
			for _, variant := range model.Variants {
				windows[variant] = window
			}
		}
		for _, instance := range model.LoadedInstances {
			if instance.ID != "" && instance.ID != model.Key && instance.Config.ContextLength > 0 {
				windows[instance.ID] = instance.Config.ContextLength
			}
		}
	}
	return windows
}
