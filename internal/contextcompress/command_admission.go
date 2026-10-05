package contextcompress

import (
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
)

// Command diagnostics take precedence over a hard preview cap. An all-error log
// may therefore exceed the budget; dropping unique errors would prevent recovery.
func (c *Compressor) admitCommandPreview(sess *session.Session, tr llm.ToolResult, original string) (llm.ToolResult, bool) {
	preview, ok := commandPreview(tr)
	if !ok {
		return tr, false
	}
	value := entry{SessionID: sess.ID, ToolName: tr.Name, ToolCallID: tr.ToolCallID, Original: original, Compressed: preview}
	value.Hash = c.store.put(sess.ID, value)
	if sess.Metadata == nil {
		sess.Metadata = make(map[string]interface{})
	}
	stored := readSessionEntries(sess.Metadata)
	stored[value.Hash] = value
	sess.Metadata[sessionCCRMetadataKey] = encodeSessionEntries(stored)
	tr.Content = formatCompressedMarker(value.Hash, tr.Name, len(original), preview)
	return tr, true
}
