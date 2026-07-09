package http

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/go-chi/chi/v5"
)

func TestInputRequiredStreamEventIncludesQuestionAndTranscript(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	sessionManager := session.NewManager(store)
	sess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	sess.AddUserMessage("deploy this")
	sess.AddAssistantMessage("I need confirmation.", nil)
	if err := sessionManager.Save(sess); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	question := &session.QuestionData{
		Question: "Deploy to production?",
		Header:   "Confirm deploy",
		Options: []session.QuestionOption{
			{Label: "Deploy", Description: "Proceed with production deploy"},
			{Label: "Stop", Description: "Do not deploy"},
		},
		Custom: true,
	}
	if err := sessionManager.SetPendingQuestion(sess.ID, question); err != nil {
		t.Fatalf("failed to set pending question: %v", err)
	}
	if err := sessionManager.SetSessionStatus(sess.ID, string(session.StatusInputRequired)); err != nil {
		t.Fatalf("failed to set input-required status: %v", err)
	}
	fresh, err := sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatalf("failed to reload session: %v", err)
	}

	server := &Server{sessionManager: sessionManager}
	event := server.inputRequiredStreamEvent(fresh)
	if event == nil {
		t.Fatal("expected input-required stream event")
	}
	if event.Type != "input_required" || event.Status != string(session.StatusInputRequired) {
		t.Fatalf("unexpected event type/status: %#v", event)
	}
	if event.Question == nil || event.Question.Header != "Confirm deploy" {
		t.Fatalf("expected pending question payload, got %#v", event.Question)
	}
	if len(event.Messages) != 2 {
		t.Fatalf("expected transcript messages in event, got %d", len(event.Messages))
	}
}

func TestHandleChatStreamForwardsProviderDeltasBeforeCompletion(t *testing.T) {
	allowProviderCompletion := make(chan struct{})
	var allowOnce sync.Once
	allowCompletion := func() {
		allowOnce.Do(func() {
			close(allowProviderCompletion)
		})
	}
	defer allowCompletion()

	providerErrors := make(chan string, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/v1/chat/completions"; got != want {
			providerErrors <- fmt.Sprintf("provider path = %q, want %q", got, want)
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		var raw map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			providerErrors <- "decode provider request: " + err.Error()
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if raw["stream"] != true {
			providerErrors <- fmt.Sprintf("provider stream flag = %#v, want true", raw["stream"])
			http.Error(w, "stream required", http.StatusBadRequest)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			providerErrors <- "provider response writer does not support flushing"
			http.Error(w, "no flush", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"Hel"}}]}

`))
		flusher.Flush()

		select {
		case <-allowProviderCompletion:
		case <-time.After(2 * time.Second):
			providerErrors <- "brute did not forward first provider delta before completion"
			return
		}

		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}

data: [DONE]

`))
		flusher.Flush()
	}))
	defer provider.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()
	cfg.ActiveProvider = string(config.ProviderOpenAI)
	cfg.DefaultModel = "test-model"
	cfg.LLMRetries = 1
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{
		APIKey:  "test-key",
		BaseURL: provider.URL + "/v1",
		Model:   "test-model",
	}

	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)
	sess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	api := httptest.NewServer(server.router)
	defer api.Close()

	reqBody := bytes.NewBufferString(`{"message":"hello"}`)
	req, err := http.NewRequest(http.MethodPost, api.URL+"/sessions/"+sess.ID+"/chat/stream", reqBody)
	if err != nil {
		t.Fatalf("create stream request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := api.Client().Do(req)
	if err != nil {
		t.Fatalf("post chat stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	sawFirstDelta := false
	for scanner.Scan() {
		var event ChatStreamEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode stream event %q: %v", scanner.Text(), err)
		}
		if event.Type != "assistant_delta" {
			continue
		}
		if event.Delta != "Hel" {
			t.Fatalf("first assistant delta = %q, want %q", event.Delta, "Hel")
		}
		sawFirstDelta = true
		allowCompletion()
		break
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stream before first delta: %v", err)
	}
	if !sawFirstDelta {
		t.Fatalf("stream ended before first assistant delta")
	}

	sawDone := false
	for scanner.Scan() {
		var event ChatStreamEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode stream event %q: %v", scanner.Text(), err)
		}
		if event.Type == "done" {
			sawDone = true
			if event.Content != "Hello" {
				t.Fatalf("done content = %q, want %q", event.Content, "Hello")
			}
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stream after first delta: %v", err)
	}
	if !sawDone {
		t.Fatalf("stream ended before done event")
	}

	select {
	case msg := <-providerErrors:
		t.Fatal(msg)
	default:
	}
}

type failingStreamWriter struct {
	header http.Header
	status int
	writes int
}

func (w *failingStreamWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *failingStreamWriter) WriteHeader(statusCode int) {
	w.status = statusCode
}

func (w *failingStreamWriter) Write(_ []byte) (int, error) {
	w.writes++
	return 0, errors.New("client disconnected")
}

func (w *failingStreamWriter) Flush() {}

func TestHandleChatStreamContinuesRunWhenInitialStatusWriteFails(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/v1/chat/completions"; got != want {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flush", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}

data: [DONE]

`))
		flusher.Flush()
	}))
	defer provider.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()
	cfg.ActiveProvider = string(config.ProviderOpenAI)
	cfg.DefaultModel = "test-model"
	cfg.LLMRetries = 1
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{
		APIKey:  "test-key",
		BaseURL: provider.URL + "/v1",
		Model:   "test-model",
	}

	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)
	sess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/chat/stream", bytes.NewBufferString(`{"message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("sessionID", sess.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rw := &failingStreamWriter{}
	server.handleChatStream(rw, req)
	if rw.writes == 0 {
		t.Fatal("expected initial stream write attempt")
	}

	fresh, err := sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatalf("failed to reload session: %v", err)
	}
	if fresh.Status != session.StatusCompleted {
		t.Fatalf("session status = %s, want %s", fresh.Status, session.StatusCompleted)
	}
	if len(fresh.Messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(fresh.Messages))
	}
	last := fresh.Messages[len(fresh.Messages)-1]
	if last.Role != "assistant" || last.Content != "Hello" {
		t.Fatalf("last message = role %q content %q, want assistant Hello", last.Role, last.Content)
	}
}

func TestHandleChatStreamSetsAntiBufferingHeader(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()
	cfg.ActiveProvider = string(config.ProviderOpenAI)
	cfg.DefaultModel = "test-model"
	cfg.LLMRetries = 1
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{
		APIKey:  "test-key",
		BaseURL: "http://127.0.0.1:1/v1",
		Model:   "test-model",
	}

	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)
	sess, err := sessionManager.Create("build")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/chat/stream", bytes.NewBufferString(`{"message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("sessionID", sess.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rw := &failingStreamWriter{}
	server.handleChatStream(rw, req)
	if got := rw.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
}
