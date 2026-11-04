package http

import (
	"context"
	"encoding/json"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/storage"
	"nhooyr.io/websocket"
)

const (
	slackTestBotToken = "xoxb-test-token"
	slackTestAppToken = "xapp-test-token"
	slackTestChannel  = "C100"
	slackTestRoot     = "1700000000.000100"
	slackTestProject  = "slack-project"
	slackTestBotUser  = "UBOT"
	slackTestBotID    = "BBOT"
)

var slackTestIdentity = slackIdentity{TeamID: "T1", Team: "Acme", UserID: slackTestBotUser, User: "agent", BotID: slackTestBotID}

type fakeSlackCall struct {
	method string
	token  string
	form   url.Values
}

type fakeSlack struct {
	t      *testing.T
	server *httptest.Server

	mu         sync.Mutex
	calls      []fakeSlackCall
	users      map[string]slackUser
	thread     []map[string]interface{}
	members    [][]slackUser
	methodErrs map[string]string
	gate       map[string]chan struct{}

	socket func(conn *websocket.Conn)
}

func newFakeSlack(t *testing.T) *fakeSlack {
	t.Helper()
	fake := &fakeSlack{t: t, users: map[string]slackUser{}, methodErrs: map[string]string{}, gate: map[string]chan struct{}{}}
	fake.server = httptest.NewServer(nethttp.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeSlack) serve(w nethttp.ResponseWriter, r *nethttp.Request) {
	if r.URL.Path == "/socket" {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			f.t.Errorf("websocket accept: %v", err)
			return
		}
		defer conn.CloseNow()
		f.socket(conn)
		return
	}

	method := strings.TrimPrefix(r.URL.Path, "/")
	if err := r.ParseForm(); err != nil {
		f.t.Errorf("parse form: %v", err)
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeSlackCall{method: method, token: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), form: r.PostForm})
	gate := f.gate[method]
	failure := f.methodErrs[method]
	f.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-time.After(3 * time.Second):
			f.t.Errorf("%s ran before its gate opened", method)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if failure != "" {
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": failure})
		return
	}
	json.NewEncoder(w).Encode(f.response(method, r))
}

func (f *fakeSlack) response(method string, r *nethttp.Request) map[string]interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]interface{}{"ok": true}
	switch method {
	case "auth.test":
		out["team_id"], out["team"], out["user_id"], out["user"], out["bot_id"] = "T1", "Acme", slackTestBotUser, "agent", slackTestBotID
	case "apps.connections.open":
		out["url"] = "ws" + strings.TrimPrefix(f.server.URL, "http") + "/socket"
	case "users.info":
		user, ok := f.users[r.PostForm.Get("user")]
		if !ok {
			return map[string]interface{}{"ok": false, "error": "user_not_found"}
		}
		out["user"] = user
	case "users.list":
		page := 0
		fmt.Sscanf(r.PostForm.Get("cursor"), "page-%d", &page)
		out["members"] = f.members[page]
		if page+1 < len(f.members) {
			out["response_metadata"] = map[string]string{"next_cursor": fmt.Sprintf("page-%d", page+1)}
		}
	case "conversations.replies":
		page := 0
		fmt.Sscanf(r.PostForm.Get("cursor"), "page-%d", &page)
		half := (len(f.thread) + 1) / 2
		if page == 0 {
			out["messages"] = f.thread[:half]
			out["response_metadata"] = map[string]string{"next_cursor": "page-1"}
		} else {
			out["messages"] = f.thread[half:]
		}
	}
	return out
}

func (f *fakeSlack) setThread(messages ...map[string]interface{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.thread = messages
}

func (f *fakeSlack) callsTo(method string) []fakeSlackCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var matched []fakeSlackCall
	for _, call := range f.calls {
		if call.method == method {
			matched = append(matched, call)
		}
	}
	return matched
}

func (f *fakeSlack) waitForCalls(method string, count int) []fakeSlackCall {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if calls := f.callsTo(method); len(calls) >= count {
			return calls
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.t.Fatalf("timed out waiting for %d %s calls, got %d", count, method, len(f.callsTo(method)))
	return nil
}

func slackThreadMsg(ts, user, text string) map[string]interface{} {
	return map[string]interface{}{"ts": ts, "user": user, "text": text}
}

func slackMention(ts, threadTS, user, text string) slackEvent {
	return slackEvent{Type: "app_mention", User: user, Text: text, TS: ts, ThreadTS: threadTS, Channel: slackTestChannel}
}

func saveSlackProject(t *testing.T, store *storage.SQLiteStore, id string) {
	t.Helper()
	folder := t.TempDir()
	if err := store.SaveProject(&storage.Project{ID: id, Name: id, Folder: &folder, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func saveSlackIntegration(t *testing.T, store *storage.SQLiteStore, config map[string]string) *storage.Integration {
	t.Helper()
	merged := map[string]string{"bot_token": slackTestBotToken, "app_token": slackTestAppToken}
	for key, value := range config {
		merged[key] = value
	}
	saveSlackProject(t, store, slackTestProject)
	integration := &storage.Integration{ID: "slack-1", Provider: "slack", Name: "Slack", Mode: "duplex", Enabled: true, Config: merged, ProjectID: slackTestProject, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := store.SaveIntegration(integration); err != nil {
		t.Fatal(err)
	}
	return integration
}

// fakeModel is an OpenAI-compatible endpoint. It lets tests inspect the exact bytes the real
// provider client sends, which is the only proof that text reaches the model.
type fakeModel struct {
	mu       sync.Mutex
	requests []fakeModelRequest
	inFlight int
	maxLive  int
	delay    time.Duration
	status   int
}

type fakeModelRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func (m *fakeModelRequest) text() string {
	var out strings.Builder
	for _, message := range m.Messages {
		out.WriteString(message.Role + ": " + message.Content + "\n")
	}
	return out.String()
}

func (m *fakeModel) serve(w nethttp.ResponseWriter, r *nethttp.Request) {
	var request fakeModelRequest
	json.NewDecoder(r.Body).Decode(&request)
	m.mu.Lock()
	m.requests = append(m.requests, request)
	m.inFlight++
	m.maxLive = max(m.maxLive, m.inFlight)
	delay, status := m.delay, m.status
	m.mu.Unlock()

	time.Sleep(delay)

	m.mu.Lock()
	m.inFlight--
	m.mu.Unlock()
	if status != 0 {
		nethttp.Error(w, `{"error":{"message":"provider exploded with internal-detail"}}`, status)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"**Done** <!channel>\"},\"finish_reason\":null}]}\n\n")
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// agentRequests drops auxiliary calls such as summaries: only agent turns carry the Slack framing.
func (m *fakeModel) agentRequests() []fakeModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	var matched []fakeModelRequest
	for i := range m.requests {
		if strings.Contains(m.requests[i].text(), "reached you through Slack") {
			matched = append(matched, m.requests[i])
		}
	}
	return matched
}

func newSlackRunServer(t *testing.T, fake *fakeSlack, model *fakeModel) (*Server, *storage.SQLiteStore) {
	t.Helper()
	server, store := newUnifiedAgentsTestServer(t)
	server.slack.baseURL = fake.server.URL
	t.Cleanup(func() {
		server.slack.runs.Wait()
		server.sessionSummaryWG.Wait()
	})
	if model == nil {
		return server, store
	}
	llmServer := httptest.NewServer(nethttp.HandlerFunc(model.serve))
	t.Cleanup(llmServer.Close)
	t.Setenv("LM_STUDIO_BASE_URL", llmServer.URL+"/v1")
	provider := server.config.Providers[string(config.ProviderLMStudio)]
	provider.BaseURL = llmServer.URL + "/v1"
	provider.Model = "test-model"
	server.config.Providers[string(config.ProviderLMStudio)] = provider
	server.config.ActiveProvider = string(config.ProviderLMStudio)
	server.config.LLMRetries = 0
	return server, store
}

func runSlackMention(server *Server, event slackEvent) {
	server.handleSlackMention(context.Background(), "slack-1", slackTestIdentity, event)
}
