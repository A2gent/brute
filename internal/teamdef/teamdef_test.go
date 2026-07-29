package teamdef

import (
	"strings"
	"testing"
)

const validTeamYAML = `id: squad-product
name: Product squad
description: Design, build and review product changes.
policy:
  lead: architect
  termination: lead_done
  max_messages: 60
  max_minutes: 60
  max_tokens: 4000000
  broadcast_allowed: true
members:
  - role: architect
    agent_id: agent-def-123
    charter: Owns design decisions and the final answer.
    can_message: [developer]
  - role: developer
    agent_id: agent-def-456
    charter: Implements changes in the repository.
`

func TestParseYAMLValidDefinitionRoundTrip(t *testing.T) {
	def, err := ParseYAML([]byte(validTeamYAML))
	if err != nil {
		t.Fatalf("ParseYAML() error = %v", err)
	}
	if def.ID != "squad-product" || def.Policy.Lead != "architect" || len(def.Members) != 2 {
		t.Fatalf("ParseYAML() = %#v", def)
	}
	if def.Members[1].AgentID != "agent-def-456" {
		t.Fatalf("developer agent_id = %q", def.Members[1].AgentID)
	}

	raw, err := ToYAML(def)
	if err != nil {
		t.Fatalf("ToYAML() error = %v", err)
	}
	roundTrip, err := ParseYAML(raw)
	if err != nil {
		t.Fatalf("round-trip ParseYAML() error = %v\n%s", err, raw)
	}
	if roundTrip.ID != def.ID || len(roundTrip.Members) != len(def.Members) {
		t.Fatalf("round trip = %#v, want %#v", roundTrip, def)
	}
}

func TestParseYAMLRejectsInvalidDefinitions(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "invalid YAML", raw: "members: [", want: "failed to parse team definition YAML"},
		{name: "unknown field", raw: validTeamYAML + "edges: []\n", want: "field edges not found"},
		{name: "duplicate role", raw: strings.Replace(validTeamYAML, "role: developer", "role: architect", 1), want: "duplicate member role"},
		{name: "dangling lead", raw: strings.Replace(validTeamYAML, "lead: architect", "lead: missing", 1), want: "policy.lead"},
		{name: "unknown can_message role", raw: strings.Replace(validTeamYAML, "can_message: [developer]", "can_message: [missing]", 1), want: "can_message"},
		{name: "missing agent", raw: strings.Replace(validTeamYAML, "agent_id: agent-def-456", "agent_id: ''", 1), want: "agent_id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseYAML([]byte(tt.raw))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseYAML() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}
