package contextcompress

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/session"
)

// fakeJev scores a candidate 4 when it contains "RELEVANT", else 0. It records
// every state so tests can assert what left the machine.
func fakeJev(t *testing.T, confidence float64, status int) (*jev.Client, *atomic.Int32, *[]string) {
	t.Helper()
	var calls atomic.Int32
	var states []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		var req struct {
			State     string                     `json:"state"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		states = append(states, req.State)
		mu.Unlock()
		answers := map[string]interface{}{}
		for key := range req.Questions {
			start := strings.Index(req.State, key+":\n")
			body := req.State[start+len(key)+2:]
			if next := strings.Index(body, "\nitem_"); next >= 0 {
				body = body[:next]
			}
			score := 0.0
			if strings.Contains(body, "RELEVANT") {
				score = 4
			}
			answers[key] = map[string]interface{}{"type": "score", "score": score, "confidence": confidence}
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"answers": answers})
	}))
	t.Cleanup(server.Close)
	return jev.NewClient("test-key", "", server.URL), &calls, &states
}

func gateSession() *session.Session {
	sess := session.New("jev-gate")
	sess.AddUserMessage("Find why the payment webhook fails")
	return sess
}

func numberedOutput(lines int, mark func(int) string) string {
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&b, "line %04d filler text for the admission gate %s\n", i, mark(i))
	}
	return b.String()
}

func TestRelevanceGateKeepsRelevantChunksHeadTailAndManifest(t *testing.T) {
	client, calls, _ := fakeJev(t, .95, http.StatusOK)
	c := NewCompressor(Config{Enabled: true})
	sess := gateSession()
	output := numberedOutput(600, func(i int) string {
		if i == 300 {
			return "RELEVANT webhook signature mismatch"
		}
		return ""
	})
	tr := llm.ToolResult{ToolCallID: "call-1", Name: "bash", Content: output}

	got := c.AdmitToolResultWithGate(context.Background(), sess, tr, 8000, &RelevanceGate{Client: client})

	if calls.Load() == 0 {
		t.Fatal("expected Jev to be called")
	}
	if !strings.HasPrefix(got.Content, "[brute-compressed kind=jev_relevance tool=bash hash=") {
		t.Fatalf("missing marker: %.200s", got.Content)
	}
	for _, want := range []string{"line 0001 ", "line 0600 ", "RELEVANT webhook signature mismatch", "Dropped line ranges:"} {
		if !strings.Contains(got.Content, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(got.Content, "line 0150 ") {
		t.Error("irrelevant middle chunk should be dropped")
	}
	if len(got.Content) >= len(output)/2 {
		t.Errorf("expected a large reduction, got %d of %d bytes", len(got.Content), len(output))
	}
	hash := regexp.MustCompile(`hash=([0-9a-f]+)`).FindStringSubmatch(got.Content)[1]
	if original, ok := c.Retrieve(sess.ID, hash, ""); !ok || original != output {
		t.Fatal("original must be retrievable with context_retrieve")
	}
	// Request-time compression must not replace the gated text with a bash preview.
	req := &llm.ChatRequest{Messages: []llm.Message{{Role: "tool", ToolResults: []llm.ToolResult{got}}}}
	out, _ := c.CompressRequest(context.Background(), sess.ID, req)
	if out.Messages[0].ToolResults[0].Content != got.Content {
		t.Fatal("request compression rewrote a Jev-gated result")
	}
}

func TestRelevanceGateFailsOpenToAdmissionCap(t *testing.T) {
	output := numberedOutput(600, func(int) string { return "" })
	for name, tc := range map[string]struct {
		confidence float64
		status     int
		nilClient  bool
	}{
		"low confidence": {confidence: .5, status: http.StatusOK},
		"server error":   {confidence: .95, status: http.StatusInternalServerError},
		"no credentials": {nilClient: true},
	} {
		t.Run(name, func(t *testing.T) {
			client, _, _ := fakeJev(t, tc.confidence, tc.status)
			gate := &RelevanceGate{Client: client}
			if tc.nilClient {
				gate.Client = nil
			}
			tr := llm.ToolResult{ToolCallID: "call-1", Name: "read", Content: output}
			got := NewCompressor(Config{Enabled: true}).AdmitToolResultWithGate(context.Background(), gateSession(), tr, 8000, gate)
			want := NewCompressor(Config{Enabled: true}).AdmitToolResult(gateSession(), tr, 8000)
			if got.Content != want.Content {
				t.Fatalf("expected plain admission fallback, got %.200s", got.Content)
			}
		})
	}
}

func TestRelevanceGateSkipsSmallAndUnsupportedResults(t *testing.T) {
	client, calls, _ := fakeJev(t, .95, http.StatusOK)
	c := NewCompressor(Config{Enabled: true})
	small := llm.ToolResult{Name: "bash", Content: numberedOutput(20, func(int) string { return "" })}
	if got := c.AdmitToolResultWithGate(context.Background(), gateSession(), small, 8000, &RelevanceGate{Client: client}); got.Content != small.Content {
		t.Fatal("small result must stay verbatim")
	}
	other := llm.ToolResult{Name: "fetch_url", Content: numberedOutput(600, func(int) string { return "" })}
	c.AdmitToolResultWithGate(context.Background(), gateSession(), other, 8000, &RelevanceGate{Client: client})
	failed := llm.ToolResult{Name: "bash", IsError: true, Content: other.Content}
	c.AdmitToolResultWithGate(context.Background(), gateSession(), failed, 8000, &RelevanceGate{Client: client})
	if calls.Load() != 0 {
		t.Fatalf("unexpected Jev calls: %d", calls.Load())
	}
}

func TestRelevanceGateKeepsSecretsLocalAndDiagnostics(t *testing.T) {
	client, _, states := fakeJev(t, .95, http.StatusOK)
	c := NewCompressor(Config{Enabled: true})
	output := numberedOutput(600, func(i int) string {
		switch i {
		case 200:
			return "api_key=sk-livesecretvalue1234567890"
		case 400:
			return "panic: runtime error: index out of range"
		}
		return ""
	})
	tr := llm.ToolResult{ToolCallID: "call-1", Name: "bash", Content: output}
	got := c.AdmitToolResultWithGate(context.Background(), gateSession(), tr, 8000, &RelevanceGate{Client: client})
	for _, state := range *states {
		if strings.Contains(state, "sk-livesecretvalue") {
			t.Fatal("secret-bearing chunk was sent to Jev")
		}
		if strings.Contains(state, "panic: runtime error") {
			t.Fatal("diagnostic chunks are always kept and need no scoring")
		}
	}
	for _, want := range []string{"api_key=sk-livesecretvalue", "panic: runtime error"} {
		if !strings.Contains(got.Content, want) {
			t.Errorf("chunk with %q must be kept verbatim", want)
		}
	}
}
