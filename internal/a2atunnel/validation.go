package a2atunnel

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

const (
	MaxA2AContentParts      = 32
	MaxA2AImagesPerMessage  = 8
	MaxA2AImageDecodedBytes = 8 * 1024 * 1024 // 8 MiB
	MaxA2AImageBase64Length = 12 * 1024 * 1024
)

var allowedA2AImageMediaTypes = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/webp": {},
	"image/gif":  {},
}

func ValidateA2AContent(content []A2AContentPart) error {
	if len(content) == 0 {
		return fmt.Errorf("content is required")
	}
	if len(content) > MaxA2AContentParts {
		return fmt.Errorf("too many content parts: max %d", MaxA2AContentParts)
	}

	imageCount := 0
	for idx, part := range content {
		p := A2AContentPart{
			Type:      strings.TrimSpace(part.Type),
			Text:      strings.TrimSpace(part.Text),
			URL:       strings.TrimSpace(part.URL),
			MediaType: strings.TrimSpace(part.MediaType),
			Data:      strings.TrimSpace(part.Data),
		}
		switch p.Type {
		case A2AContentTypeText:
			if p.Text == "" {
				return fmt.Errorf("content part %d type=text requires text", idx+1)
			}
		case A2AContentTypeImageURL:
			imageCount++
			if err := validateA2AImageURL(p.URL); err != nil {
				return fmt.Errorf("content part %d: %w", idx+1, err)
			}
		case A2AContentTypeImageBase64:
			imageCount++
			if err := validateA2AImageBase64(p.MediaType, p.Data); err != nil {
				return fmt.Errorf("content part %d: %w", idx+1, err)
			}
		default:
			return fmt.Errorf("unsupported content part type: %s", p.Type)
		}
	}
	if imageCount > MaxA2AImagesPerMessage {
		return fmt.Errorf("too many images: max %d", MaxA2AImagesPerMessage)
	}
	return nil
}

func ValidateA2AImages(images []A2AImage) error {
	if len(images) > MaxA2AImagesPerMessage {
		return fmt.Errorf("too many images: max %d", MaxA2AImagesPerMessage)
	}
	for idx, img := range images {
		image := A2AImage{
			Name:       strings.TrimSpace(img.Name),
			MediaType:  strings.TrimSpace(img.MediaType),
			DataBase64: strings.TrimSpace(img.DataBase64),
			URL:        strings.TrimSpace(img.URL),
		}
		if image.URL != "" {
			if err := validateA2AImageURL(image.URL); err != nil {
				return fmt.Errorf("image %d: %w", idx+1, err)
			}
			continue
		}
		if image.DataBase64 == "" {
			return fmt.Errorf("image %d has neither data_base64 nor url", idx+1)
		}
		if err := validateA2AImageBase64(image.MediaType, image.DataBase64); err != nil {
			return fmt.Errorf("image %d: %w", idx+1, err)
		}
	}
	return nil
}

func validateA2AImageURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("image URL is required")
	}
	u, err := url.Parse(trimmed)
	if err != nil || u == nil {
		return fmt.Errorf("invalid image URL")
	}
	switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("image URL scheme must be http or https")
	}
}

func validateA2AImageBase64(mediaType, data string) error {
	mt := strings.ToLower(strings.TrimSpace(mediaType))
	if mt == "" {
		mt = "image/png"
	}
	if _, ok := allowedA2AImageMediaTypes[mt]; !ok {
		return fmt.Errorf("unsupported image media_type: %s", mt)
	}

	payload := strings.TrimSpace(data)
	if payload == "" {
		return fmt.Errorf("image_base64 data is required")
	}
	if len(payload) > MaxA2AImageBase64Length {
		return fmt.Errorf("image payload too large")
	}

	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(payload)
	}
	if err != nil {
		return fmt.Errorf("invalid base64 image data")
	}
	if len(decoded) > MaxA2AImageDecodedBytes {
		return fmt.Errorf("image exceeds %d bytes", MaxA2AImageDecodedBytes)
	}
	return nil
}
