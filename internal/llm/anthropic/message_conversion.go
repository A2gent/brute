package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

// convertMessage converts an LLM message to Anthropic format
func (c *Client) convertMessage(msg llm.Message) anthropicMessage {
	if msg.Role == "user" && len(msg.Images) > 0 {
		blocks := make([]contentBlock, 0, len(msg.Images)+1)
		if strings.TrimSpace(msg.Content) != "" {
			blocks = append(blocks, contentBlock{
				Type: "text",
				Text: msg.Content,
			})
		}
		for _, img := range msg.Images {
			if block := llmImageToAnthropicBlock(img); block != nil {
				blocks = append(blocks, *block)
			}
		}
		if len(blocks) == 0 {
			return anthropicMessage{Role: msg.Role, Content: msg.Content}
		}
		return anthropicMessage{
			Role:    msg.Role,
			Content: blocks,
		}
	}

	if msg.Role == "tool" {
		// Tool results need special handling
		blocks := make([]contentBlock, 0, len(msg.ToolResults))
		for _, result := range msg.ToolResults {
			// Ensure ToolCallID is not empty to avoid tool_use_id validation errors
			if result.ToolCallID == "" {
				continue // Skip tool results without valid tool call IDs
			}

			content := any(result.Content)
			if inline := extractInlineImage(result.Metadata); inline != nil {
				content = []map[string]interface{}{
					{
						"type": "image",
						"source": map[string]interface{}{
							"type":       "base64",
							"media_type": inline.MediaType,
							"data":       inline.DataBase64,
						},
					},
					{
						"type": "text",
						"text": result.Content,
					},
				}
			}
			blocks = append(blocks, contentBlock{
				Type:      "tool_result",
				ToolUseID: result.ToolCallID,
				Content:   content,
				IsError:   result.IsError,
			})
		}

		// Only create tool result message if we have valid blocks
		if len(blocks) > 0 {
			return anthropicMessage{
				Role:    "user",
				Content: blocks,
			}
		}

		// If no valid tool results, return empty user message
		return anthropicMessage{
			Role:    "user",
			Content: "",
		}
	}

	if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
		// Assistant with tool calls
		blocks := make([]contentBlock, 0)
		if msg.Content != "" {
			blocks = append(blocks, contentBlock{
				Type: "text",
				Text: msg.Content,
			})
		}
		for _, tc := range msg.ToolCalls {
			// Skip tool calls with missing required fields
			if tc.ID == "" || tc.Name == "" {
				logging.Debug("Skipping tool call with missing ID or Name: ID=%s, Name=%s", tc.ID, tc.Name)
				continue
			}

			var input any
			if tc.Input != "" {
				if err := json.Unmarshal([]byte(tc.Input), &input); err != nil {
					// If input is malformed, use empty object instead of nil
					logging.Debug("Fixed malformed tool call input for %s: %v", tc.Name, err)
					input = map[string]interface{}{}
				}
			} else {
				// If input is empty, use empty object
				logging.Debug("Fixed empty tool call input for %s", tc.Name)
				input = map[string]interface{}{}
			}
			blocks = append(blocks, contentBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Name,
				Input: input,
			})
		}
		return anthropicMessage{
			Role:    "assistant",
			Content: blocks,
		}
	}

	// Simple text message
	return anthropicMessage{
		Role:    msg.Role,
		Content: msg.Content,
	}
}

func llmImageToAnthropicBlock(img llm.Image) *contentBlock {
	mediaType := strings.TrimSpace(img.MediaType)
	dataBase64 := strings.TrimSpace(img.DataBase64)
	if mediaType == "" {
		mediaType = "image/png"
	}
	if dataBase64 == "" {
		if dataURL := strings.TrimSpace(img.URL); strings.HasPrefix(strings.ToLower(dataURL), "data:") {
			dataPartIdx := strings.Index(dataURL, ",")
			if dataPartIdx > 0 {
				header := dataURL[:dataPartIdx]
				if strings.HasPrefix(strings.ToLower(header), "data:") {
					if semi := strings.Index(header, ";"); semi > 5 {
						mediaType = header[5:semi]
					}
				}
				dataBase64 = dataURL[dataPartIdx+1:]
			}
		}
	}
	if dataBase64 == "" {
		return nil
	}
	return &contentBlock{
		Type: "image",
		Source: map[string]interface{}{
			"type":       "base64",
			"media_type": mediaType,
			"data":       dataBase64,
		},
	}
}

func contentBlockToImage(block contentBlock) *llm.Image {
	sourceMap, ok := block.Source.(map[string]interface{})
	if !ok {
		return nil
	}
	mediaType := strings.TrimSpace(asString(sourceMap["media_type"]))
	dataBase64 := strings.TrimSpace(asString(sourceMap["data"]))
	if mediaType == "" && dataBase64 == "" {
		return nil
	}
	return &llm.Image{
		MediaType:  mediaType,
		DataBase64: dataBase64,
		URL:        llm.Image{MediaType: mediaType, DataBase64: dataBase64}.DataURL(),
	}
}

type inlineImage struct {
	MediaType  string
	DataBase64 string
}

func extractInlineImage(metadata map[string]interface{}) *inlineImage {
	if len(metadata) == 0 {
		return nil
	}
	rawInline, ok := metadata["image_inline"]
	if !ok {
		return nil
	}
	inlineMap, ok := rawInline.(map[string]interface{})
	if !ok {
		return nil
	}
	mediaType, _ := inlineMap["media_type"].(string)
	dataBase64, _ := inlineMap["data_base64"].(string)
	mediaType = strings.TrimSpace(mediaType)
	dataBase64 = strings.TrimSpace(dataBase64)
	if mediaType == "" {
		return nil
	}
	if dataBase64 == "" {
		path := strings.TrimSpace(asString(inlineMap["path"]))
		if path == "" {
			path = strings.TrimSpace(asString(metadataPath(metadata)))
		}
		if path == "" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil || len(raw) == 0 {
			return nil
		}
		maxBytes := int64(2 * 1024 * 1024)
		if v, ok := inlineMap["max_bytes"].(float64); ok && int64(v) > 0 {
			maxBytes = int64(v)
		}
		if int64(len(raw)) > maxBytes {
			return nil
		}
		dataBase64 = base64.StdEncoding.EncodeToString(raw)
	}
	if dataBase64 == "" {
		return nil
	}
	return &inlineImage{
		MediaType:  mediaType,
		DataBase64: dataBase64,
	}
}

func metadataPath(metadata map[string]interface{}) string {
	imageFile, ok := metadata["image_file"]
	if !ok {
		return ""
	}
	imageFileMap, ok := imageFile.(map[string]interface{})
	if !ok {
		return ""
	}
	return asString(imageFileMap["path"])
}

func asString(value interface{}) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}
