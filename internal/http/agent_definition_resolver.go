package http

import (
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/agentdef"
)

// definitionForUnifiedAgent resolves an agent ID to a definition.
// Precedence: stored unified definition > legacy SubAgent > on-disk definition.
// The stored definition is authoritative so an explicit import (e.g. runtime
// host) is never shadowed by a legacy SubAgent that shares its ID.
func (s *Server) definitionForUnifiedAgent(id string, projectID string) (*agentdef.Definition, string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, "", fmt.Errorf("agent ID is required")
	}

	record, err := s.store.GetAgentDefinition(id)
	if err == nil && record != nil {
		def, parseErr := agentdef.ParseYAML([]byte(record.DefinitionYAML))
		if parseErr != nil {
			return nil, "", fmt.Errorf("stored agent definition %s is invalid: %w", id, parseErr)
		}
		discoveredProjectID := agentDefinitionRecordProjectID(record)
		if discoveredProjectID == "" {
			discoveredProjectID = stringFromOptional(projectIDFromDefinition(def))
		}
		return def, discoveredProjectID, nil
	}

	if sa, err := s.store.GetSubAgent(id); err == nil && sa != nil {
		def, defErr := agentdef.FromSubAgent(sa)
		if defErr != nil {
			return nil, "", fmt.Errorf("failed to build agent definition from saved agent %s: %w", id, defErr)
		}
		return def, subAgentProjectID(sa), nil
	}

	def, discoveredProjectID, discoverErr := s.discoveredDefinitionForUnifiedAgent(id, projectID)
	if discoverErr != nil {
		return nil, "", fmt.Errorf("agent definition not found: %s", id)
	}
	return def, discoveredProjectID, nil
}
