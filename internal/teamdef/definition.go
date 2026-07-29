// Package teamdef defines portable team rosters and their runtime policy.
package teamdef

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	TerminationLeadDone    = "lead_done"
	TerminationIdle        = "idle"
	TerminationMaxMessages = "max_messages"
)

var teamIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Definition is the canonical team definition stored as YAML.
type Definition struct {
	ID          string   `yaml:"id" json:"id"`
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Policy      Policy   `yaml:"policy" json:"policy"`
	Members     []Member `yaml:"members" json:"members"`
}

// Policy bounds a run and determines who may finish it.
type Policy struct {
	Lead             string `yaml:"lead,omitempty" json:"lead,omitempty"`
	Termination      string `yaml:"termination" json:"termination"`
	MaxMessages      int    `yaml:"max_messages" json:"max_messages"`
	MaxMinutes       int    `yaml:"max_minutes" json:"max_minutes"`
	MaxTokens        int64  `yaml:"max_tokens,omitempty" json:"max_tokens,omitempty"`
	BroadcastAllowed bool   `yaml:"broadcast_allowed" json:"broadcast_allowed"`
}

// Member binds a unique role address to one unified agent definition.
type Member struct {
	Role       string   `yaml:"role" json:"role"`
	AgentID    string   `yaml:"agent_id" json:"agent_id"`
	Charter    string   `yaml:"charter,omitempty" json:"charter,omitempty"`
	CanMessage []string `yaml:"can_message,omitempty" json:"can_message,omitempty"`
}

// Normalize removes accidental whitespace without changing authored prose.
func (d *Definition) Normalize() {
	if d == nil {
		return
	}
	d.ID = strings.TrimSpace(d.ID)
	d.Name = strings.TrimSpace(d.Name)
	d.Description = strings.TrimSpace(d.Description)
	d.Policy.Lead = strings.TrimSpace(d.Policy.Lead)
	d.Policy.Termination = strings.TrimSpace(d.Policy.Termination)
	for i := range d.Members {
		d.Members[i].Role = strings.TrimSpace(d.Members[i].Role)
		d.Members[i].AgentID = strings.TrimSpace(d.Members[i].AgentID)
		d.Members[i].Charter = strings.TrimSpace(d.Members[i].Charter)
		for j := range d.Members[i].CanMessage {
			d.Members[i].CanMessage[j] = strings.TrimSpace(d.Members[i].CanMessage[j])
		}
	}
}

// Validate checks roster addresses and run bounds before a team is persisted.
func (d *Definition) Validate() error {
	if d == nil {
		return fmt.Errorf("team definition is empty")
	}
	if !teamIdentifierPattern.MatchString(d.ID) {
		return fmt.Errorf("team id must start with an alphanumeric character and contain only letters, numbers, dots, underscores, or hyphens")
	}
	if d.Name == "" {
		return fmt.Errorf("team name is required")
	}
	if len(d.Members) == 0 {
		return fmt.Errorf("team members are required")
	}

	roles := make(map[string]struct{}, len(d.Members))
	for i, member := range d.Members {
		if !teamIdentifierPattern.MatchString(member.Role) {
			return fmt.Errorf("members[%d].role must be a valid address", i)
		}
		if member.Role == "user" {
			return fmt.Errorf("members[%d].role %q is reserved for user participation", i, member.Role)
		}
		if _, exists := roles[member.Role]; exists {
			return fmt.Errorf("duplicate member role %q", member.Role)
		}
		roles[member.Role] = struct{}{}
		if member.AgentID == "" {
			return fmt.Errorf("members[%d].agent_id is required", i)
		}
	}
	if d.Policy.Lead != "" {
		if _, exists := roles[d.Policy.Lead]; !exists {
			return fmt.Errorf("policy.lead %q is not a member role", d.Policy.Lead)
		}
	}
	switch d.Policy.Termination {
	case TerminationLeadDone, TerminationIdle, TerminationMaxMessages:
	default:
		return fmt.Errorf("policy.termination must be one of %q, %q, or %q", TerminationLeadDone, TerminationIdle, TerminationMaxMessages)
	}
	if d.Policy.MaxMessages <= 0 {
		return fmt.Errorf("policy.max_messages must be greater than zero")
	}
	if d.Policy.MaxMinutes <= 0 {
		return fmt.Errorf("policy.max_minutes must be greater than zero")
	}
	if d.Policy.MaxTokens < 0 {
		return fmt.Errorf("policy.max_tokens cannot be negative")
	}

	for _, member := range d.Members {
		seen := make(map[string]struct{}, len(member.CanMessage))
		for _, target := range member.CanMessage {
			if target == "user" {
				continue
			}
			if _, exists := roles[target]; !exists {
				return fmt.Errorf("member role %q can_message references unknown role %q", member.Role, target)
			}
			if _, duplicate := seen[target]; duplicate {
				return fmt.Errorf("member role %q can_message contains duplicate role %q", member.Role, target)
			}
			seen[target] = struct{}{}
		}
	}
	return nil
}

// MemberByRole resolves one mailbox address.
func (d *Definition) MemberByRole(role string) (Member, bool) {
	if d == nil {
		return Member{}, false
	}
	for _, member := range d.Members {
		if member.Role == strings.TrimSpace(role) {
			return member, true
		}
	}
	return Member{}, false
}
