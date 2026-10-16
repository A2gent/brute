package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
	"github.com/go-chi/chi/v5"
)

func TestSystemPromptSnapshotRestartAndDetailCompatibility(t *testing.T) {
	path := t.TempDir()
	store, err := storage.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	manager := session.NewManager(store)
	sess, err := manager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	prefix := "Environment context:\n- Current time: 2026-10-14T18:33:24+03:00 (Local)\nfrozen 🤖"
	original := &systemPromptSnapshot{BasePrompt: "legacy", CombinedPrompt: prefix, CombinedEstimated: 42,
		Blocks: []systemPromptBlockSnapshot{{Type: "text", Value: "old", Enabled: true, ResolvedContent: "frozen"}}}
	raw, _ := json.Marshal(original)
	if err := store.SaveSystemPromptSnapshot(sess.ID, raw); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager = session.NewManager(store)
	server := NewServer(config.DefaultConfig(), nil, tools.NewManager("."), manager, store, speechcache.New(0), 0)
	reloaded, err := manager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Metadata[sessionSystemPromptSnapshotMetadataKey] != nil || server.sessionToResponse(reloaded).SystemPromptSnapshot != nil {
		t.Fatal("ordinary session read hydrated snapshot")
	}
	if got, err := server.buildSystemPromptForSession(reloaded); err != nil || got != prefix {
		t.Fatalf("restart changed frozen prefix: %q", got)
	}
	for _, query := range []string{"", "?include_metadata=false", "?include_messages=false", "?message_limit=1"} {
		req := httptest.NewRequest(http.MethodGet, "/sessions/"+sess.ID+query, nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("sessionID", sess.ID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		rec := httptest.NewRecorder()
		server.handleGetSession(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("detail %s: %d %s", query, rec.Code, rec.Body.String())
		}
		var response SessionResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if query == "?include_messages=false" {
			if response.SystemPromptSnapshot != nil || response.Metadata[sessionSystemPromptSnapshotMetadataKey] != nil {
				t.Fatal("lightweight detail hydrated snapshot")
			}
			continue
		}
		if response.SystemPromptSnapshot == nil || response.SystemPromptSnapshot.CombinedPrompt != prefix || response.SystemPromptSnapshot.CombinedEstimated != 42 || len(response.SystemPromptSnapshot.Blocks) != 1 {
			t.Fatalf("detail snapshot incompatible: %+v", response.SystemPromptSnapshot)
		}
		if query != "?include_metadata=false" {
			legacy, _ := json.Marshal(response.Metadata[sessionSystemPromptSnapshotMetadataKey])
			var got systemPromptSnapshot
			if err := json.Unmarshal(legacy, &got); err != nil || got.CombinedPrompt != prefix {
				t.Fatalf("legacy metadata API incompatible: %s %v", legacy, err)
			}
		}
	}
}

func TestDisplaySnapshotCreationDoesNotSavePartialSession(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddUserMessage("retain transcript")
	sess.Metadata["private_state"] = "retain state"
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	partial, err := server.sessionManager.GetForDisplay(sess.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	partial.Title = "stale title"
	partial.Metadata = nil
	snapshot, err := server.ensureDisplaySystemPromptSnapshot(partial)
	if err != nil || snapshot == nil {
		t.Fatal("missing generated snapshot")
	}
	got, err := server.sessionManager.Get(sess.ID)
	if err != nil || got.Title != sess.Title || len(got.Messages) != 1 || got.Metadata["private_state"] != "retain state" || !got.UpdatedAt.Equal(sess.UpdatedAt) {
		t.Fatalf("detail read overwrote session: %+v %v", got, err)
	}
	if got.Metadata[sessionSystemPromptSnapshotMetadataKey] != nil {
		t.Fatal("snapshot persisted in metadata")
	}
	payload, err := store.GetSystemPromptSnapshot(sess.ID)
	if err != nil || !strings.Contains(string(payload), "combined_prompt") {
		t.Fatalf("snapshot not persisted separately: %s %v", payload, err)
	}
}

func TestA2ASubAgentSnapshotPreservedAcrossReload(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	sess, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	sa := &storage.SubAgent{ID: "snapshot-sub-agent", Name: "Frozen", InstructionBlocks: `[{"type":"text","value":"original instruction"}]`}
	if err := store.SaveSubAgent(sa); err != nil {
		t.Fatal(err)
	}
	sess.Metadata["sub_agent_id"] = sa.ID
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	first, err := server.buildSystemPromptForA2ASession(sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "original instruction") {
		t.Fatal("sub-agent instruction missing")
	}
	sa.InstructionBlocks = `[{"type":"text","value":"changed instruction"}]`
	if err := store.SaveSubAgent(sa); err != nil {
		t.Fatal(err)
	}
	reloaded, err := server.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := server.buildSystemPromptForA2ASession(reloaded); err != nil || got != first {
		t.Fatal("A2A restart regenerated frozen sub-agent prefix")
	}
}

type failingPromptSnapshotStore struct {
	storage.Store
	snapshots storage.SystemPromptSnapshotStore
	readErr   error
	payload   json.RawMessage
	writeErr  error
	writes    int
}

func (s *failingPromptSnapshotStore) GetSystemPromptSnapshot(id string) (json.RawMessage, error) {
	if s.readErr != nil || s.payload != nil {
		return s.payload, s.readErr
	}
	return s.snapshots.GetSystemPromptSnapshot(id)
}

func (s *failingPromptSnapshotStore) SaveSystemPromptSnapshot(id string, payload json.RawMessage) error {
	s.writes++
	if s.writeErr != nil {
		return s.writeErr
	}
	return s.snapshots.SaveSystemPromptSnapshot(id, payload)
}

func TestSystemPromptSnapshotErrorsStopPreparation(t *testing.T) {
	for _, failure := range []string{"read", "decode", "write"} {
		t.Run(failure, func(t *testing.T) {
			server, store := newUnifiedAgentsTestServer(t)
			sess, err := server.sessionManager.Create("build")
			if err != nil {
				t.Fatal(err)
			}
			fault := &failingPromptSnapshotStore{Store: store, snapshots: store}
			switch failure {
			case "read":
				fault.readErr = errors.New("snapshot read unavailable")
			case "decode":
				fault.payload = json.RawMessage(`{"combined_prompt":42}`)
			case "write":
				fault.writeErr = errors.New("snapshot write unavailable")
			}
			server.store = fault
			if prompt, err := server.buildSystemPromptForSession(sess); err == nil || prompt != "" {
				t.Fatalf("%s failure not propagated: %q %v", failure, prompt, err)
			}
			req := httptest.NewRequest(http.MethodGet, "/sessions/"+sess.ID, nil)
			route := chi.NewRouteContext()
			route.URLParams.Add("sessionID", sess.ID)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
			rec := httptest.NewRecorder()
			server.handleGetSession(rec, req)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("detail hid %s failure: %d %s", failure, rec.Code, rec.Body.String())
			}
			if failure != "write" && fault.writes != 0 {
				t.Fatal("failed read/decode overwrote frozen snapshot")
			}
			if sess.Metadata[sessionSystemPromptSnapshotMetadataKey] != nil {
				t.Fatal("failed preparation cached an unpersisted snapshot")
			}
		})
	}
}
