package http

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestDirectAgentChildReceivesParentHistoryOnlyWhenFresh(t *testing.T) {
	const original = "model unique locations from the chapel page"
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	pngData := base64.StdEncoding.EncodeToString(pngBuf.Bytes())
	failedTurn := func(sess *session.Session) {
		sess.AddUserMessageWithImages(original, []session.ImageAttachment{{Name: "a.png", MediaType: "image/png", DataBase64: pngData}})
		sess.AddAssistantMessage("Agent failed: database or disk is full", nil)
		sess.AddUserMessage("Continue")
	}
	cases := []struct {
		name        string
		seed        func(*session.Session)
		reuseChild  bool
		task        string
		wantExact   bool
		wantImages  int
		wantCreated int
	}{
		{name: "first turn stays verbatim", seed: func(sess *session.Session) { sess.AddUserMessage("hello") }, task: "hello", wantExact: true, wantCreated: 1},
		{name: "retry after failed child creation carries the original request", seed: failedTurn, task: "Continue", wantImages: 1, wantCreated: 1},
		{name: "reused child already has the history", seed: failedTurn, reuseChild: true, task: "Continue", wantExact: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newMCPBridgeTestServer(t)
			yaml := "version: \"1\"\nagent:\n  id: fresh-host\n  name: Fresh Host\nruntime:\n  type: host\nworkspace:\n  scope: none\n"
			now := time.Now()
			if err := s.store.SaveAgentDefinition(&storage.AgentDefinitionRecord{ID: "fresh-host", Name: "Fresh Host", Runtime: "host", DefinitionYAML: yaml, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			sess, _ := s.sessionManager.Create("build")
			sess.Metadata = map[string]interface{}{"unified_agent_id": "fresh-host"}
			tc.seed(sess)
			if err := s.sessionManager.Save(sess); err != nil {
				t.Fatal(err)
			}
			stored, err := s.sessionManager.Get(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			def, err := s.unifiedAgentDefinitionForSession(stored)
			if err != nil {
				t.Fatal(err)
			}
			workspace, err := s.resolveDockerWorkspaceBinding(def, "")
			if err != nil {
				t.Fatal(err)
			}

			var posted ChatRequest
			created := 0
			child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/health":
				case "/sessions":
					created++
					_ = json.NewEncoder(w).Encode(CreateSessionResponse{ID: "direct-child"})
				case "/sessions/direct-child":
					_ = json.NewEncoder(w).Encode(SessionResponse{ID: "direct-child", Status: "completed", Metadata: map[string]interface{}{"source": "direct_agent_session"}})
				case "/sessions/direct-child/chat/stream":
					_ = json.NewDecoder(r.Body).Decode(&posted)
					_ = json.NewEncoder(w).Encode(ChatStreamEvent{Type: "done", Content: "ok", Status: "completed"})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer child.Close()
			u, _ := url.Parse(child.URL)
			port, _ := strconv.Atoi(u.Port())
			name := containerNameForAgent(def.Agent.ID, workspace.ContainerNameBinding)
			s.hostRuntime.procs[name] = &hostAgentProc{
				name: name, defID: def.Agent.ID, port: port, done: make(chan struct{}),
				cmd: &exec.Cmd{
					Process: &os.Process{Pid: os.Getpid()},
					Env:     []string{parentAgentToolsURLEnv + "=http://127.0.0.1:" + strconv.Itoa(s.Port())},
				},
				labels: map[string]string{dockerRuntimeLLMFingerprintLabelKey: llmFingerprint(def)},
			}
			if tc.reuseChild {
				addDockerChildSessionEntry(stored, &LocalDockerAgent{Name: name, APIURL: child.URL}, "direct-child", nil)
			}

			if _, err := s.runDirectAgentSession(context.Background(), stored, tc.task, nil); err != nil {
				t.Fatal(err)
			}
			if created != tc.wantCreated {
				t.Fatalf("created %d children, want %d", created, tc.wantCreated)
			}
			if tc.wantExact {
				if posted.Message != tc.task {
					t.Fatalf("message = %q, want verbatim %q", posted.Message, tc.task)
				}
				return
			}
			if !strings.HasPrefix(posted.Message, tc.task) || !strings.Contains(posted.Message, original) {
				t.Fatalf("message lost the parent request: %q", posted.Message)
			}
			if len(posted.Images) != tc.wantImages {
				t.Fatalf("child got %d images, want %d", len(posted.Images), tc.wantImages)
			}
		})
	}
}
