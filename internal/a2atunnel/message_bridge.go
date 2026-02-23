package a2atunnel

import (
	"strings"
)

const (
	A2ABridgeVersion          = "0.1-bridge"
	A2AContentTypeText        = "text"
	A2AContentTypeImageURL    = "image_url"
	A2AContentTypeImageBase64 = "image_base64"
)

type A2AParty struct {
	AgentID string `json:"agent_id,omitempty"`
	Name    string `json:"name,omitempty"`
}

type A2AContentPart struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	URL       string `json:"url,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	Name      string `json:"name,omitempty"`
}

func BuildA2AContent(task string, images []A2AImage) []A2AContentPart {
	content := make([]A2AContentPart, 0, len(images)+1)
	trimmedTask := strings.TrimSpace(task)
	if trimmedTask != "" {
		content = append(content, A2AContentPart{Type: A2AContentTypeText, Text: trimmedTask})
	}
	for _, img := range images {
		if url := strings.TrimSpace(img.URL); url != "" {
			content = append(content, A2AContentPart{Type: A2AContentTypeImageURL, URL: url, Name: strings.TrimSpace(img.Name)})
			continue
		}
		data := strings.TrimSpace(img.DataBase64)
		if data == "" {
			continue
		}
		mediaType := strings.TrimSpace(img.MediaType)
		if mediaType == "" {
			mediaType = "image/png"
		}
		content = append(content, A2AContentPart{
			Type:      A2AContentTypeImageBase64,
			Data:      data,
			MediaType: mediaType,
			Name:      strings.TrimSpace(img.Name),
		})
	}
	return content
}

func LegacyFromA2AContent(content []A2AContentPart) (string, []A2AImage) {
	texts := make([]string, 0, len(content))
	images := make([]A2AImage, 0, len(content))
	for _, raw := range content {
		part := A2AContentPart{
			Type:      strings.TrimSpace(raw.Type),
			Text:      strings.TrimSpace(raw.Text),
			URL:       strings.TrimSpace(raw.URL),
			MediaType: strings.TrimSpace(raw.MediaType),
			Data:      strings.TrimSpace(raw.Data),
			Name:      strings.TrimSpace(raw.Name),
		}
		switch part.Type {
		case A2AContentTypeText:
			if part.Text != "" {
				texts = append(texts, part.Text)
			}
		case A2AContentTypeImageURL:
			if part.URL != "" {
				images = append(images, A2AImage{Name: part.Name, URL: part.URL})
			}
		case A2AContentTypeImageBase64:
			if part.Data != "" {
				if part.MediaType == "" {
					part.MediaType = "image/png"
				}
				images = append(images, A2AImage{Name: part.Name, MediaType: part.MediaType, DataBase64: part.Data})
			}
		}
	}
	return strings.Join(texts, "\n"), images
}
