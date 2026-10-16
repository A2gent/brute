package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func stallJev(t *testing.T, status int, choice string, confidence, progress float64) (*jev.Client, *atomic.Int32, <-chan jev.SystemOneRequest) {
	t.Helper()
	calls := &atomic.Int32{}
	requests := make(chan jev.SystemOneRequest, 20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request jev.SystemOneRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests <- request
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(jev.SystemOneResponse{Answers: map[string]jev.Answer{
			"progress": {Type: "noul", Noul: &progress},
			"state":    {Type: "choice", Choice: choice, Confidence: confidence},
		}})
	}))
	t.Cleanup(server.Close)
	return jev.NewClient("test-key", "", server.URL), calls, requests
}

func stallLoop(t *testing.T, detector *StallDetector, count int, repeat bool) (*session.Session, *MockLLM, *session.Manager) {
	t.Helper()
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sm := session.NewManager(store)
	sess, err := sm.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	sess.AddUserMessage("Find the parser definition and fix its bug")
	sess.SetStatus(session.StatusRunning)
	if err := sm.Save(sess); err != nil {
		t.Fatal(err)
	}
	mock := &MockLLM{Response: &llm.ChatResponse{Content: "Done"}}
	for i := 0; i < count; i++ {
		pattern := "parser"
		if !repeat {
			pattern = fmt.Sprintf("parser_%d", i)
		}
		mock.Responses = append(mock.Responses, &llm.ChatResponse{ToolCalls: []llm.ToolCall{{
			ID: fmt.Sprintf("grep-%d", i), Name: "grep", Input: fmt.Sprintf(`{"pattern":%q}`, pattern),
		}}})
	}
	manager := tools.NewManager(t.TempDir())
	manager.Register(namedTool{"grep"})
	ag := New(Config{StallDetector: detector, MaxSteps: count + 2}, mock, manager, sm)
	if _, _, err := ag.Run(context.Background(), sess, ""); err != nil {
		t.Fatal(err)
	}
	return sess, mock, sm
}

func stallNudges(request *llm.ChatRequest) int {
	return strings.Count(request.SystemPrompt, "You are repeating")
}

func TestStallLoopRepeatingGrepInjectsNudge(t *testing.T) {
	client, calls, requests := stallJev(t, http.StatusOK, "repeating", .95, .05)
	sess, mock, sm := stallLoop(t, &StallDetector{Client: client}, 4, true)
	if calls.Load() != 2 || len(mock.CapturedRequests) != 5 {
		t.Fatalf("Jev calls=%d main calls=%d", calls.Load(), len(mock.CapturedRequests))
	}
	for i, request := range mock.CapturedRequests {
		for _, message := range request.Messages {
			if message.Role == "system" {
				t.Fatal("system nudge must use SystemPrompt, not a provider-incompatible message role")
			}
		}
		if got := stallNudges(request); (i < 3 && got != 0) || (i >= 3 && got == 0) {
			t.Fatalf("request %d nudge count=%d", i, got)
		}
	}
	request := <-requests
	if len(request.Questions) != 2 || request.Questions["progress"].Type != "noul" || request.Questions["state"].Type != "choice" {
		t.Fatalf("both questions must share one request: %+v", request)
	}
	for _, text := range []string{"Find the parser definition", "grep", "args_head", "result_first_line", "status"} {
		if !strings.Contains(request.StateString(), text) {
			t.Errorf("digest missing %q: %s", text, request.StateString())
		}
	}
	stored, err := sm.Get(sess.ID)
	if err != nil || metadataFloat(stored.Metadata, "stall_check_attempt_count") != 2 || stored.Metadata["stall_check_last_outcome"] != "success" {
		t.Fatalf("persisted auxiliary metrics: sess=%+v err=%v", stored, err)
	}
}

func TestStallLoopHealthyNoNudge(t *testing.T) {
	client, calls, _ := stallJev(t, http.StatusOK, "progressing", .98, .95)
	sess, mock, _ := stallLoop(t, &StallDetector{Client: client}, 8, false)
	if calls.Load() != 1 || sess.Status != session.StatusCompleted {
		t.Fatalf("periodic check: calls=%d status=%s", calls.Load(), sess.Status)
	}
	for _, request := range mock.CapturedRequests {
		if stallNudges(request) != 0 {
			t.Fatal("healthy session was nudged")
		}
	}
}

func TestStallLoopFailOpenAndOptIn(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		status                  int
		choice                  string
		confidence, progress    float64
		disabled, missingClient bool
		outcome                 string
	}{
		{name: "disabled", disabled: true},
		{name: "missing client", missingClient: true},
		{name: "http error", status: 500, outcome: "error"},
		{name: "invalid answer", status: 200, choice: "unknown", confidence: .9, progress: .1, outcome: "invalid_response"},
		{name: "low confidence blocked", status: 200, choice: "blocked_needs_user", confidence: .7, progress: .1, outcome: "success"},
		{name: "conflicting progress", status: 200, choice: "repeating", confidence: .9, progress: .9, outcome: "success"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, calls, _ := stallJev(t, tc.status, tc.choice, tc.confidence, tc.progress)
			detector := &StallDetector{Client: client}
			if tc.disabled {
				detector = nil
			} else if tc.missingClient {
				detector.Client = nil
			}
			sess, mock, _ := stallLoop(t, detector, 4, true)
			if sess.Status != session.StatusCompleted {
				t.Fatalf("must fail open, status=%s", sess.Status)
			}
			for _, request := range mock.CapturedRequests {
				if stallNudges(request) != 0 {
					t.Fatal("unexpected nudge")
				}
			}
			if tc.disabled || tc.missingClient {
				if calls.Load() != 0 {
					t.Fatal("disabled detector called Jev")
				}
			} else if sess.Metadata["stall_check_last_outcome"] != tc.outcome {
				t.Fatalf("outcome=%v", sess.Metadata["stall_check_last_outcome"])
			}
		})
	}
}

func TestStallLoopBlockedAsksUserAndStops(t *testing.T) {
	client, calls, _ := stallJev(t, http.StatusOK, "blocked_needs_user", .95, .05)
	sess, mock, sm := stallLoop(t, &StallDetector{Client: client}, 4, true)
	if len(mock.CapturedRequests) != 3 || calls.Load() != 1 || sess.Status != session.StatusInputRequired {
		t.Fatalf("run continued after blocker: main=%d Jev=%d status=%s", len(mock.CapturedRequests), calls.Load(), sess.Status)
	}
	question, err := sm.GetPendingQuestion(sess.ID)
	if err != nil || question == nil || !question.Custom || !strings.Contains(question.Question, "?") {
		t.Fatalf("pending question=%+v err=%v", question, err)
	}
	stored, err := sm.Get(sess.ID)
	if err != nil || stored.Status != session.StatusInputRequired || stored.Messages[len(stored.Messages)-1].Role != "assistant" {
		t.Fatalf("question/status not persisted: %+v, %v", stored, err)
	}
}

func TestStallPrefilterAndWindow(t *testing.T) {
	detector := &StallDetector{Client: jev.NewClient("key", "", ""), Window: 3}
	tracker := &stallTracker{}
	observe := func(args, result string, failure bool) {
		tracker.observe(detector, []session.ToolCall{{ID: "id", Name: "grep", Input: json.RawMessage(args)}}, []llm.ToolResult{{ToolCallID: "id", Content: result, IsError: failure}})
	}
	observe(`{"pattern":"x","path":"src"}`, "hit 1", false)
	observe(`{ "path": "src", "pattern": "x" }`, "hit 2", false)
	if tracker.repeated {
		t.Fatal("two successful calls must not trigger")
	}
	observe(`{"pattern":"x","path":"src"}`, "hit 3", false)
	if !tracker.repeated {
		t.Fatal("canonical identical args must trigger on third call")
	}
	observe(`{"pattern":"other"}`, "different", false)
	if tracker.repeated || len(tracker.calls) != 3 {
		t.Fatal("old calls must leave the bounded window")
	}
	observe(`{"pattern":"a"}`, "permission denied", true)
	observe(`{"pattern":"b"}`, "permission denied", true)
	if !tracker.repeated {
		t.Fatal("same error with different args must trigger")
	}
}

func TestStallDigestBoundsPrivacyAndTimeout(t *testing.T) {
	client, calls, requests := stallJev(t, 200, "progressing", .9, .9)
	detector := &StallDetector{Client: client, Interval: 1, Window: 3, Secrets: []string{"private-value"}}
	ag := &Agent{config: Config{StallDetector: detector}}
	tracker := &stallTracker{}
	for i := 0; i < 5; i++ {
		tracker.observe(detector, []session.ToolCall{{ID: "id", Name: "grep", Input: json.RawMessage(fmt.Sprintf(`{"pattern":%q}`, strings.Repeat("я", 400)))}}, []llm.ToolResult{{ToolCallID: "id", Content: strings.Repeat("z", 500) + "\nsecond line must not be sent"}})
	}
	sess := session.New("test")
	sess.AddUserMessage("Find parser")
	ag.checkStall(context.Background(), sess, tracker, 1)
	request := <-requests
	if calls.Load() != 1 || len(request.StateString()) > 5000 || strings.Contains(request.StateString(), "second line") {
		t.Fatalf("unbounded digest: %s", request.StateString())
	}
	sess.AddUserMessage("Find private-value")
	ag.checkStall(context.Background(), sess, tracker, 2)
	if calls.Load() != 1 {
		t.Fatal("secret escaped to Jev")
	}
	sess.AddUserMessage("Find parser")
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if question := ag.checkStall(ctx, sess, tracker, 3); question != "" || sess.Metadata["stall_check_last_outcome"] != "timeout" {
		t.Fatalf("timeout must fail open: question=%q metadata=%v", question, sess.Metadata)
	}
}

func TestStallPrefilterOnlyNewEvidenceTriggers(t *testing.T) {
	detector := &StallDetector{Client: jev.NewClient("key", "", ""), Window: 12}
	tracker := &stallTracker{}
	for i := 0; i < 3; i++ {
		tracker.observe(detector, []session.ToolCall{{ID: "id", Name: "grep", Input: json.RawMessage(`{"pattern":"x"}`)}}, []llm.ToolResult{{ToolCallID: "id", Content: "hit"}})
	}
	if !tracker.repeated {
		t.Fatal("expected repeat")
	}
	tracker.observe(detector, []session.ToolCall{{ID: "id", Name: "read", Input: json.RawMessage(`{"path":"parser.go"}`)}}, []llm.ToolResult{{ToolCallID: "id", Content: "source"}})
	if tracker.repeated || len(tracker.calls) != 4 {
		t.Fatal("old evidence must not fire a new check after changing approach")
	}
}

func TestStallPrefilterHashesFullContent(t *testing.T) {
	detector := &StallDetector{Client: jev.NewClient("key", "", "")}
	tracker := &stallTracker{}
	for i := 0; i < 3; i++ {
		args, _ := json.Marshal(map[string]string{"pattern": strings.Repeat("x", 300) + fmt.Sprint(i)})
		tracker.observe(detector, []session.ToolCall{{ID: "id", Name: "grep", Input: args}}, []llm.ToolResult{{ToolCallID: "id", IsError: true, Content: "same error first line\n" + fmt.Sprint(i)}})
	}
	if tracker.repeated {
		t.Fatal("identical digest heads must not be mistaken for identical calls/errors")
	}
}
