package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	parallelMaxSteps           = 12
	parallelDefaultOutputChars = 12000
	parallelMaxOutputChars     = 200000
	parallelStepTimeout        = 90 * time.Second
)

type parallelContextKey struct{}

var parallelUnsupportedTools = map[string]string{
	"parallel":             "recursive parallel calls are not allowed",
	"delegate_to_subagent": "sub-agent delegation must be called as a top-level tool call",
	"browser_chrome":       "browser automation is stateful and must be called sequentially as a top-level tool call",
}

// ParallelTool executes independent tool calls concurrently and returns ordered results.
type ParallelTool struct {
	manager *Manager
}

type ParallelParams struct {
	Steps          []ParallelStep `json:"steps"`
	MaxOutputChars int            `json:"max_output_chars,omitempty"`
}

type ParallelStep struct {
	Tool       string                     `json:"tool"`
	Args       json.RawMessage            `json:"args,omitempty"`
	InlineArgs map[string]json.RawMessage `json:"-"`
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
	return "Run multiple independent tool calls concurrently in one call. Use this for parallel codebase exploration, such as several grep/read/find_files/bash searches that do not depend on each other. Do not use this for recursive parallel calls, delegate_to_subagent, or browser_chrome; call those as top-level tool calls instead."
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
							"description": "Tool name to execute for this parallel step. Cannot be parallel, delegate_to_subagent, or browser_chrome.",
						},
						"args": map[string]interface{}{
							"type":        "object",
							"description": "Arguments for the step tool. For convenience, tool arguments may also be placed directly on the step object.",
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
	resultChans := make([]chan parallelStepOutput, 0, len(p.Steps))

	for i, step := range p.Steps {
		toolName := normalizeToolName(step.Tool)
		if toolName == "" {
			return &Result{Success: false, Error: fmt.Sprintf("step %d: tool is required", i+1)}, nil
		}
		if reason, ok := parallelUnsupportedTools[toolName]; ok {
			return &Result{Success: false, Error: fmt.Sprintf("step %d: %s", i+1, reason)}, nil
		}
		args, err := decodeParallelStepArgs(step)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("step %d: %v", i+1, err)}, nil
		}
		stepParams, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("step %d: failed to serialize step args: %w", i+1, err)
		}

		resultCh := make(chan parallelStepOutput, 1)
		resultChans = append(resultChans, resultCh)
		go func(idx int, name string, raw json.RawMessage) {
			start := time.Now()
			stepCtx, cancel := context.WithTimeout(ctx, parallelStepTimeout)
			defer cancel()

			stepResult, err := t.manager.Execute(stepCtx, name, raw)
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

			resultCh <- out
		}(i, toolName, stepParams)
	}

	for i, resultCh := range resultChans {
		select {
		case out := <-resultCh:
			results[i] = out
		case <-ctx.Done():
			results[i] = parallelStepOutput{
				Step:    i + 1,
				Tool:    normalizeToolName(p.Steps[i].Tool),
				Success: false,
				Error:   ctx.Err().Error(),
			}
		case <-time.After(parallelStepTimeout):
			results[i] = parallelStepOutput{
				Step:       i + 1,
				Tool:       normalizeToolName(p.Steps[i].Tool),
				Success:    false,
				Error:      fmt.Sprintf("parallel step timed out after %s", parallelStepTimeout),
				DurationMs: parallelStepTimeout.Milliseconds(),
			}
		}
	}

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

func (s *ParallelStep) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	if toolRaw, ok := raw["tool"]; ok {
		if err := json.Unmarshal(toolRaw, &s.Tool); err != nil {
			return fmt.Errorf("tool must be a string: %w", err)
		}
	}
	if argsRaw, ok := raw["args"]; ok {
		s.Args = argsRaw
	}

	inline := make(map[string]json.RawMessage, len(raw))
	for key, value := range raw {
		if key == "tool" || key == "args" {
			continue
		}
		inline[key] = value
	}
	if len(inline) > 0 {
		s.InlineArgs = inline
	}
	return nil
}

func decodeParallelStepArgs(step ParallelStep) (map[string]interface{}, error) {
	if len(step.Args) > 0 {
		return decodeStageArgs(step.Args)
	}
	if len(step.InlineArgs) == 0 {
		return map[string]interface{}{}, nil
	}

	raw, err := json.Marshal(step.InlineArgs)
	if err != nil {
		return nil, fmt.Errorf("failed to encode inline args: %w", err)
	}
	return decodeStageArgs(raw)
}

// Ensure ParallelTool implements Tool.
var _ Tool = (*ParallelTool)(nil)
