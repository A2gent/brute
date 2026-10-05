package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/A2gent/brute/internal/llm"
)

const (
	readCacheMaxBodyBytes = 256 * 1024
	readCacheMaxBytes     = 16 * 1024 * 1024
	readCacheMaxSessions  = 128
	readCacheMaxRanges    = 256
)

type readCacheKey struct {
	path        string
	start, end  int
	lineNumbers bool
}

type readCacheEntry struct {
	fileHash, outputHash [sha256.Size]byte
	callID               string
	lines                int
	visible              bool
	output               string
}

type readSessionCache struct {
	mu       sync.Mutex
	sessions map[string]map[readCacheKey]readCacheEntry
	bytes    int
}

func readCacheSession(ctx context.Context) string {
	// Pipeline intermediate outputs are hidden, and downstream stages need data,
	// not a conversational reference. Never deduplicate reads in a pipeline.
	if ctx.Value(pipelineContextKey{}) != nil {
		return ""
	}
	id, _ := ctx.Value("session_id").(string)
	return id
}

func (t *ReadTool) cachedRead(sessionID string, key readCacheKey, hash [sha256.Size]byte) *Result {
	if sessionID == "" {
		return nil
	}
	t.cache.mu.Lock()
	defer t.cache.mu.Unlock()
	entry, ok := t.cache.sessions[sessionID][key]
	if !ok || !entry.visible || entry.fileHash != hash {
		return nil
	}
	stub := readStub(key, entry)
	return &Result{Success: true, Output: stub, Metadata: map[string]interface{}{
		"read_cache_reference": map[string]interface{}{"stub": stub, "body": entry.output, "call_id": entry.callID},
	}}
}

func readStub(key readCacheKey, entry readCacheEntry) string {
	return fmt.Sprintf("[unchanged since earlier read in this session, %d lines; %s lines %d-%d. Full content is earlier in the conversation in tool result %s. Call read with the same path/range and force: true to get the full body.]", entry.lines, key.path, key.start, key.end, entry.callID)
}

func (t *ReadTool) rememberRead(ctx context.Context, key readCacheKey, hash [sha256.Size]byte, output string, lines int) {
	sessionID := readCacheSession(ctx)
	callID, _ := ctx.Value("tool_call_id").(string)
	if sessionID == "" || callID == "" {
		return
	}
	t.cache.mu.Lock()
	defer t.cache.mu.Unlock()
	// Refuse new entries rather than evicting originals that outstanding stubs
	// may still reference. This bounds memory without compromising restoration.
	old := t.cache.sessions[sessionID][key]
	if len(output) > readCacheMaxBodyBytes || t.cache.bytes-len(old.output)+len(output) > readCacheMaxBytes ||
		(len(t.cache.sessions) >= readCacheMaxSessions && t.cache.sessions[sessionID] == nil) ||
		(len(t.cache.sessions[sessionID]) >= readCacheMaxRanges && old.callID == "") {
		return
	}
	t.cache.bytes += len(output) - len(old.output)
	if t.cache.sessions == nil {
		t.cache.sessions = make(map[string]map[readCacheKey]readCacheEntry)
	}
	if t.cache.sessions[sessionID] == nil {
		t.cache.sessions[sessionID] = make(map[readCacheKey]readCacheEntry)
	}
	// Pending results are not safe references until request construction confirms
	// the full body survived wrapping, truncation, sanitization and compression.
	t.cache.sessions[sessionID][key] = readCacheEntry{fileHash: hash, outputHash: sha256.Sum256([]byte(output)), callID: callID, lines: lines, output: output}
}

// SyncContext invalidates reads whose original full output is no longer in the
// final model request. It intentionally runs after compression and compaction.
func (t *ReadTool) SyncContext(sessionID string, messages []llm.Message) map[string]string {
	t.cache.mu.Lock()
	defer t.cache.mu.Unlock()
	entries := t.cache.sessions[sessionID]

	visible := make(map[string]map[[sha256.Size]byte]bool)
	for _, msg := range messages {
		for _, result := range msg.ToolResults {
			if result.IsError {
				continue
			}
			outputs := []string{}
			switch normalizeToolName(result.Name) {
			case "read":
				outputs = append(outputs, result.Content)
			case "parallel":
				var steps []parallelStepOutput
				if json.Unmarshal([]byte(result.Content), &steps) == nil {
					for _, step := range steps {
						if step.Success && normalizeToolName(step.Tool) == "read" {
							outputs = append(outputs, step.Output)
						}
					}
				}
			}
			if visible[result.ToolCallID] == nil {
				visible[result.ToolCallID] = make(map[[sha256.Size]byte]bool)
			}
			for _, output := range outputs {
				visible[result.ToolCallID][sha256.Sum256([]byte(output))] = true
			}
		}
	}

	// References carry their captured body in metadata, not model-visible text.
	// This survives cache eviction/overwriting and compaction between tool runs.
	repairs := make(map[string]string)
	for mi := range messages {
		for ri := range messages[mi].ToolResults {
			tr := &messages[mi].ToolResults[ri]
			if normalizeToolName(tr.Name) == "read" {
				if body, ok := repairReadReference(tr.Content, tr.Metadata, visible); ok {
					tr.Content = body
					repairs[tr.ToolCallID] = body
				}
			}
			if normalizeToolName(tr.Name) == "parallel" {
				var steps []parallelStepOutput
				if json.Unmarshal([]byte(tr.Content), &steps) != nil {
					continue
				}
				changed := false
				refs, _ := tr.Metadata["read_cache_references"].(map[string]interface{})
				for i := range steps {
					metadata := map[string]interface{}{"read_cache_reference": refs[fmt.Sprint(steps[i].Step)]}
					if body, ok := repairReadReference(steps[i].Output, metadata, visible); ok {
						steps[i].Output = body
						changed = true
					}
				}
				if changed {
					raw, _ := json.MarshalIndent(steps, "", "  ")
					tr.Content = string(raw)
					repairs[tr.ToolCallID] = tr.Content
				}
			}
		}
	}

	for key, entry := range entries {
		if !visible[entry.callID][entry.outputHash] {
			t.cache.bytes -= len(entry.output)
			delete(entries, key)
			continue
		}
		entry.visible = true
		entries[key] = entry
	}
	if len(entries) == 0 {
		delete(t.cache.sessions, sessionID)
	}
	return repairs
}

func repairReadReference(output string, metadata map[string]interface{}, visible map[string]map[[sha256.Size]byte]bool) (string, bool) {
	ref, ok := metadata["read_cache_reference"].(map[string]interface{})
	if !ok {
		return "", false
	}
	stub, _ := ref["stub"].(string)
	body, _ := ref["body"].(string)
	callID, _ := ref["call_id"].(string)
	if output != stub || visible[callID][sha256.Sum256([]byte(body))] {
		return "", false
	}
	return body, true
}
