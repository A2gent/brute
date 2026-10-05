package llm

import (
	"encoding/json"
	"sort"
	"strings"
)

// Chrome extension screenshots can arrive as JSON data URLs, unlike CDP's
// file-backed screenshots. Move only recognized image fields to the existing
// image metadata channels so base64 is never excerpted or tokenized as text.
func SeparateToolResultImages(tr ToolResult) ToolResult {
	if !strings.Contains(tr.Content, "base64,") && !strings.Contains(tr.Content, `"data_base64"`) {
		return tr
	}
	var value interface{}
	if json.Unmarshal([]byte(tr.Content), &value) != nil {
		return tr
	}
	images := make([]interface{}, 0)
	var walk func(interface{})
	walk = func(value interface{}) {
		switch v := value.(type) {
		case map[string]interface{}:
			keys := make([]string, 0, len(v))
			for key := range v {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				raw := v[key]
				text, ok := raw.(string)
				if ok && (key == "data_url" || key == "dataUrl" || key == "screenshotDataUrl") && strings.HasPrefix(text, "data:image/") {
					prefix, payload, found := strings.Cut(text, ";base64,")
					if found && payload != "" {
						images = append(images, map[string]interface{}{"media_type": strings.TrimPrefix(prefix, "data:"), "data_base64": payload})
						v[key] = "[image moved to image_inline metadata]"
					}
				} else if ok && key == "data_base64" && text != "" {
					mediaType, _ := v["media_type"].(string)
					if strings.HasPrefix(mediaType, "image/") {
						images = append(images, map[string]interface{}{"media_type": mediaType, "data_base64": text})
						v[key] = "[image moved to image_inline metadata]"
					}
				} else if ok && (key == "output" || key == "input") {
					// Wrappers encode each child's JSON output as a string.
					var nested interface{}
					if json.Unmarshal([]byte(text), &nested) == nil {
						before := len(images)
						walk(nested)
						if len(images) > before {
							encoded, _ := json.Marshal(nested)
							v[key] = string(encoded)
						}
					}
				} else {
					walk(raw)
				}
			}
		case []interface{}:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(value)
	if len(images) == 0 {
		return tr
	}
	metadata := make(map[string]interface{}, len(tr.Metadata)+1)
	for key, value := range tr.Metadata {
		metadata[key] = value
	}
	// Keep the existing single-image channel for Anthropic and UI consumers.
	if existing, exists := metadata["image_inline"]; exists {
		images = append([]interface{}{existing}, images...)
	} else {
		metadata["image_inline"] = images[0]
	}
	metadata["admission_images"] = images
	metadata["image_original_text"] = tr.Content
	content, err := json.Marshal(value)
	if err != nil {
		return tr
	}
	tr.Content = string(content)
	tr.Metadata = metadata
	return tr
}
