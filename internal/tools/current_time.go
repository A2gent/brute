package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// CurrentTimeTool returns the current system time, optionally converted to a requested timezone.
type CurrentTimeTool struct {
	now func() time.Time
}

type CurrentTimeParams struct {
	Timezone string `json:"timezone,omitempty"`
}

func NewCurrentTimeTool() *CurrentTimeTool {
	return &CurrentTimeTool{now: time.Now}
}

func (t *CurrentTimeTool) Name() string {
	return "current_time"
}

func (t *CurrentTimeTool) Description() string {
	return "Get the current time from the local system clock. Optionally convert it to a named IANA timezone like UTC, Europe/Tallinn, or America/New_York."
}

func (t *CurrentTimeTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"timezone": map[string]interface{}{
				"type":        "string",
				"description": "Optional IANA timezone name to render the current time in, for example UTC, Europe/Tallinn, or America/New_York.",
			},
		},
	}
}

func (t *CurrentTimeTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p CurrentTimeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid parameters: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	now := t.now()
	location := now.Location()
	locationName := location.String()

	if p.Timezone != "" {
		loc, err := time.LoadLocation(p.Timezone)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("invalid timezone %q: %v", p.Timezone, err)}, nil
		}
		now = now.In(loc)
		location = loc
		locationName = loc.String()
	}

	utcNow := now.UTC()

	return &Result{
		Success: true,
		Output:  now.Format(time.RFC3339),
		Metadata: map[string]interface{}{
			"timezone":      locationName,
			"formatted":     now.Format(time.RFC3339),
			"formatted_utc": utcNow.Format(time.RFC3339),
			"unix":          now.Unix(),
			"unix_milli":    now.UnixMilli(),
			"offset_seconds": func() int {
				_, offset := now.Zone()
				return offset
			}(),
		},
	}, nil
}

var _ Tool = (*CurrentTimeTool)(nil)
