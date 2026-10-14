package contextcompress

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/session"
)

// TestJevAdmissionReplay measures the production gate against real Jev on
// archived results exported by `scripts/jev_baseline.py --gate-candidates`.
// It sends archive text (minus guarded secrets) to Jev, so it only runs when
// both JEV_GATE_CANDIDATES and TYPESAFE_API_KEY are set explicitly.
func TestJevAdmissionReplay(t *testing.T) {
	path, key := os.Getenv("JEV_GATE_CANDIDATES"), os.Getenv("TYPESAFE_API_KEY")
	if path == "" || key == "" {
		t.Skip("set JEV_GATE_CANDIDATES and TYPESAFE_API_KEY to replay archived results")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gate := &RelevanceGate{Client: jev.NewClient(key, os.Getenv("TYPESAFE_MODEL"), os.Getenv("TYPESAFE_BASE_URL"))}
	type totals struct{ calls, gated, before, after, weightedBefore, weightedAfter int }
	perTool := map[string]*totals{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	for scanner.Scan() {
		var row struct {
			Tool, Task, Content string
			Weight              int
		}
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			continue
		}
		sess := session.New("replay")
		sess.AddUserMessage(row.Task)
		c := NewCompressor(Config{Enabled: true})
		tr := llm.ToolResult{ToolCallID: "replay", Name: row.Tool, Content: row.Content}
		before := (utf8.RuneCountInString(c.AdmitToolResult(sess, tr, DefaultAdmissionMaxTokens).Content) + 3) / 4
		got, gated := c.gateRelevance(context.Background(), sess, tr, DefaultAdmissionMaxTokens, gate)
		after := before
		if gated {
			after = (utf8.RuneCountInString(got.Content) + 3) / 4
		}
		total := perTool[row.Tool]
		if total == nil {
			total = &totals{}
			perTool[row.Tool] = total
		}
		total.calls++
		if gated {
			total.gated++
		}
		total.before += before
		total.after += after
		total.weightedBefore += before * row.Weight
		total.weightedAfter += after * row.Weight
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Log("tool\tcalls\tgated\tadmitted_tokens\tgated_tokens\tweighted_before\tweighted_after")
	for tool, total := range perTool {
		t.Logf("%s\t%d\t%d\t%d\t%d\t%d\t%d", tool, total.calls, total.gated, total.before, total.after, total.weightedBefore, total.weightedAfter)
	}
}
