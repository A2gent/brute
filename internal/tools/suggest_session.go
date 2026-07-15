package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const sessionActionFenceLanguage = "a2gent-session"

// SuggestSessionParams describes a UI action card that lets the user branch off into a new session.
type SuggestSessionParams struct {
	Title     string   `json:"title"`
	Label     string   `json:"label,omitempty"`
	Severity  string   `json:"severity,omitempty"`
	Files     []string `json:"files,omitempty"`
	Prompt    string   `json:"prompt"`
	Completed bool     `json:"completed,omitempty"`
}

// SuggestSessionTool formats a quick session branch-off card for the Caesar UI.
type SuggestSessionTool struct{}

func NewSuggestSessionTool() *SuggestSessionTool {
	return &SuggestSessionTool{}
}

func (t *SuggestSessionTool) Name() string {
	return "suggest_session"
}

func (t *SuggestSessionTool) Description() string {
	return `Suggest one quick branch-off into a new UI session by producing an a2gent-session action card.

Use this for any actionable follow-up that the user may want to implement or investigate in a separate session instead of immediately changing the current session scope.

Important usage:
- Create at most one card per distinct follow-up, but call this tool multiple times when your answer contains multiple independent actionable findings that deserve separate sessions.
- Place each returned fenced block immediately after the specific finding it belongs to; do not collect all cards at the end.
- Do not merge unrelated findings into one broad card. Prefer a narrow, actionable title/files/prompt for each card.
- Skip the tool for tiny inline suggestions, purely informational notes, duplicates, or findings that should be fixed together with another card.

The tool returns a fenced block that must be included verbatim in your assistant message near the relevant finding. The UI renders that block as a Create Session button/card. Keep the prompt self-contained with the issue summary, files to inspect, expected behavior, and verification steps.`
}

func (t *SuggestSessionTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Short title for the new follow-up session card.",
			},
			"label": map[string]interface{}{
				"type":        "string",
				"description": "Button label. Defaults to 'Create session'. Use labels like 'Create fix session' or 'Investigate separately'.",
			},
			"severity": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"high", "medium", "low"},
				"description": "Optional severity for styling the card.",
			},
			"files": map[string]interface{}{
				"type":        "array",
				"description": "Relevant file paths to show on the card.",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
			"prompt": map[string]interface{}{
				"type":        "string",
				"description": "Self-contained prompt for the new session. Include the summary, files to inspect, expected behavior, and verification steps.",
			},
			"completed": map[string]interface{}{
				"type":        "boolean",
				"description": "Set true when this follow-up is already finished in the current session. Hides the Add to TODO action in Caesar.",
			},
		},
		"required": []string{"title", "prompt"},
	}
}

func (t *SuggestSessionTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var p SuggestSessionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	card, errMessage := normalizeSuggestSessionParams(p)
	if errMessage != "" {
		return &Result{Success: false, Error: errMessage}, nil
	}

	payload, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal session suggestion: %w", err)
	}

	// Return the exact markdown fence because Caesar already parses this format in all assistant messages.
	fencedBlock := fmt.Sprintf("```%s\n%s\n```", sessionActionFenceLanguage, payload)
	return &Result{
		Success: true,
		Output:  fencedBlock,
		Metadata: map[string]interface{}{
			"kind":     "session_action_card",
			"language": sessionActionFenceLanguage,
			"card":     card,
		},
	}, nil
}

func normalizeSuggestSessionParams(p SuggestSessionParams) (SuggestSessionParams, string) {
	card := SuggestSessionParams{
		Title:     strings.TrimSpace(p.Title),
		Label:     strings.TrimSpace(p.Label),
		Severity:  strings.ToLower(strings.TrimSpace(p.Severity)),
		Prompt:    strings.TrimSpace(p.Prompt),
		Completed: p.Completed,
	}

	if card.Title == "" {
		return SuggestSessionParams{}, "title is required"
	}
	if card.Prompt == "" {
		return SuggestSessionParams{}, "prompt is required"
	}

	if card.Severity != "" {
		switch card.Severity {
		case "high", "medium", "low":
		default:
			return SuggestSessionParams{}, "severity must be one of: high, medium, low"
		}
	}

	seenFiles := make(map[string]struct{}, len(p.Files))
	for _, file := range p.Files {
		trimmed := strings.TrimSpace(file)
		if trimmed == "" {
			continue
		}
		if _, seen := seenFiles[trimmed]; seen {
			continue
		}
		seenFiles[trimmed] = struct{}{}
		card.Files = append(card.Files, trimmed)
	}

	return card, ""
}

var _ Tool = (*SuggestSessionTool)(nil)
