package contextcompress

import (
	"encoding/json"
	"strings"

	"github.com/A2gent/brute/internal/llm"
)

func commandPreview(tr llm.ToolResult) (string, bool) {
	if tr.Name == "bash" || (tr.Name == "pipeline" && tr.Metadata["command_output"] == true) {
		if !largeBashResult(tr) {
			return tr.Content, false
		}
		return compressBashOutput(tr), true
	}
	if tr.Name != "parallel" && tr.Metadata["command_output_kind"] != "parallel" {
		return tr.Content, false
	}
	var steps []map[string]interface{}
	decoder := json.NewDecoder(strings.NewReader(tr.Content))
	if decoder.Decode(&steps) != nil {
		return tr.Content, false
	}
	changed := false
	for _, step := range steps {
		name, _ := step["tool"].(string)
		output, _ := step["output"].(string)
		metadata, _ := step["metadata"].(map[string]interface{})
		preview, applied := commandPreview(llm.ToolResult{Name: name, Content: output, Metadata: metadata})
		if applied && len(preview) < len(output) {
			step["output"] = preview
			changed = true
		}
	}
	if !changed {
		return tr.Content, false
	}
	data, err := json.MarshalIndent(steps, "", "  ")
	if err != nil {
		return tr.Content, false
	}
	return string(data) + tr.Content[decoder.InputOffset():], true
}

// Admission caps may already have stored the original before request compression.
// Reuse that hash rather than treating the lossy excerpt as a new original.
func (c *Compressor) restoreCommandPreview(sessionID string, tr *llm.ToolResult) (Item, bool) {
	if !strings.HasPrefix(tr.Content, "[brute-compressed ") {
		return Item{}, false
	}
	first, _, _ := strings.Cut(tr.Content, "\n")
	hash := ""
	for _, field := range strings.Fields(first) {
		if strings.HasPrefix(field, "hash=") {
			hash = strings.TrimSuffix(strings.TrimPrefix(field, "hash="), "]")
		}
	}
	original, ok := c.Retrieve(sessionID, hash, "")
	if !ok {
		return Item{}, false
	}
	raw := *tr
	raw.Content = original
	preview, applied := commandPreview(raw)
	if !applied {
		return Item{}, false
	}
	tr.Content = formatCompressedMarker(hash, tr.Name, len(original), preview)
	return Item{Hash: hash, ToolName: tr.Name, ToolCallID: tr.ToolCallID, OriginalChars: len(original), ShownChars: len(tr.Content)}, true
}
