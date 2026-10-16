package lmstudio

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestContextWindowNativeCatalog(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/v1/models" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected request: %s, auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"models":[
			{"type":"llm","key":"qwen/qwen3.8-27b","max_context_length":262144,"variants":["qwen/qwen3.8-27b@4bit"],"loaded_instances":[]},
			{"type":"llm","key":"loaded","max_context_length":262144,"loaded_instances":[{"id":"instance-a","config":{"context_length":4096}},{"id":"instance-b","config":{"context_length":8192}}]},
			{"type":"embedding","key":"embedding","max_context_length":2048},
			{"type":"llm","key":"unknown","max_context_length":0}
		]}`))
	}))
	defer server.Close()

	for model, want := range map[string]int{
		"qwen/qwen3.8-27b": 262144, "qwen/qwen3.8-27b@4bit": 262144,
		"loaded": 4096, "instance-a": 4096, "instance-b": 8192,
		"embedding": 0, "unknown": 0, "missing": 0,
	} {
		if got := ContextWindow(server.URL+"/v1/", "secret", model); got != want {
			t.Errorf("ContextWindow(%q) = %d, want %d", model, got, want)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want one cached catalog request", got)
	}
}

func TestContextWindowCatalogRefreshAndServerIsolation(t *testing.T) {
	var window atomic.Int32
	window.Store(4096)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if window.Load() == 4096 {
			w.Write([]byte(`{"models":[{"type":"llm","key":"model","max_context_length":262144,"loaded_instances":[{"id":"model","config":{"context_length":4096}}]}]}`))
		} else {
			w.Write([]byte(`{"models":[{"type":"llm","key":"model","max_context_length":262144}]}`))
		}
	}))
	defer server.Close()
	if got := ContextWindow(server.URL, "", "model"); got != 4096 {
		t.Fatalf("loaded window = %d", got)
	}
	window.Store(262144)
	contextCatalogs.Range(func(key, value any) bool {
		catalog := value.(*contextCatalog)
		catalog.Lock()
		catalog.expires = time.Time{}
		catalog.Unlock()
		return true
	})
	if got := ContextWindow(server.URL, "", "model"); got != 262144 {
		t.Fatalf("refreshed window = %d", got)
	}
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	if got := ContextWindow(other.URL, "", "model"); got != 0 {
		t.Fatalf("unrelated server window = %d", got)
	}
}

func TestContextWindowUnavailableCatalog(t *testing.T) {
	for _, body := range []string{"not json", `{"models":[{"type":"llm","key":"model","max_context_length":-1}]}`} {
		t.Run(body, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Write([]byte(body))
			}))
			defer server.Close()
			var wg sync.WaitGroup
			for range 10 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if got := ContextWindow(server.URL+"/v1", "", "model"); got != 0 {
						t.Errorf("unavailable window = %d, want 0", got)
					}
				}()
			}
			wg.Wait()
			if got := requests.Load(); got != 1 {
				t.Errorf("requests = %d, want one cached failure", got)
			}
		})
	}
}

func TestContextWindowCredentialsAndTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer valid":
			w.Write([]byte(`{"models":[{"type":"llm","key":"model","max_context_length":262144}]}`))
		case "Bearer slow":
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()
	if got := ContextWindow(server.URL, "valid", "model"); got != 262144 {
		t.Fatalf("authorized window = %d", got)
	}
	if got := ContextWindow(server.URL, "invalid", "model"); got != 0 {
		t.Fatalf("unauthorized credentials reused catalog: %d", got)
	}
	start := time.Now()
	if got := ContextWindow(server.URL, "slow", "model"); got != 0 {
		t.Fatalf("timed out request window = %d", got)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("request exceeded timeout allowance: %s", elapsed)
	}
}
