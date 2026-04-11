package tools

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
)

// RandomNumberTool generates a random integer within a configurable range.
type RandomNumberTool struct{}

type RandomNumberParams struct {
	Start        int64 `json:"start"`
	End          int64 `json:"end"`
	IncludeStart *bool `json:"include_start,omitempty"`
	IncludeEnd   *bool `json:"include_end,omitempty"`
}

func NewRandomNumberTool() *RandomNumberTool {
	return &RandomNumberTool{}
}

func (t *RandomNumberTool) Name() string {
	return "random_number"
}

func (t *RandomNumberTool) Description() string {
	return "Generate a cryptographically secure random integer within a configurable range. Supports inclusive or exclusive start/end bounds."
}

func (t *RandomNumberTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"start": map[string]interface{}{
				"type":        "integer",
				"description": "Range start bound.",
			},
			"end": map[string]interface{}{
				"type":        "integer",
				"description": "Range end bound.",
			},
			"include_start": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether the start bound is inclusive. Defaults to true.",
			},
			"include_end": map[string]interface{}{
				"type":        "boolean",
				"description": "Whether the end bound is inclusive. Defaults to true.",
			},
		},
		"required": []string{"start", "end"},
	}
}

func (t *RandomNumberTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p RandomNumberParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	includeStart := true
	if p.IncludeStart != nil {
		includeStart = *p.IncludeStart
	}
	includeEnd := true
	if p.IncludeEnd != nil {
		includeEnd = *p.IncludeEnd
	}

	effectiveStart := p.Start
	if !includeStart {
		if p.Start == math.MaxInt64 {
			return &Result{Success: false, Error: "exclusive start at max int64 leaves no valid integers"}, nil
		}
		effectiveStart++
	}

	effectiveEnd := p.End
	if !includeEnd {
		if p.End == math.MinInt64 {
			return &Result{Success: false, Error: "exclusive end at min int64 leaves no valid integers"}, nil
		}
		effectiveEnd--
	}

	if effectiveStart > effectiveEnd {
		return &Result{Success: false, Error: "range contains no integers after applying inclusivity rules"}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	lower := big.NewInt(effectiveStart)
	span := new(big.Int).Sub(big.NewInt(effectiveEnd), lower)
	span.Add(span, big.NewInt(1))

	n, err := rand.Int(rand.Reader, span)
	if err != nil {
		return nil, fmt.Errorf("generate random number: %w", err)
	}

	value := new(big.Int).Add(lower, n)

	return &Result{
		Success: true,
		Output:  value.String(),
		Metadata: map[string]interface{}{
			"start":           p.Start,
			"end":             p.End,
			"include_start":   includeStart,
			"include_end":     includeEnd,
			"effective_start": effectiveStart,
			"effective_end":   effectiveEnd,
			"value":           value.String(),
		},
	}, nil
}

var _ Tool = (*RandomNumberTool)(nil)
