package session

import (
	"encoding/json"
	"github.com/A2gent/brute/internal/llm"
)

const provenanceMetadataKey = "llm_provenance"

// Only the versioned producer record is trustworthy. Old timing metadata may
// name a router or a stale selection and must not be used to infer history.
func provenanceFromMetadata(metadata map[string]interface{}) *llm.Provenance {
	raw := metadata[provenanceMetadataKey]
	if raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var p llm.Provenance
	if json.Unmarshal(data, &p) != nil {
		return nil
	}
	return llm.NewProvenance(p.Provider, p.Model)
}

func metadataWithProvenance(metadata map[string]interface{}, p *llm.Provenance) map[string]interface{} {
	if p == nil {
		return metadata
	}
	next := cloneMetadata(metadata)
	next[provenanceMetadataKey] = map[string]interface{}{"provider": p.Provider, "model": p.Model}
	return next
}
