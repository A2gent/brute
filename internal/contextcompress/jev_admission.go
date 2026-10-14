package contextcompress

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/llm/jev"
	"github.com/A2gent/brute/internal/session"
)

const (
	DefaultRelevanceGateMinTokens = 2000
	relevanceChunkLines           = 40
	relevanceChunkBytes           = 3000
	// More chunks would mean many classifier calls on the agent's critical path;
	// such outputs fall back to the deterministic admission cap instead.
	relevanceMaxChunks = 60
	relevanceTimeout   = 20 * time.Second
)

// RelevanceGate opts admission into Jev scoring of large bash/grep/read results.
// A nil gate or nil Client disables it; zero numeric fields use defaults.
type RelevanceGate struct {
	Client        *jev.Client
	MinTokens     int
	Threshold     float64
	MinConfidence float64
	Secrets       []string
}

type relevanceChunk struct {
	text             string
	firstLine, lines int
	keep, scored     bool
	score            float64
}

// AdmitToolResultWithGate tries Jev relevance gating first and otherwise behaves
// exactly like AdmitToolResult. Every failure path keeps the deterministic cap.
func (c *Compressor) AdmitToolResultWithGate(ctx context.Context, sess *session.Session, tr llm.ToolResult, maxTokens int, gate *RelevanceGate) llm.ToolResult {
	if gated, ok := c.gateRelevance(ctx, sess, tr, maxTokens, gate); ok {
		return gated
	}
	return c.AdmitToolResult(sess, tr, maxTokens)
}

func (c *Compressor) gateRelevance(ctx context.Context, sess *session.Session, tr llm.ToolResult, maxTokens int, gate *RelevanceGate) (llm.ToolResult, bool) {
	if gate == nil || gate.Client == nil || sess == nil || tr.IsError || !relevanceGatedTool(tr.Name) {
		return tr, false
	}
	minTokens := gate.MinTokens
	if minTokens <= 0 {
		minTokens = DefaultRelevanceGateMinTokens
	}
	originalTokens := (utf8.RuneCountInString(tr.Content) + 3) / 4
	// Embedded image payloads are moved out by AdmitToolResult; never score them.
	if originalTokens <= minTokens || strings.HasPrefix(tr.Content, "[brute-compressed ") || !utf8.ValidString(tr.Content) || strings.Contains(tr.Content, "base64") {
		return tr, false
	}
	task := relevanceTask(sess)
	if task == "" || !jev.SafeEgressText(task, gate.Secrets) {
		return tr, false
	}
	chunks, ok := splitRelevanceChunks(tr.Content)
	if !ok || len(chunks) < 3 {
		return tr, false
	}
	// Head/tail, bash diagnostics and anything the egress guard rejects stay verbatim
	// and are never sent. Only the remaining chunks are scored.
	var candidates []string
	var indices []int
	for i := range chunks {
		chunk := &chunks[i]
		if i == 0 || i == len(chunks)-1 || (tr.Name == "bash" && relevanceProtected(chunk.text)) || !jev.SafeEgressText(chunk.text, gate.Secrets) {
			chunk.keep = true
			continue
		}
		candidates = append(candidates, chunk.text)
		indices = append(indices, i)
	}
	if len(candidates) == 0 {
		return tr, false
	}
	threshold := gate.Threshold
	if threshold <= 0 || threshold > 4 || math.IsNaN(threshold) {
		threshold = 2
	}
	minConfidence := gate.MinConfidence
	if minConfidence <= 0 || minConfidence > 1 || math.IsNaN(minConfidence) {
		minConfidence = .85
	}
	scoreCtx, cancel := context.WithTimeout(ctx, relevanceTimeout)
	defer cancel()
	scores, err := jev.ScoreRelevance(scoreCtx, gate.Client, task, candidates, minConfidence)
	if err != nil {
		return tr, false
	}
	dropped := 0
	for i, index := range indices {
		chunks[index].scored, chunks[index].score = true, scores[i]
		chunks[index].keep = scores[i] >= threshold
		if !chunks[index].keep {
			dropped++
		}
	}
	if dropped == 0 {
		return tr, false
	}
	value := entry{SessionID: sess.ID, ToolName: tr.Name, ToolCallID: tr.ToolCallID, Original: tr.Content}
	value.Hash = c.store.put(sess.ID, value)
	value.Compressed = renderRelevanceChunks(value.Hash, tr.Name, originalTokens, chunks)
	// A gated result that still exceeds the cap gains nothing over the cap's own
	// excerpt; let the deterministic path handle it.
	if (utf8.RuneCountInString(value.Compressed)+3)/4 > maxTokens || len(value.Compressed) >= len(tr.Content) {
		return tr, false
	}
	c.store.put(sess.ID, value)
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}
	stored := readSessionEntries(sess.Metadata)
	stored[value.Hash] = value
	sess.Metadata[sessionCCRMetadataKey] = encodeSessionEntries(stored)
	tr.Content = value.Compressed
	return tr, true
}

func relevanceGatedTool(name string) bool {
	return name == "bash" || name == "grep" || name == "read"
}

// The current task is the latest user request plus the latest assistant text
// (usually the plan that triggered this tool call).
func relevanceTask(sess *session.Session) string {
	var user, assistant string
	for i := len(sess.Messages) - 1; i >= 0 && (user == "" || assistant == ""); i-- {
		content := strings.TrimSpace(sess.Messages[i].Content)
		switch {
		case sess.Messages[i].Role == "user" && user == "":
			user = content
		case sess.Messages[i].Role == "assistant" && assistant == "" && user == "":
			assistant = content
		}
	}
	if user == "" {
		return ""
	}
	task := "User request:\n" + user
	if assistant != "" {
		task += "\nAssistant plan:\n" + assistant
	}
	if len(task) > jev.RelevanceTaskBytes {
		// Keep the end: the newest plan text is the most specific signal.
		task = task[len(task)-jev.RelevanceTaskBytes:]
		for len(task) > 0 && !utf8.RuneStart(task[0]) {
			task = task[1:]
		}
	}
	return task
}

// splitRelevanceChunks cuts on line boundaries so dropped ranges can be named
// by line number. An overlong line cannot be scored whole, so it aborts gating.
func splitRelevanceChunks(content string) ([]relevanceChunk, bool) {
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var chunks []relevanceChunk
	var current relevanceChunk
	var body strings.Builder
	for i, line := range lines {
		if len(line) > jev.RelevanceCandidateBytes {
			return nil, false
		}
		if current.lines > 0 && (current.lines >= relevanceChunkLines || body.Len()+len(line) > relevanceChunkBytes) {
			current.text = body.String()
			chunks = append(chunks, current)
			current, body = relevanceChunk{}, strings.Builder{}
		}
		if current.lines == 0 {
			current.firstLine = i + 1
		}
		body.WriteString(line)
		current.lines++
	}
	if current.lines > 0 {
		current.text = body.String()
		chunks = append(chunks, current)
	}
	return chunks, len(chunks) <= relevanceMaxChunks
}

// Only bash is protected: source files and grep hits mention "error" in nearly
// every chunk, which would disable gating for read/grep.
func relevanceProtected(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if isImportantLine(line) || strings.HasPrefix(line, "Exit code:") {
			return true
		}
	}
	return false
}

func renderRelevanceChunks(hash, toolName string, originalTokens int, chunks []relevanceChunk) string {
	var body, manifest strings.Builder
	kept, total := 0, 0
	for _, chunk := range chunks {
		total += chunk.lines
		if chunk.keep {
			kept += chunk.lines
			body.WriteString(chunk.text)
			if !strings.HasSuffix(chunk.text, "\n") {
				body.WriteByte('\n')
			}
			continue
		}
		last := chunk.firstLine + chunk.lines - 1
		fmt.Fprintf(&body, "... [lines %d-%d omitted, jev score %.1f] ...\n", chunk.firstLine, last, chunk.score)
		if manifest.Len() > 0 {
			manifest.WriteString(", ")
		}
		fmt.Fprintf(&manifest, "%d-%d", chunk.firstLine, last)
	}
	return fmt.Sprintf("[brute-compressed kind=jev_relevance tool=%s hash=%s original_tokens_approx=%d kept_lines=%d/%d]\nOnly chunks relevant to the current task were kept. Use context_retrieve with this hash and a query for omitted lines.\n\n%sDropped line ranges: %s\n", toolName, hash, originalTokens, kept, total, body.String(), manifest.String())
}
