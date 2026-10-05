package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

// Inspect the catalog when the provider first receives work, not after completion:
// a terminal lifecycle event cannot repair a list that stays queued during a run.
func TestSessionCatalogRunningPublishedBeforeAgentWork(t *testing.T) {
	for _, path := range []string{"streaming_chat", "nonstream_chat", "serial_queue"} {
		t.Run(path, func(t *testing.T) {
			var events <-chan SessionCatalogEvent
			var manager *session.Manager
			var sessionID string
			observed := make(chan error, 1)
			release := make(chan struct{})
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var observation error
				select {
				case event := <-events:
					if event.Type != "session_updated" || event.SessionID != sessionID || event.Status != string(session.StatusRunning) {
						observation = fmt.Errorf("catalog event at agent start = %+v, want session_updated/running for %s", event, sessionID)
					}
				default:
					observation = errors.New("no running catalog event before agent work")
				}
				fresh, err := manager.Get(sessionID)
				if err != nil {
					observation = fmt.Errorf("load session at agent start: %w", err)
				} else if fresh.Status != session.StatusRunning {
					observation = fmt.Errorf("persisted status at agent start = %s, want running", fresh.Status)
				}
				observed <- observation
				<-release
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"))
			}))
			t.Cleanup(provider.Close)

			server, sess := newCatalogStartTestServer(t, provider.URL, "")
			manager = server.sessionManager
			sessionID = sess.ID
			if path == "serial_queue" {
				sess.Metadata = map[string]interface{}{
					sessionQueueModeMetadataKey: sessionQueueModeSerial,
					sessionQueueAutoStartKey:    true,
				}
				sess.AddUserMessage("hello")
				if err := manager.Save(sess); err != nil {
					t.Fatal(err)
				}
			}
			var unsubscribe func()
			events, unsubscribe = server.SubscribeSessionCatalogEvents("")
			t.Cleanup(unsubscribe)

			done := make(chan struct{})
			recorder := httptest.NewRecorder()
			go func() {
				defer close(done)
				if path == "serial_queue" {
					server.runSerialQueuedSession(context.Background(), sess.ID)
					return
				}
				endpoint := "/sessions/" + sess.ID + "/chat"
				if path == "streaming_chat" {
					endpoint += "/stream"
				}
				server.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, endpoint, bytes.NewBufferString(`{"message":"hello"}`)))
			}()
			t.Cleanup(func() {
				close(release)
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("session run did not stop after releasing provider")
				}
			})

			select {
			case err := <-observed:
				if err != nil {
					t.Fatal(err)
				}
			case <-done:
				t.Fatalf("session run ended before agent work: %s", recorder.Body.String())
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for agent work")
			}
		})
	}
}

func TestSerialQueueMissingMessagePublishesSavedFailure(t *testing.T) {
	for _, failSave := range []bool{false, true} {
		t.Run(fmt.Sprintf("save_fails=%t", failSave), func(t *testing.T) {
			rejectStatus := session.Status("")
			if failSave {
				rejectStatus = session.StatusFailed
			}
			server, sess := newCatalogStartTestServer(t, "", rejectStatus)
			sess.Metadata = map[string]interface{}{
				sessionQueueModeMetadataKey: sessionQueueModeSerial,
				sessionQueueAutoStartKey:    true,
			}
			if err := server.sessionManager.Save(sess); err != nil {
				t.Fatal(err)
			}
			events, unsubscribe := server.SubscribeSessionCatalogEvents("")
			defer unsubscribe()
			if !server.runSerialQueuedSession(context.Background(), sess.ID) {
				t.Fatal("missing-message session should allow queue advancement")
			}
			select {
			case event := <-events:
				if failSave {
					t.Fatalf("published unpersisted failure: %+v", event)
				}
				if event.Type != "session_updated" || event.SessionID != sess.ID || event.Status != string(session.StatusFailed) {
					t.Fatalf("unexpected failure catalog event: %+v", event)
				}
			default:
				if !failSave {
					t.Fatal("no catalog event for saved missing-message failure")
				}
			}
		})
	}
}

func TestSessionCatalogRunningNotPublishedWhenSaveFails(t *testing.T) {
	for _, path := range []string{"/chat", "/chat/stream", "serial_queue"} {
		t.Run(path, func(t *testing.T) {
			server, sess := newCatalogStartTestServer(t, "", session.StatusRunning)
			if path == "serial_queue" {
				sess.Metadata = map[string]interface{}{
					sessionQueueModeMetadataKey: sessionQueueModeSerial,
					sessionQueueAutoStartKey:    true,
				}
				sess.AddUserMessage("hello")
				if err := server.sessionManager.Save(sess); err != nil {
					t.Fatal(err)
				}
			}
			events, unsubscribe := server.SubscribeSessionCatalogEvents("")
			defer unsubscribe()
			if path == "serial_queue" {
				server.runSerialQueuedSession(context.Background(), sess.ID)
			} else {
				recorder := httptest.NewRecorder()
				server.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+path, bytes.NewBufferString(`{"message":"hello"}`)))
				if recorder.Code != http.StatusInternalServerError {
					t.Fatalf("failed running save status = %d, want 500", recorder.Code)
				}
			}
			select {
			case event := <-events:
				t.Fatalf("published unpersisted running status: %+v", event)
			default:
			}
		})
	}
}

type catalogStartFailingStore struct {
	storage.Store
	rejectStatus session.Status
}

func (s *catalogStartFailingStore) SaveSession(sess *storage.Session) error {
	if sess.Status == string(s.rejectStatus) {
		return errors.New("test session save failure")
	}
	return s.Store.SaveSession(sess)
}

func newCatalogStartTestServer(t *testing.T, providerURL string, rejectStatus session.Status) (*Server, *session.Session) {
	t.Helper()
	t.Setenv("A2GENT_PARENT_PROXY_URL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var sessionStore storage.Store = store
	if rejectStatus != "" {
		sessionStore = &catalogStartFailingStore{Store: store, rejectStatus: rejectStatus}
	}
	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()
	cfg.ActiveProvider = string(config.ProviderOpenAI)
	cfg.DefaultModel = "test-model"
	cfg.LLMRetries = 1
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{
		APIKey: "test-key", BaseURL: providerURL + "/v1", Model: "test-model",
	}
	manager := session.NewManager(sessionStore)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), manager, sessionStore, speechcache.New(0), 0)
	sess, err := manager.CreateQueued("build")
	if err != nil {
		t.Fatal(err)
	}
	return server, sess
}
