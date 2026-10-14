package session

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/A2gent/brute/internal/llm"
)

const TransitionMetadataKey = "provider_transitions"
const DefaultTransitionMode = "compact_auto"

func TransitionMode(raw string) (string, error) {
	switch raw {
	case "":
		return DefaultTransitionMode, nil
	case "compact_auto", "switch_auto", "confirm", "linked_session":
		return raw, nil
	}
	return "", fmt.Errorf("invalid provider transition mode: %s", raw)
}

type ProviderEndpoint struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}
type TransitionCompaction struct {
	Outcome        string            `json:"outcome"`
	Summarizer     *ProviderEndpoint `json:"summarizer,omitempty"`
	Usage          *llm.TokenUsage   `json:"usage,omitempty"`
	BeforeEstimate int               `json:"before_estimate,omitempty"`
	AfterEstimate  int               `json:"after_estimate,omitempty"`
	TargetWindow   int               `json:"target_window,omitempty"`
}

// Timeline anchors the notice in the archived transcript, not in LLM messages.
type TransitionTimeline struct {
	AfterMessageID string `json:"after_message_id"`
	MessageCount   int    `json:"message_count"`
	Sequence       int    `json:"sequence"`
}
type ProviderTransition struct {
	Timeline        *TransitionTimeline  `json:"timeline,omitempty"`
	SourceOutcome   string               `json:"source_outcome,omitempty"`
	TargetOutcome   string               `json:"target_outcome,omitempty"`
	RetryCount      *int                 `json:"retry_count,omitempty"`
	Version         int                  `json:"version"`
	ID              string               `json:"id"`
	Source          ProviderEndpoint     `json:"source"`
	Target          ProviderEndpoint     `json:"target"`
	Cause           string               `json:"cause"`
	Reason          string               `json:"reason,omitempty"`
	Mode            string               `json:"mode"`
	Phase           string               `json:"phase"`
	Confirmation    string               `json:"confirmation"`
	ContextStrategy string               `json:"context_strategy"`
	Compaction      TransitionCompaction `json:"compaction"`
	Revision        int                  `json:"revision"`
	CreatedAt       time.Time            `json:"created_at"`
	UpdatedAt       time.Time            `json:"updated_at"`
	Error           string               `json:"error,omitempty"`
	FallbackIndex   int                  `json:"fallback_index,omitempty"`
	ChainProvider   string               `json:"chain_provider,omitempty"`
	LinkedSessionID string               `json:"linked_session_id,omitempty"`
}

func NewProviderTransition(id string, source, target ProviderEndpoint, cause, mode string) (*ProviderTransition, error) {
	mode, err := TransitionMode(mode)
	if err != nil {
		return nil, err
	}
	t := &ProviderTransition{Version: 1, ID: id, Source: source, Target: target, Cause: cause, Mode: mode, Phase: "requested", Confirmation: "not_required", ContextStrategy: "compact", Compaction: TransitionCompaction{Outcome: "not_started"}, CreatedAt: time.Now().UTC()}
	if mode == "confirm" {
		t.Phase = "awaiting_confirmation"
		t.Confirmation = "pending"
	}
	if mode == "switch_auto" {
		t.ContextStrategy = "current"
		t.Compaction.Outcome = "skipped"
	}
	return t, nil
}
func (t *ProviderTransition) Terminal() bool {
	return t.Phase == "completed" || t.Phase == "rejected" || t.Phase == "linked"
}
func (t *ProviderTransition) Decide(decision string) (bool, error) {
	value := ""
	switch decision {
	case "approve":
		value = "approved"
	case "reject":
		value = "rejected"
	default:
		return false, fmt.Errorf("invalid decision")
	}
	if t.Confirmation == value {
		return false, nil
	}
	if t.Confirmation != "pending" || t.Phase != "awaiting_confirmation" {
		return false, fmt.Errorf("decision conflicts with transition state")
	}
	t.Confirmation = value
	if value == "approved" {
		t.Phase = "requested"
	} else {
		t.Phase = "rejected"
	}
	return true, nil
}
func ProviderTransitions(sess *Session) []ProviderTransition {
	if sess == nil || sess.Metadata == nil {
		return nil
	}
	data, err := json.Marshal(sess.Metadata[TransitionMetadataKey])
	if err != nil {
		return nil
	}
	var out []ProviderTransition
	_ = json.Unmarshal(data, &out)
	return out
}
func CurrentProviderTransition(sess *Session) *ProviderTransition {
	ts := ProviderTransitions(sess)
	if len(ts) == 0 {
		return nil
	}
	return &ts[len(ts)-1]
}
func PutProviderTransition(sess *Session, t *ProviderTransition) {
	ts := ProviderTransitions(sess)
	t.Revision++
	t.UpdatedAt = time.Now().UTC()
	found := false
	for i := range ts {
		if ts[i].ID == t.ID {
			t.Timeline = ts[i].Timeline
			ts[i] = *t
			found = true
			break
		}
	}
	if !found {
		after := ""
		if len(sess.Messages) > 0 {
			after = sess.Messages[len(sess.Messages)-1].ID
		}
		t.Timeline = &TransitionTimeline{AfterMessageID: after, MessageCount: len(sess.Messages), Sequence: len(ts) + 1}
		ts = append(ts, *t)
	}
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}
	// Copy through JSON so published snapshots never alias mutable agent state.
	data, _ := json.Marshal(ts)
	var values interface{}
	_ = json.Unmarshal(data, &values)
	sess.Metadata[TransitionMetadataKey] = values
}
