package contextcompress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
)

const (
	RetrievalToolName     = "context_retrieve"
	defaultMinChars       = 6000
	maxPreviewLines       = 120
	sessionCCRMetadataKey = "context_compression_store"
)

type Config struct {
	Enabled  bool
	MinChars int
}

type SessionStore interface {
	Get(id string) (*session.Session, error)
	Save(sess *session.Session) error
}

type Compressor struct {
	config       Config
	store        *Store
	sessionStore SessionStore
}

type Result struct {
	Applied bool
	Items   []Item
}

type Item struct {
	Hash          string
	ToolName      string
	ToolCallID    string
	OriginalChars int
	ShownChars    int
}

type entry struct {
	SessionID  string
	Hash       string
	ToolName   string
	ToolCallID string
	Original   string
	Compressed string
}

type Store struct {
	mu      sync.RWMutex
	entries map[string]entry
}

func NewStore() *Store {
	return &Store{entries: make(map[string]entry)}
}

func NewCompressor(config Config) *Compressor {
	return NewCompressorWithStores(config, NewStore(), nil)
}

func NewCompressorWithStore(config Config, store *Store) *Compressor {
	return NewCompressorWithStores(config, store, nil)
}

func NewCompressorWithSessionStore(config Config, sessionStore SessionStore) *Compressor {
	return NewCompressorWithStores(config, NewStore(), sessionStore)
}

func NewCompressorWithStores(config Config, store *Store, sessionStore SessionStore) *Compressor {
	if store == nil {
		store = NewStore()
	}
	return &Compressor{config: config, store: store, sessionStore: sessionStore}
}

func (c *Compressor) CompressRequest(_ context.Context, sessionID string, req *llm.ChatRequest) (*llm.ChatRequest, Result) {
	if req == nil {
		return req, Result{}
	}
	if c == nil || !c.config.Enabled {
		return cloneRequest(req), Result{}
	}

	out := cloneRequest(req)
	minChars := c.config.MinChars
	if minChars <= 0 {
		minChars = defaultMinChars
	}

	result := Result{}
	persist := make([]entry, 0)
	for msgIdx := range out.Messages {
		msg := &out.Messages[msgIdx]
		if msg.Role != "tool" || len(msg.ToolResults) == 0 {
			continue
		}
		for resultIdx := range msg.ToolResults {
			tr := &msg.ToolResults[resultIdx]
			if !shouldCompressToolResult(*tr, minChars) {
				continue
			}
			compressed := compressToolContent(tr.Name, tr.Content)
			if len(compressed) >= len(tr.Content) {
				continue
			}
			stored := entry{
				SessionID:  sessionID,
				ToolName:   tr.Name,
				ToolCallID: tr.ToolCallID,
				Original:   tr.Content,
				Compressed: compressed,
			}
			hash := c.store.put(sessionID, stored)
			stored.Hash = hash
			persist = append(persist, stored)
			tr.Content = formatCompressedMarker(hash, tr.Name, len(stored.Original), compressed)
			result.Applied = true
			result.Items = append(result.Items, Item{
				Hash:          hash,
				ToolName:      tr.Name,
				ToolCallID:    tr.ToolCallID,
				OriginalChars: len(stored.Original),
				ShownChars:    len(tr.Content),
			})
		}
	}

	if len(persist) > 0 {
		c.persistEntries(sessionID, persist)
	}
	if result.Applied {
		out.SystemPrompt = appendRetrievalInstructions(out.SystemPrompt)
		out.Tools = ensureRetrievalTool(out.Tools)
	}
	return out, result
}

func (c *Compressor) Retrieve(sessionID, hash, query string) (string, bool) {
	if c == nil || c.store == nil {
		return "", false
	}
	if content, ok := c.store.retrieve(sessionID, hash, query); ok {
		return content, true
	}
	value, ok := c.retrievePersistedEntry(sessionID, hash)
	if !ok {
		return "", false
	}
	c.store.put(sessionID, value)
	return queryEntry(value, query), true
}

func (c *Compressor) retrievePersistedEntry(sessionID, hash string) (entry, bool) {
	if c == nil || c.sessionStore == nil {
		return entry{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	hash = strings.TrimSpace(hash)
	if sessionID == "" || hash == "" {
		return entry{}, false
	}
	sess, err := c.sessionStore.Get(sessionID)
	if err != nil || sess == nil {
		return entry{}, false
	}
	stored := readSessionEntries(sess.Metadata)
	value, ok := stored[hash]
	return value, ok
}

func (c *Compressor) persistEntries(sessionID string, values []entry) {
	if c == nil || c.sessionStore == nil || len(values) == 0 {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	sess, err := c.sessionStore.Get(sessionID)
	if err != nil || sess == nil {
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}
	stored := readSessionEntries(sess.Metadata)
	for _, value := range values {
		stored[value.Hash] = value
	}
	// Store CCR entries in session metadata so retrieval remains session-scoped
	// across agent instances and process restarts.
	sess.Metadata[sessionCCRMetadataKey] = encodeSessionEntries(stored)
	_ = c.sessionStore.Save(sess)
}

func (s *Store) put(sessionID string, value entry) string {
	if s == nil {
		return ""
	}
	h := sha256.Sum256([]byte(sessionID + "\x00" + value.ToolCallID + "\x00" + value.ToolName + "\x00" + value.Original))
	hash := hex.EncodeToString(h[:])[:16]
	value.Hash = hash
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[storeKey(sessionID, hash)] = value
	return hash
}

func (s *Store) retrieve(sessionID, hash, query string) (string, bool) {
	value, ok := s.get(sessionID, hash)
	if !ok {
		return "", false
	}
	return queryEntry(value, query), true
}

func (s *Store) get(sessionID, hash string) (entry, bool) {
	if s == nil {
		return entry{}, false
	}
	s.mu.RLock()
	value, ok := s.entries[storeKey(sessionID, strings.TrimSpace(hash))]
	s.mu.RUnlock()
	return value, ok
}

func queryEntry(value entry, query string) string {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return value.Original
	}
	lines := strings.Split(value.Original, "\n")
	matches := make([]string, 0)
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), query) {
			matches = append(matches, fmt.Sprintf("%d:%s", i+1, line))
		}
	}
	if len(matches) == 0 {
		return fmt.Sprintf("No lines matched query %q in compressed %s output %s.", query, value.ToolName, value.Hash)
	}
	return strings.Join(matches, "\n")
}

func encodeSessionEntries(entries map[string]entry) map[string]interface{} {
	out := make(map[string]interface{}, len(entries))
	for hash, value := range entries {
		out[hash] = map[string]interface{}{
			"hash":         value.Hash,
			"tool_name":    value.ToolName,
			"tool_call_id": value.ToolCallID,
			"original":     value.Original,
			"compressed":   value.Compressed,
		}
	}
	return out
}

func readSessionEntries(metadata map[string]interface{}) map[string]entry {
	entries := make(map[string]entry)
	if metadata == nil {
		return entries
	}
	raw, ok := metadata[sessionCCRMetadataKey]
	if !ok || raw == nil {
		return entries
	}
	switch typed := raw.(type) {
	case map[string]entry:
		for hash, value := range typed {
			entries[hash] = value
		}
		return entries
	case map[string]interface{}:
		for hash, itemRaw := range typed {
			itemMap, ok := itemRaw.(map[string]interface{})
			if !ok {
				continue
			}
			value := entry{
				Hash:       stringValue(itemMap["hash"]),
				ToolName:   stringValue(itemMap["tool_name"]),
				ToolCallID: stringValue(itemMap["tool_call_id"]),
				Original:   stringValue(itemMap["original"]),
				Compressed: stringValue(itemMap["compressed"]),
			}
			if strings.TrimSpace(value.Hash) == "" {
				value.Hash = strings.TrimSpace(hash)
			}
			entries[strings.TrimSpace(hash)] = value
		}
	}
	return entries
}

func stringValue(raw interface{}) string {
	value, _ := raw.(string)
	return value
}

func storeKey(sessionID, hash string) string {
	return strings.TrimSpace(sessionID) + ":" + strings.TrimSpace(hash)
}

func shouldCompressToolResult(tr llm.ToolResult, minChars int) bool {
	if len(tr.Content) < minChars {
		return false
	}
	if tr.IsError && len(tr.Content) < minChars*2 {
		// Small/medium errors often need exact text for recovery; leave them verbatim.
		return false
	}
	name := strings.ToLower(strings.TrimSpace(tr.Name))
	if name == "" {
		return false
	}
	if excludedTool(name) {
		return false
	}
	return compressibleTool(name)
}

func excludedTool(name string) bool {
	switch name {
	case "read", "write", "edit", "replace_lines", "insert_lines", "question", "session_task_progress", "context_retrieve":
		return true
	default:
		return false
	}
}

func compressibleTool(name string) bool {
	switch name {
	case "bash", "code_execution", "grep", "content_search", "find_files", "filter", "fetch_url", "youtube_transcript", "exa_search", "brave_search_query", "tavily_search", "perplexity_search", "sql_query", "delegate_to_agent", "delegate_to_subagent", "delegate_to_external_agent":
		return true
	default:
		return false
	}
}

func compressToolContent(toolName, content string) string {
	if looksLikeSearchOutput(content) {
		return compressSearchOutput(content)
	}
	return compressLogLikeOutput(content)
}

func looksLikeSearchOutput(content string) bool {
	lines := strings.Split(content, "\n")
	matches := 0
	for _, line := range lines {
		if hasPathLinePattern(line) {
			matches++
			if matches >= 3 {
				return true
			}
		}
	}
	return false
}

func hasPathLinePattern(line string) bool {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 3 {
		return false
	}
	if parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, r := range parts[1] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func compressSearchOutput(content string) string {
	lines := strings.Split(content, "\n")
	selected := make(map[int]struct{})
	perFile := make(map[string][]int)
	for i, line := range lines {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) >= 3 {
			perFile[parts[0]] = append(perFile[parts[0]], i)
		}
		if isImportantLine(line) {
			selected[i] = struct{}{}
		}
	}
	for _, indexes := range perFile {
		limit := 3
		for i := 0; i < len(indexes) && i < limit; i++ {
			selected[indexes[i]] = struct{}{}
		}
		for i := len(indexes) - limit; i < len(indexes); i++ {
			if i >= 0 {
				selected[indexes[i]] = struct{}{}
			}
		}
	}
	return renderSelectedLines(lines, selected)
}

func compressLogLikeOutput(content string) string {
	lines := strings.Split(content, "\n")
	selected := make(map[int]struct{})
	for i := 0; i < len(lines) && i < 20; i++ {
		selected[i] = struct{}{}
	}
	for i := len(lines) - 40; i < len(lines); i++ {
		if i >= 0 {
			selected[i] = struct{}{}
		}
	}
	for i, line := range lines {
		if !isImportantLine(line) {
			continue
		}
		for j := i - 2; j <= i+4; j++ {
			if j >= 0 && j < len(lines) {
				selected[j] = struct{}{}
			}
		}
	}
	return renderSelectedLines(lines, selected)
}

func isImportantLine(line string) bool {
	lower := strings.ToLower(line)
	needles := []string{"error", "fatal", "panic", "failed", "failure", "exception", "traceback", "cannot", "undefined", "exit status", "fail:", " fail", "timeout"}
	for _, needle := range needles {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func renderSelectedLines(lines []string, selected map[int]struct{}) string {
	indexes := make([]int, 0, len(selected))
	for idx := range selected {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	if len(indexes) > maxPreviewLines {
		head := indexes[:maxPreviewLines/2]
		tail := indexes[len(indexes)-maxPreviewLines/2:]
		indexes = append(append([]int{}, head...), tail...)
	}

	var b strings.Builder
	last := -2
	omitted := 0
	for _, idx := range indexes {
		if idx < 0 || idx >= len(lines) {
			continue
		}
		if last >= 0 && idx > last+1 {
			gap := idx - last - 1
			omitted += gap
			b.WriteString(fmt.Sprintf("... [%d lines omitted] ...\n", gap))
		}
		b.WriteString(lines[idx])
		b.WriteByte('\n')
		last = idx
	}
	if last >= 0 && last < len(lines)-1 {
		gap := len(lines) - last - 1
		omitted += gap
		b.WriteString(fmt.Sprintf("... [%d lines omitted] ...\n", gap))
	}
	if omitted == 0 {
		return strings.Join(lines, "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatCompressedMarker(hash, toolName string, originalChars int, compressed string) string {
	return fmt.Sprintf("[brute-compressed kind=tool_result tool=%s hash=%s original_chars=%d]\nUse context_retrieve with this hash if exact omitted content is needed. Prefer passing a query to retrieve only relevant lines.\n\n%s", toolName, hash, originalChars, compressed)
}

func appendRetrievalInstructions(systemPrompt string) string {
	instruction := "Compressed tool results may appear in the conversation. When exact omitted content is needed, call context_retrieve with the shown hash and an optional query; do not guess omitted details."
	if strings.Contains(systemPrompt, "context_retrieve") {
		return systemPrompt
	}
	if strings.TrimSpace(systemPrompt) == "" {
		return instruction
	}
	return strings.TrimRight(systemPrompt, "\n") + "\n\n" + instruction
}

func ensureRetrievalTool(tools []llm.ToolDefinition) []llm.ToolDefinition {
	for _, tool := range tools {
		if tool.Name == RetrievalToolName {
			return tools
		}
	}
	return append(tools, llm.ToolDefinition{
		Name:        RetrievalToolName,
		Description: "Retrieve original content for a compressed tool result by hash. Use the optional query to return only matching lines when possible.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"hash":  map[string]interface{}{"type": "string", "description": "Hash shown in the brute-compressed marker."},
				"query": map[string]interface{}{"type": "string", "description": "Optional text to search for in the original content."},
			},
			"required": []string{"hash"},
		},
	})
}

func RetrievalToolDefinition() llm.ToolDefinition {
	return ensureRetrievalTool(nil)[0]
}

func cloneRequest(req *llm.ChatRequest) *llm.ChatRequest {
	if req == nil {
		return nil
	}
	out := *req
	out.Messages = append([]llm.Message(nil), req.Messages...)
	for i := range out.Messages {
		out.Messages[i].Images = append([]llm.Image(nil), req.Messages[i].Images...)
		out.Messages[i].ToolCalls = append([]llm.ToolCall(nil), req.Messages[i].ToolCalls...)
		out.Messages[i].ToolResults = append([]llm.ToolResult(nil), req.Messages[i].ToolResults...)
		for j := range out.Messages[i].ToolResults {
			if req.Messages[i].ToolResults[j].Metadata != nil {
				out.Messages[i].ToolResults[j].Metadata = cloneMap(req.Messages[i].ToolResults[j].Metadata)
			}
		}
	}
	out.Tools = append([]llm.ToolDefinition(nil), req.Tools...)
	return &out
}

func cloneMap(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
