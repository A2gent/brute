package cursorcli

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/A2gent/brute/internal/llm"
)

// The fixture is a trimmed real `agent --output-format stream-json
// --stream-partial-output` capture containing glob/shell/read tool calls.
func TestToolEmitterFromFixture(t *testing.T) {
	f, err := os.Open("testdata/stream_tools.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var events []llm.StreamEvent
	em := newToolEmitter(func(ev llm.StreamEvent) error { events = append(events, ev); return nil })
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		env, err := parseCLIStreamEnvelope(sc.Text())
		if err != nil {
			t.Fatal(err)
		}
		if err := em.handle(env); err != nil {
			t.Fatal(err)
		}
	}

	counts := map[llm.StreamEventType]int{}
	names := map[string]bool{}
	var reasoning strings.Builder
	var readErr, shellOK bool
	for _, ev := range events {
		counts[ev.Type]++
		names[ev.ToolCallName] = true
		if ev.Type == llm.StreamEventReasoningDelta {
			reasoning.WriteString(ev.ReasoningDelta)
		}
		if ev.Type == llm.StreamEventToolOutput && ev.ToolCallName == "read" && ev.ToolIsError && ev.ToolOutput == "File not found" {
			readErr = true
		}
		if ev.Type == llm.StreamEventToolOutput && ev.ToolCallName == "shell" && !ev.ToolIsError {
			shellOK = true
		}
	}
	if counts[llm.StreamEventToolStarted] == 0 || counts[llm.StreamEventToolStarted] != counts[llm.StreamEventToolCompleted] {
		t.Fatalf("started/completed mismatch: %v", counts)
	}
	if counts[llm.StreamEventToolOutput] != counts[llm.StreamEventToolCompleted] {
		t.Fatalf("output/completed mismatch: %v", counts)
	}
	for _, n := range []string{"glob", "shell", "read"} {
		if !names[n] {
			t.Errorf("missing tool %q in %v", n, names)
		}
	}
	if !readErr || !shellOK {
		t.Errorf("readErr=%v shellOK=%v", readErr, shellOK)
	}
	if !strings.Contains(reasoning.String(), "Listing workspace files") {
		t.Errorf("reasoning = %q", reasoning.String())
	}
}
