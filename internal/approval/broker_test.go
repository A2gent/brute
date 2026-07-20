package approval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func testLimits() Limits {
	return Limits{
		MaxPending:     4,
		MaxInputBytes:  256,
		DefaultTimeout: 200 * time.Millisecond,
	}
}

func TestRequestRequiresSessionID(t *testing.T) {
	b := New(testLimits())
	_, err := b.Request(context.Background(), RequestParams{
		ToolUseID: "tu1",
		ToolName:  "bash",
	})
	if !errors.Is(err, ErrSessionIDRequired) {
		t.Fatalf("got %v, want ErrSessionIDRequired", err)
	}
}

func TestRequestResolveAllowOnce(t *testing.T) {
	b := New(testLimits())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	input := json.RawMessage(`{"cmd":"ls"}`)
	done := make(chan Result, 1)
	go func() {
		result, err := b.Request(ctx, RequestParams{
			SessionID: "sess-1",
			ToolUseID: "tu1",
			ToolName:  "bash",
			Input:     input,
			Reason:    "run ls",
		})
		if err != nil {
			t.Errorf("Request: %v", err)
			done <- Result{}
			return
		}
		done <- result
	}()

	waitForPending(t, b, 1)
	pending := b.Pending()
	if len(pending) != 1 {
		t.Fatalf("pending count = %d", len(pending))
	}
	req := pending[0]
	if req.ID == "" {
		t.Fatal("request id is empty")
	}
	if req.SessionID != "sess-1" || req.ToolName != "bash" {
		t.Fatalf("unexpected request: %+v", req)
	}

	if err := b.Resolve(req.ID, "sess-1", DecisionAllowOnce); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result := <-done; result.Decision != DecisionAllowOnce {
		t.Fatalf("decision = %q", result.Decision)
	}
	if len(b.Pending()) != 0 {
		t.Fatal("expected no pending after resolve")
	}
}

func TestResolveRejectsUnknownAndDuplicate(t *testing.T) {
	b := New(testLimits())
	ctx := context.Background()

	go func() {
		_, _ = b.Request(ctx, RequestParams{
			SessionID: "sess-1",
			ToolUseID: "tu1",
			ToolName:  "bash",
		})
	}()
	waitForPending(t, b, 1)
	id := b.Pending()[0].ID

	if err := b.Resolve("missing", "sess-1", DecisionAllowOnce); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("unknown resolve err = %v", err)
	}
	if err := b.Resolve(id, "sess-1", DecisionAllowOnce); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if err := b.Resolve(id, "sess-1", DecisionAllowOnce); !errors.Is(err, ErrRequestAlreadyResolved) {
		t.Fatalf("duplicate resolve err = %v", err)
	}
}

func TestResolveRejectsDuplicateAfterRequestCompletes(t *testing.T) {
	b := New(testLimits())
	ctx := context.Background()

	go func() {
		_, _ = b.Request(ctx, RequestParams{
			SessionID: "sess-1",
			ToolUseID: "tu1",
			ToolName:  "bash",
		})
	}()
	waitForPending(t, b, 1)
	id := b.Pending()[0].ID

	if err := b.Resolve(id, "sess-1", DecisionAllowOnce); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	waitForPending(t, b, 0)

	if err := b.Resolve(id, "sess-1", DecisionAllowOnce); !errors.Is(err, ErrRequestAlreadyResolved) {
		t.Fatalf("duplicate resolve after cleanup err = %v", err)
	}
}

func TestResolveRequiresMatchingSession(t *testing.T) {
	b := New(testLimits())
	ctx := context.Background()

	go func() {
		_, _ = b.Request(ctx, RequestParams{
			SessionID: "sess-1",
			ToolUseID: "tu1",
			ToolName:  "bash",
		})
	}()
	waitForPending(t, b, 1)
	id := b.Pending()[0].ID

	if err := b.Resolve(id, "other-session", DecisionAllowOnce); !errors.Is(err, ErrSessionMismatch) {
		t.Fatalf("session mismatch err = %v", err)
	}
}

func TestResolveRejectsAllowSessionForInteractiveRequest(t *testing.T) {
	b := New(testLimits())
	ctx := context.Background()

	startInteractive := func() string {
		go func() {
			_, _ = b.Request(ctx, RequestParams{
				SessionID: "sess-1",
				ToolUseID: "tu-ask",
				ToolName:  "AskUserQuestion",
				Input:     json.RawMessage(`{"questions":[{"question":"Pick?","options":[{"label":"A"}]}]}`),
				AskUser:   &AskUserPayload{Question: "Pick?", Suggestions: []string{"A"}},
			})
		}()
		waitForPending(t, b, 1)
		return b.Pending()[0].ID
	}

	t.Run("resolve rejects allow_session", func(t *testing.T) {
		id := startInteractive()
		if err := b.Resolve(id, "sess-1", DecisionAllowSession); !errors.Is(err, ErrInvalidDecision) {
			t.Fatalf("resolve err = %v, want ErrInvalidDecision", err)
		}
		if b.SessionAllowed("sess-1", "AskUserQuestion") {
			t.Fatal("interactive allow_session must not cache")
		}
		if len(b.Pending()) != 1 {
			t.Fatalf("pending = %d, want 1 after rejected allow_session", len(b.Pending()))
		}
		_ = b.Resolve(id, "sess-1", DecisionAllowOnce)
	})

	t.Run("request skips session cache", func(t *testing.T) {
		if !b.SessionAllowed("sess-1", "bash") {
			go func() {
				_, _ = b.Request(ctx, RequestParams{
					SessionID: "sess-1",
					ToolUseID: "tu-bash",
					ToolName:  "bash",
				})
			}()
			waitForPending(t, b, 1)
			id := b.Pending()[0].ID
			if err := b.Resolve(id, "sess-1", DecisionAllowSession); err != nil {
				t.Fatalf("Resolve bash: %v", err)
			}
		}

		done := make(chan struct{})
		go func() {
			_, err := b.Request(ctx, RequestParams{
				SessionID: "sess-1",
				ToolUseID: "tu-ask-2",
				ToolName:  "AskUserQuestion",
				Input:     json.RawMessage(`{"questions":[{"question":"Again?","options":[{"label":"Yes"}]}]}`),
				AskUser:   &AskUserPayload{Question: "Again?", Suggestions: []string{"Yes"}},
			})
			if err != nil {
				t.Errorf("interactive request with bash cache: %v", err)
			}
			close(done)
		}()
		waitForPending(t, b, 1)
		if b.SessionAllowed("sess-1", "AskUserQuestion") {
			t.Fatal("AskUserQuestion must never be session-cached")
		}
		id := b.Pending()[0].ID
		_ = b.Resolve(id, "sess-1", DecisionAllowOnce)
		<-done
	})
}

func TestAllowSessionCachesPerSessionAndTool(t *testing.T) {
	b := New(testLimits())
	ctx := context.Background()

	go func() {
		_, _ = b.Request(ctx, RequestParams{
			SessionID: "sess-1",
			ToolUseID: "tu1",
			ToolName:  "bash",
		})
	}()
	waitForPending(t, b, 1)
	id := b.Pending()[0].ID
	if err := b.Resolve(id, "sess-1", DecisionAllowSession); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !b.SessionAllowed("sess-1", "bash") {
		t.Fatal("expected session cache hit for sess-1/bash")
	}
	if b.SessionAllowed("sess-2", "bash") {
		t.Fatal("cache must not transfer to another session")
	}
	if b.SessionAllowed("sess-1", "edit") {
		t.Fatal("cache must be tool-specific")
	}

	dec, err := b.Request(ctx, RequestParams{
		SessionID: "sess-1",
		ToolUseID: "tu2",
		ToolName:  "bash",
		Input:     json.RawMessage(`{"cmd":"pwd"}`),
	})
	if err != nil {
		t.Fatalf("cached request: %v", err)
	}
	if dec.Decision != DecisionAllowSession {
		t.Fatalf("cached decision = %q", dec.Decision)
	}
	if len(b.Pending()) != 0 {
		t.Fatal("cached approval should not create pending request")
	}
}

func TestRequestTimeoutAndCancellation(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		b := New(testLimits())
		_, err := b.Request(context.Background(), RequestParams{
			SessionID: "sess-1",
			ToolUseID: "tu1",
			ToolName:  "bash",
			Timeout:   50 * time.Millisecond,
		})
		if !errors.Is(err, ErrTimedOut) {
			t.Fatalf("got %v, want ErrTimedOut", err)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		b := New(testLimits())
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := b.Request(ctx, RequestParams{
				SessionID: "sess-1",
				ToolUseID: "tu1",
				ToolName:  "bash",
				Timeout:   time.Minute,
			})
			done <- err
		}()
		waitForPending(t, b, 1)
		cancel()
		if err := <-done; !errors.Is(err, ErrCancelled) {
			t.Fatalf("got %v, want ErrCancelled", err)
		}
	})
}

func TestBoundedPendingAndInputSize(t *testing.T) {
	b := New(testLimits())
	ctx := context.Background()

	block := make(chan struct{})
	for i := 0; i < testLimits().MaxPending; i++ {
		go func() {
			_, _ = b.Request(ctx, RequestParams{
				SessionID: "sess-1",
				ToolUseID: "tu",
				ToolName:  "bash",
			})
			<-block
		}()
	}
	waitForPending(t, b, testLimits().MaxPending)

	_, err := b.Request(ctx, RequestParams{
		SessionID: "sess-1",
		ToolUseID: "overflow",
		ToolName:  "bash",
	})
	if !errors.Is(err, ErrTooManyPending) {
		t.Fatalf("pending overflow err = %v", err)
	}
	close(block)

	large := make([]byte, testLimits().MaxInputBytes+1)
	for i := range large {
		large[i] = 'a'
	}
	_, err = b.Request(ctx, RequestParams{
		SessionID: "sess-1",
		ToolUseID: "tu-big",
		ToolName:  "bash",
		Input:     json.RawMessage(large),
	})
	if !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("input size err = %v", err)
	}
}

func TestInputCopiedDefensively(t *testing.T) {
	b := New(testLimits())
	ctx := context.Background()
	input := json.RawMessage(`{"cmd":"ls"}`)
	orig := append(json.RawMessage(nil), input...)

	go func() {
		_, _ = b.Request(ctx, RequestParams{
			SessionID: "sess-1",
			ToolUseID: "tu1",
			ToolName:  "bash",
			Input:     input,
		})
	}()
	waitForPending(t, b, 1)
	req := b.Pending()[0]

	copy(input, []byte(`{"cmd":"rm -rf /"}`))
	if string(req.Input) != string(orig) {
		t.Fatalf("stored input mutated: %s", req.Input)
	}
}

func TestSubscribeAndAudit(t *testing.T) {
	b := New(testLimits())
	var mu sync.Mutex
	var events []Event
	unsub := b.Subscribe(func(ev Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	defer unsub()

	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		_, _ = b.Request(ctx, RequestParams{
			SessionID: "sess-1",
			ToolUseID: "tu1",
			ToolName:  "bash",
			Input:     json.RawMessage(`{"cmd":"ls"}`),
			Reason:    "list files",
			AskUser:   &AskUserPayload{Question: "ok?", Suggestions: []string{"yes", "no"}},
		})
		close(done)
	}()
	waitForPending(t, b, 1)
	id := b.Pending()[0].ID
	_ = b.Resolve(id, "sess-1", DecisionDeny)
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(events) < 2 {
		t.Fatalf("events = %d, want at least 2", len(events))
	}
	if events[0].Kind != EventRequested {
		t.Fatalf("first event = %q", events[0].Kind)
	}
	if events[len(events)-1].Kind != EventResolved || events[len(events)-1].Decision != DecisionDeny {
		t.Fatalf("last event = %+v", events[len(events)-1])
	}

	audit := b.Audit()
	if len(audit) < 2 {
		t.Fatalf("audit entries = %d", len(audit))
	}
	foundRequested := false
	foundResolved := false
	for _, entry := range audit {
		if entry.Kind == AuditRequested {
			foundRequested = true
			if entry.SessionID != "sess-1" || entry.ToolName != "bash" {
				t.Fatalf("audit requested = %+v", entry)
			}
		}
		if entry.Kind == AuditResolved && entry.Decision == DecisionDeny {
			foundResolved = true
		}
		if entry.Timestamp.IsZero() {
			t.Fatalf("audit timestamp missing: %+v", entry)
		}
	}
	if !foundRequested || !foundResolved {
		t.Fatalf("audit coverage requested=%v resolved=%v", foundRequested, foundResolved)
	}
}

func TestAuditTimeoutAndCancelled(t *testing.T) {
	b := New(testLimits())

	_, _ = b.Request(context.Background(), RequestParams{
		SessionID: "sess-1",
		ToolUseID: "tu1",
		ToolName:  "bash",
		Timeout:   20 * time.Millisecond,
	})

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		for _, entry := range b.Audit() {
			if entry.Kind == AuditTimedOut {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expected timed_out audit entry")
}

func TestRequestReturnsRequestID(t *testing.T) {
	b := New(testLimits())
	ctx := context.Background()

	done := make(chan Result, 1)
	go func() {
		result, err := b.Request(ctx, RequestParams{
			SessionID: "sess-1",
			ToolUseID: "tu1",
			ToolName:  "bash",
		})
		if err != nil {
			t.Errorf("Request: %v", err)
			done <- Result{}
			return
		}
		done <- result
	}()
	waitForPending(t, b, 1)
	brokerID := b.Pending()[0].ID
	if brokerID == "" {
		t.Fatal("broker request id is empty")
	}
	if err := b.Resolve(brokerID, "sess-1", DecisionAllowOnce); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	result := <-done
	if result.RequestID != brokerID {
		t.Fatalf("request id = %q, want %q", result.RequestID, brokerID)
	}
	if result.Decision != DecisionAllowOnce {
		t.Fatalf("decision = %q", result.Decision)
	}
}

func TestAuditEntryJSONExcludesSensitiveInput(t *testing.T) {
	b := New(testLimits())
	ctx := context.Background()
	sensitive := "rm -rf /tmp/secret"
	input := json.RawMessage(`{"cmd":"` + sensitive + `"}`)

	done := make(chan struct{})
	go func() {
		_, _ = b.Request(ctx, RequestParams{
			SessionID: "sess-1",
			ToolUseID: "tu1",
			ToolName:  "bash",
			Input:     input,
		})
		close(done)
	}()
	waitForPending(t, b, 1)
	id := b.Pending()[0].ID
	if err := b.Resolve(id, "sess-1", DecisionAllowOnce); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	<-done

	for _, entry := range b.Audit() {
		raw, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal audit entry: %v", err)
		}
		s := string(raw)
		if strings.Contains(s, sensitive) || strings.Contains(s, `"Input"`) {
			t.Fatalf("audit must not include tool input: %s", s)
		}
	}

	go func() {
		_, _ = b.Request(ctx, RequestParams{
			SessionID: "sess-2",
			ToolUseID: "tu2",
			ToolName:  "bash",
			Input:     input,
		})
	}()
	waitForPending(t, b, 1)
	if !strings.Contains(string(b.Pending()[0].Input), sensitive) {
		t.Fatalf("Request.Input must remain for UI: %s", b.Pending()[0].Input)
	}
}

func waitForPending(t *testing.T, b *Broker, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(b.Pending()) >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("pending count = %d, want %d", len(b.Pending()), want)
}
