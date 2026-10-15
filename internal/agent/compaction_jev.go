package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/contextcompress"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/session"
)

const (
	// Smaller tool outputs cost little to summarize and are not worth a classifier call.
	compactionJevMinBytes = 2000
	// Bounds classifier fan-out on the compaction path; the largest outputs are scored first.
	compactionJevMaxCandidates = 60
	compactionJevTimeout       = 20 * time.Second
)

// compactionJevStats describes Jev history filtering. Used is true only when Jev
// actually scored candidates; dropped outputs were replaced before summarization.
type compactionJevStats struct {
	Used         bool
	Scored       int
	Dropped      int
	DroppedBytes int
}

func (s compactionJevStats) metadata() map[string]interface{} {
	return map[string]interface{}{"jev_used": true, "jev_scored": s.Scored, "jev_dropped": s.Dropped, "jev_dropped_bytes": s.DroppedBytes}
}

// compactionRelevanceTask ignores synthetic "continue" prompts left by earlier
// compactions; scoring history against them would drop useful outputs.
func compactionRelevanceTask(sess *session.Session) string {
	var view session.Session
	for _, msg := range sess.Messages {
		if msg.Role == "user" && (msg.Metadata["synthetic_continuation"] == true || msg.Content == transitionContinuationPrompt) {
			continue
		}
		view.Messages = append(view.Messages, msg)
	}
	return contextcompress.RelevanceTask(&view)
}

// filterHistoryWithJev drops large tool outputs that Jev scores as irrelevant to
// the current task, so the summarizer spends its budget on what matters. User and
// assistant text is never dropped. Any failure returns messages unchanged.
func filterHistoryWithJev(ctx context.Context, gate *contextcompress.RelevanceGate, task string, messages []session.Message) ([]session.Message, compactionJevStats) {
	var stats compactionJevStats
	if gate == nil || gate.Client == nil || task == "" || !jev.SafeEgressText(task, gate.Secrets) {
		return messages, stats
	}
	type ref struct{ msg, result int }
	var refs []ref
	for i, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		for j, tr := range msg.ToolResults {
			c := tr.Content
			if tr.IsError || len(c) < compactionJevMinBytes || !utf8.ValidString(c) || strings.Contains(c, "base64") || !jev.SafeEgressText(c, gate.Secrets) {
				continue
			}
			refs = append(refs, ref{i, j})
		}
	}
	if len(refs) == 0 {
		return messages, stats
	}
	size := func(r ref) int { return len(messages[r.msg].ToolResults[r.result].Content) }
	sort.SliceStable(refs, func(a, b int) bool { return size(refs[a]) > size(refs[b]) })
	if len(refs) > compactionJevMaxCandidates {
		refs = refs[:compactionJevMaxCandidates]
	}
	candidates := make([]string, len(refs))
	for i, r := range refs {
		tr := messages[r.msg].ToolResults[r.result]
		// Jev caps each candidate, so only the output head is scored.
		candidates[i] = "Output of tool " + tr.Name + ":\n" + tr.Content
	}
	threshold, minConfidence := gate.Policy()
	scoreCtx, cancel := context.WithTimeout(ctx, compactionJevTimeout)
	defer cancel()
	scores, err := jev.ScoreRelevance(scoreCtx, gate.Client, task, candidates, minConfidence)
	if err != nil {
		return messages, stats
	}
	stats.Used, stats.Scored = true, len(refs)
	// Copy-on-write: the input shares backing arrays with the live session.
	filtered := append([]session.Message(nil), messages...)
	copied := map[int]bool{}
	for i, r := range refs {
		if scores[i] >= threshold {
			continue
		}
		if !copied[r.msg] {
			filtered[r.msg].ToolResults = append([]session.ToolResult(nil), messages[r.msg].ToolResults...)
			copied[r.msg] = true
		}
		tr := &filtered[r.msg].ToolResults[r.result]
		stats.Dropped++
		stats.DroppedBytes += len(tr.Content)
		tr.Content = fmt.Sprintf("[tool output omitted before summarization: Jev relevance score %.1f, %d bytes]", scores[i], len(tr.Content))
	}
	return filtered, stats
}
