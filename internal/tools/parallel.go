package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	parallelMaxSteps           = 12
	parallelDefaultOutputChars = 12000
	parallelMaxOutputChars     = 200000
)

type parallelContextKey struct{}

// ParallelTool executes independent tool calls concurrently and returns ordered results.
type ParallelTool struct {
	manager *Manager
}

type ParallelParams struct {
	Steps          []ParallelStep `json:"steps"`
	MaxOutputChars int            `json:"max_output_chars,omitempty"`
}

type ParallelStep struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args,omitempty"`
}

type parallelStepOutput struct {
	Step       int                    `json:"step"`
	Tool       string                 `json:"tool"`
	Success    bool                   `json:"success"`
	Output     string                 `json:"output,omitempty"`
	Error      string                 `json:"error,omitempty"`
	DurationMs int64                  `json:"duration_ms"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

func NewParallelTool(manager *Manager) *ParallelTool {
	return &ParallelTool{manager: manager}
}

func (t *ParallelTool) Name() string {
	return "parallel"
}

func (t *ParallelTool) Description() string {
	return "Run multiple independent tool calls concurrently in one call. Use this for parallel codebase exploration, such as several grep/read/find_files/bash searches that do not depend on each other."
}

func (t *ParallelTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"steps": map[string]interface{}{
				"type":        "array",
				"description": "Independent tool calls to execute concurrently. Results are returned in the same order.",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"tool": map[string]interface{}{
							"type":        "string",
							"description": "Tool name to execute for this parallel step.",
						},
						"args": map[string]interface{}{
							"type":        "object",
							"description": "Arguments for the step tool.",
						},
					},
					"required": []string{"tool"},
				},
			},
			"max_output_chars": map[string]interface{}{
				"type":        "integer",
				"description": "Max characters returned across all step outputs (default: 12000, max: 200000).",
			},
		},
		"required": []string{"steps"},
	}
}

func (t *ParallelTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p ParallelParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if t.manager == nil {
		return &Result{Success: false, Error: "parallel tool manager is not configured"}, nil
	}
	if len(p.Steps) == 0 {
		return &Result{Success: false, Error: "steps is required"}, nil
	}
	if len(p.Steps) > parallelMaxSteps {
		return &Result{Success: false, Error: fmt.Sprintf("too many parallel steps (%d > %d)", len(p.Steps), parallelMaxSteps)}, nil
	}

	depth := 0
	if v := ctx.Value(parallelContextKey{}); v != nil {
		if n, ok := v.(int); ok {
			depth = n
		}
	}
	if depth >= 3 {
		return &Result{Success: false, Error: "parallel nesting depth exceeded"}, nil
	}
	ctx = context.WithValue(ctx, parallelContextKey{}, depth+1)

	maxChars := p.MaxOutputChars
	if maxChars <= 0 {
		maxChars = parallelDefaultOutputChars
	}
	if maxChars > parallelMaxOutputChars {
		maxChars = parallelMaxOutputChars
	}

	results := make([]parallelStepOutput, len(p.Steps))
	var wg sync.WaitGroup

	for i, step := range p.Steps {
		toolName := strings.TrimSpace(step.Tool)
		if toolName == "" {
			return &Result{Success: false, Error: fmt.Sprintf("step %d: tool is required", i+1)}, nil
		}
		if toolName == t.Name() {
			return &Result{Success: false, Error: fmt.Sprintf("step %d: recursive parallel call is not allowed", i+1)}, nil
		}
		args, err := decodeStageArgs(step.Args)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("step %d: %v", i+1, err)}, nil
		}
		stepParams, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("step %d: failed to serialize step args: %w", i+1, err)
		}

		wg.Add(1)
		go func(idx int, name string, raw json.RawMessage) {
			defer wg.Done()

			start := time.Now()
			stepResult, err := t.manager.Execute(ctx, name, raw)
			duration := time.Since(start)
			out := parallelStepOutput{
				Step:       idx + 1,
				Tool:       name,
				DurationMs: duration.Milliseconds(),
			}

			if err != nil {
				out.Success = false
				out.Error = err.Error()
			} else if stepResult == nil {
				out.Success = false
				out.Error = "tool returned no result"
			} else if !stepResult.Success {
				out.Success = false
				out.Output = stepResult.Output
				out.Error = strings.TrimSpace(stepResult.Error)
				if out.Error == "" {
					out.Error = "tool returned unsuccessful result"
				}
				out.Metadata = stepResult.Metadata
			} else {
				out.Success = true
				out.Output = stepResult.Output
				out.Metadata = stepResult.Metadata
			}

			results[idx] = out
		}(i, toolName, stepParams)
	}

	wg.Wait()

	success := true
	totalOutputChars := 0
	maxPerStep := maxChars
	if len(results) > 0 {
		maxPerStep = maxChars / len(results)
		if maxPerStep < 1 {
			maxPerStep = 1
		}
	}
	for i := range results {
		if !results[i].Success {
			success = false
		}
		totalOutputChars += len(results[i].Output)
		results[i].Output = truncateToChars(results[i].Output, maxPerStep)
	}

	outputBytes, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode parallel results: %w", err)
	}

	return &Result{
		Success: success,
		Output:  string(outputBytes),
		Metadata: map[string]interface{}{
			"parallel_steps":      len(results),
			"total_output_chars":  totalOutputChars,
			"max_output_chars":    maxChars,
			"output_truncated_by": "per_step",
		},
	}, nil
}

// Ensure ParallelTool implements Tool.
var _ Tool = (*ParallelTool)(nil)
