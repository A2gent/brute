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
	"testing"
	"time"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestDirectAgentSessionForwardsInlineImages(t *testing.T) {
	s := newMCPBridgeTestServer(t)
	yaml := "version: \"1\"\nagent:\n  id: img-host\n  name: Img Host\nruntime:\n  type: host\nworkspace:\n  scope: none\n"
	now := time.Now()
	if err := s.store.SaveAgentDefinition(&storage.AgentDefinitionRecord{ID: "img-host", Name: "Img Host", Runtime: "host", DefinitionYAML: yaml, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	wantData := base64.StdEncoding.EncodeToString(pngBuf.Bytes())

	sess, _ := s.sessionManager.Create("build")
	sess.Metadata = map[string]interface{}{"unified_agent_id": "img-host"}
	sess.AddUserMessageWithImages("look", []session.ImageAttachment{{Name: "a.png", MediaType: "image/png", DataBase64: wantData}})
	if err := s.sessionManager.Save(sess); err != nil {
		t.Fatal(err)
	}
	stored, err := s.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.Messages[len(stored.Messages)-1].Images; len(got) != 1 || got[0].Path == "" || got[0].DataBase64 != "" {
		t.Fatalf("image should be stored as a file reference: %+v", got)
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
	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
		case "/sessions":
			_ = json.NewEncoder(w).Encode(CreateSessionResponse{ID: "direct-child"})
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

	if _, err := s.runDirectAgentSession(context.Background(), stored, "look", nil); err != nil {
		t.Fatal(err)
	}
	if len(posted.Images) != 1 {
		t.Fatalf("child got %d images, want 1", len(posted.Images))
	}
	img := posted.Images[0]
	if img.DataBase64 != wantData || img.MediaType != "image/png" || img.Path != "" {
		t.Fatalf("image not inlined: path=%q media=%q dataMatch=%v", img.Path, img.MediaType, img.DataBase64 == wantData)
	}
}
