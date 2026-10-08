package http

import (
	"crypto/sha256"

	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/session"
)

// Images must bypass the compact text budget: a base64 excerpt is not usable
// visual context. Scan the whole parent so the initial reference survives long runs.
func linkedContinuationImages(parent *session.Session, uploads []session.ImageAttachment) []session.ImageAttachment {
	var images []session.ImageAttachment
	seen := make(map[[32]byte]bool)
	add := func(image session.ImageAttachment) {
		if image.DataBase64 == "" && image.URL == "" {
			return
		}
		key := sha256.Sum256([]byte(image.MediaType + "\x00" + image.DataBase64 + "\x00" + image.URL))
		if seen[key] {
			return
		}
		seen[key] = true
		images = append(images, image)
	}
	for _, image := range uploads {
		add(image)
	}
	if parent == nil {
		return images
	}
	for _, msg := range parent.Messages {
		for _, image := range msg.Images {
			add(image)
		}
		for _, result := range msg.ToolResults {
			result := linkedContextToolResult(result)
			addMetadataImage := func(raw interface{}) {
				image, _ := raw.(map[string]interface{})
				mediaType, _ := image["media_type"].(string)
				data, _ := image["data_base64"].(string)
				url, _ := image["url"].(string)
				add(session.ImageAttachment{MediaType: mediaType, DataBase64: data, URL: url})
			}
			if admitted, ok := result.Metadata["admission_images"].([]interface{}); ok {
				for _, image := range admitted {
					addMetadataImage(image)
				}
			}
			addMetadataImage(result.Metadata["image_inline"])
		}
	}
	return images
}

func linkedContextToolResult(result session.ToolResult) llm.ToolResult {
	return llm.SeparateToolResultImages(llm.ToolResult{Content: result.Content, Metadata: result.Metadata})
}
