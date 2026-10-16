package storage

import (
	"encoding/json"
	"fmt"
	"github.com/A2gent/brute/internal/imagefiles"
	"strings"
)

// externalizeMessageImages is also used for direct storage callers and migration.
func externalizeMessageImages(root, sessionID string, metadata map[string]interface{}) (map[string]interface{}, bool, error) {
	raw, exists := metadata["images"]
	if !exists {
		return metadata, false, nil
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return nil, false, err
	}
	var images []map[string]interface{}
	if err := json.Unmarshal(payload, &images); err != nil {
		return nil, false, err
	}
	changed := false
	for _, fields := range images {
		data, _ := fields["data_base64"].(string)
		url, _ := fields["url"].(string)
		if data == "" && !strings.HasPrefix(url, "data:image/") {
			continue
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return nil, false, err
		}
		var attachment imagefiles.Attachment
		if err := json.Unmarshal(encoded, &attachment); err != nil {
			return nil, false, err
		}
		saved, err := attachment.Save(root, sessionID)
		if err != nil {
			return nil, false, fmt.Errorf("save image: %w", err)
		}
		encoded, _ = json.Marshal(saved)
		var next map[string]interface{}
		_ = json.Unmarshal(encoded, &next)
		delete(fields, "data_base64")
		if saved.URL == "" {
			delete(fields, "url")
		}
		for k, v := range next {
			fields[k] = v
		}
		changed = true
	}
	if !changed {
		return metadata, false, nil
	}
	next := make(map[string]interface{}, len(metadata))
	for k, v := range metadata {
		next[k] = v
	}
	next["images"] = images
	return next, true, nil
}
