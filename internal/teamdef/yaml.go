package teamdef

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v2"
)

// ParseYAML strictly decodes, normalizes, and validates a team definition.
func ParseYAML(raw []byte) (*Definition, error) {
	if strings.TrimSpace(string(raw)) == "" {
		return nil, fmt.Errorf("team definition YAML is empty")
	}
	var def Definition
	if err := yaml.UnmarshalStrict(raw, &def); err != nil {
		return nil, fmt.Errorf("failed to parse team definition YAML: %w", err)
	}
	def.Normalize()
	if err := def.Validate(); err != nil {
		return nil, err
	}
	return &def, nil
}

// ToYAML normalizes, validates, and serializes a canonical team definition.
func ToYAML(def *Definition) ([]byte, error) {
	if def == nil {
		return nil, fmt.Errorf("team definition is empty")
	}
	def.Normalize()
	if err := def.Validate(); err != nil {
		return nil, err
	}
	return yaml.Marshal(def)
}
