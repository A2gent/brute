package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

type questionDirectChild struct {
	mu       sync.Mutex
	status   string
	messages []string
	injected []string
	stream   func(n int, w http.ResponseWriter)
}

func (c *questionDirectChild) handler(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/health":
	case "/sessions":
		_ = json.NewEncoder(w).Encode(CreateSessionResponse{ID: "direct-child"})
	case "/sessions/direct-child":
		c.mu.Lock()
		status := c.status
		c.mu.Unlock()
		_ = json.NewEncoder(w).Encode(SessionResponse{ID: "direct-child", Status: status, Metadata: map[string]interface{}{"source": "direct_agent_session"}})
	case "/sessions/direct-child/inject":
		var body struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.injected = append(c.injected, body.Message)
		c.mu.Unlock()
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	case "/sessions/direct-child/chat/stream":
		var req ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		c.mu.Lock()
		c.messages = append(c.messages, req.Message)
		n := len(c.messages)
		c.mu.Unlock()
		c.stream(n, w)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newDirectAgentQuestionTest(t *testing.T, child *questionDirectChild) (*Server, *session.Session) {
	t.Helper()
	s := newMCPBridgeTestServer(t)
	yaml := "version: \"1\"\nagent:\n  id: q-host\n  name: Q Host\nruntime:\n  type: host\nworkspace:\n  scope: none\n"
	now := time.Now()
	if err := s.store.SaveAgentDefinition(&storage.AgentDefinitionRecord{ID: "q-host", Name: "Q Host", Runtime: "host", DefinitionYAML: yaml, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	sess, _ := s.sessionManager.Create("build")
	sess.Metadata = map[string]interface{}{"unified_agent_id": "q-host"}
	sess.AddUserMessage("pick a color")
	sess.SetStatus(session.StatusRunning)
	if err := s.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	def, err := s.unifiedAgentDefinitionForSession(sess)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := s.resolveDockerWorkspaceBinding(def, "")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(child.handler))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	name := containerNameForAgent(def.Agent.ID, workspace.ContainerNameBinding)
	s.hostRuntime.procs[name] = &hostAgentProc{
		name: name, defID: def.Agent.ID, port: port, done: make(chan struct{}),
		cmd:    &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}, Env: []string{fmt.Sprintf("%s=http://127.0.0.1:%d", parentAgentToolsURLEnv, s.Port())}},
		labels: map[string]string{dockerRuntimeLLMFingerprintLabelKey: llmFingerprint(def)},
	}
	return s, sess
}

func writeQuestionStreamEvents(w http.ResponseWriter, events ...ChatStreamEvent) {
	for _, ev := range events {
		_ = json.NewEncoder(w).Encode(ev)
	}
}

func waitForDirectSessionIdle(t *testing.T, s *Server, id string, want session.Status) *session.Session {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := s.sessionManager.Get(id)
		if err == nil && got.Status == want && s.activeSessionRunCount(id) == 0 {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s did not reach %s (last %v)", id, want, got.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDirectAgentQuestionMirroredAndAnswerResumesChild(t *testing.T) {
	child := &questionDirectChild{status: string(session.StatusInputRequired)}
	child.stream = func(n int, w http.ResponseWriter) {
		if n == 1 {
			writeQuestionStreamEvents(w,
				ChatStreamEvent{Type: "input_required", Status: "input_required", Question: &session.QuestionData{Question: "Which color?", Header: "Color"}, Messages: []MessageResponse{{Role: "user", Content: "child transcript"}}},
				ChatStreamEvent{Type: "done", Content: "Waiting for your answer.", Status: "input_required"},
			)
			return
		}
		writeQuestionStreamEvents(w, ChatStreamEvent{Type: "done", Content: "Green it is.", Status: "completed"})
	}
	s, sess := newDirectAgentQuestionTest(t, child)

	result, runErr := s.runSessionWithoutStreaming(context.Background(), sess, "pick a color")
	if err := s.finalizeSessionRunWithoutStreaming(context.Background(), sess, result, runErr); err != nil {
		t.Fatal(err)
	}
	parked := waitForDirectSessionIdle(t, s, sess.ID, session.StatusInputRequired)
	if q, _ := s.sessionManager.GetPendingQuestion(parked.ID); q == nil || q.Question != "Which color?" {
		t.Fatalf("child question not mirrored to parent: %#v", q)
	}

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/answer", bytes.NewBufferString(`{"answer":"green"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("answer status = %d (%s)", rec.Code, rec.Body.String())
	}
	done := waitForDirectSessionIdle(t, s, sess.ID, session.StatusCompleted)

	child.mu.Lock()
	defer child.mu.Unlock()
	if len(child.messages) != 2 || child.messages[1] != "green" {
		t.Fatalf("answer not forwarded to child: %q", child.messages)
	}
	var order []string
	for _, m := range done.Messages {
		if m.Role == "user" || m.Role == "assistant" {
			order = append(order, m.Role+":"+m.Content)
		}
	}
	want := []string{"user:pick a color", "assistant:Waiting for your answer.", "user:green", "assistant:Green it is."}
	if len(order) != len(want) {
		t.Fatalf("parent transcript = %q, want %q", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("parent transcript = %q, want %q", order, want)
		}
	}
	if _, ok := done.Metadata["pending_question"]; ok {
		t.Fatal("pending question survived the answer")
	}
}

func TestDirectAgentInjectionForwardedAndKeptOnSave(t *testing.T) {
	release := make(chan struct{})
	streaming := make(chan struct{})
	child := &questionDirectChild{status: string(session.StatusCompleted)}
	child.stream = func(n int, w http.ResponseWriter) {
		close(streaming)
		<-release
		writeQuestionStreamEvents(w, ChatStreamEvent{Type: "done", Content: "Done with the note.", Status: "completed"})
	}
	s, sess := newDirectAgentQuestionTest(t, child)

	runID := s.registerActiveSessionRun(sess.ID, func() {})
	finished := make(chan error, 1)
	go func() {
		result, runErr := s.runSessionWithoutStreaming(context.Background(), sess, "pick a color")
		finished <- s.finalizeSessionRunWithoutStreaming(context.Background(), sess, result, runErr)
	}()
	<-streaming

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/inject", bytes.NewBufferString(`{"message":"use blue instead"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("inject status = %d (%s)", rec.Code, rec.Body.String())
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	s.unregisterActiveSessionRun(sess.ID, runID)

	child.mu.Lock()
	injected := append([]string(nil), child.injected...)
	child.mu.Unlock()
	if len(injected) != 1 || injected[0] != "use blue instead" {
		t.Fatalf("note not forwarded to child: %q", injected)
	}
	got, err := s.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var note, reply bool
	for _, m := range got.Messages {
		note = note || (m.Role == "user" && m.Content == "use blue instead")
		reply = reply || (m.Role == "assistant" && m.Content == "Done with the note.")
	}
	if !note || !reply {
		t.Fatalf("parent lost injected note (%v) or reply (%v): %+v", note, reply, got.Messages)
	}
}
