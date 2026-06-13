package agentdef

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v2"
)

// ParseYAML decodes, normalizes, and validates a unified agent definition.
func ParseYAML(raw []byte) (*Definition, error) {
	if strings.TrimSpace(string(raw)) == "" {
		return nil, fmt.Errorf("agent definition YAML is empty")
	}
	var def Definition
	if err := yaml.UnmarshalStrict(raw, &def); err != nil {
		return nil, fmt.Errorf("failed to parse agent definition YAML: %w", err)
	}
	def.Normalize()
	if err := def.Validate(); err != nil {
		return nil, err
	}
	return &def, nil
}

// ToYAML normalizes, validates, and encodes a definition.
func ToYAML(def *Definition) ([]byte, error) {
	if def == nil {
		return nil, fmt.Errorf("agent definition is empty")
	}
	def.Normalize()
	if err := def.Validate(); err != nil {
		return nil, err
	}
	return yaml.Marshal(def)
}

// StripLocal returns a copy without machine-specific bindings, suitable for
// publishing as a portable template.
func StripLocal(def *Definition) *Definition {
	if def == nil {
		return nil
	}
	out := *def
	out.Local = Local{}
	return &out
}
