package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	pipelineMaxSteps           = 12
	pipelineDefaultOutputChars = 12000
	pipelineMaxOutputChars     = 200000
)

type pipelineContextKey struct{}

// PipelineTool executes multiple tools sequentially and passes output forward.
// This keeps intermediate outputs out of the LLM context.
type PipelineTool struct {
	manager *Manager
}

type PipelineParams struct {
	Steps          []PipelineStep `json:"steps"`
	MaxOutputChars int            `json:"max_output_chars,omitempty"`
}

type PipelineStep struct {
	Tool          string          `json:"tool"`
	Args          json.RawMessage `json:"args,omitempty"`
	InputFromPrev bool            `json:"input_from_prev,omitempty"`
	InputKey      string          `json:"input_key,omitempty"`
}

func NewPipelineTool(manager *Manager) *PipelineTool {
	return &PipelineTool{manager: manager}
}

func (t *PipelineTool) Name() string {
	return "pipeline"
}

func (t *PipelineTool) Description() string {
	return "Run a sequence of tools in one call. Each step can receive the previous step output, reducing LLM context usage."
}

func (t *PipelineTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"steps": map[string]interface{}{
				"type":        "array",
				"description": "Ordered tool steps to execute sequentially.",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"tool": map[string]interface{}{
							"type":        "string",
							"description": "Tool name to execute for this stage.",
						},
						"args": map[string]interface{}{
							"type":        "object",
							"description": "Arguments for the stage tool.",
						},
						"input_from_prev": map[string]interface{}{
							"type":        "boolean",
							"description": "If true, inject previous stage output into args[input_key] (or args.input by default).",
						},
						"input_key": map[string]interface{}{
							"type":        "string",
							"description": "Argument key to receive previous output (default: input).",
						},
					},
					"required": []string{"tool"},
				},
			},
			"max_output_chars": map[string]interface{}{
				"type":        "integer",
				"description": "Max characters returned from the final stage output (default: 12000, max: 200000). Final relevance_gate verdicts are exempt to preserve all paths and full fallback.",
			},
		},
		"required": []string{"steps"},
	}
}

func (t *PipelineTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var p PipelineParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if t.manager == nil {
		return &Result{Success: false, Error: "pipeline tool manager is not configured"}, nil
	}
	if len(p.Steps) == 0 {
		return &Result{Success: false, Error: "steps is required"}, nil
	}
	if len(p.Steps) > pipelineMaxSteps {
		return &Result{Success: false, Error: fmt.Sprintf("too many pipeline steps (%d > %d)", len(p.Steps), pipelineMaxSteps)}, nil
	}

	depth := 0
	if v := ctx.Value(pipelineContextKey{}); v != nil {
		if n, ok := v.(int); ok {
			depth = n
		}
	}
	if depth >= 3 {
		return &Result{Success: false, Error: "pipeline nesting depth exceeded"}, nil
	}
	ctx = context.WithValue(ctx, pipelineContextKey{}, depth+1)

	maxChars := p.MaxOutputChars
	if maxChars <= 0 {
		maxChars = pipelineDefaultOutputChars
	}
	if maxChars > pipelineMaxOutputChars {
		maxChars = pipelineMaxOutputChars
	}

	prevOutput := ""
	stageMeta := make([]map[string]interface{}, 0, len(p.Steps))
	var images []interface{}
	commandMetadata := make(map[string]interface{})

	for i, stage := range p.Steps {
		toolName := normalizeToolName(stage.Tool)
		if toolName == "" {
			return &Result{Success: false, Error: fmt.Sprintf("step %d: tool is required", i+1)}, nil
		}
		if toolName == t.Name() {
			return &Result{Success: false, Error: fmt.Sprintf("step %d: recursive pipeline call is not allowed", i+1)}, nil
		}

		args, err := decodeStageArgs(stage.Args)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("step %d: %v", i+1, err)}, nil
		}

		if i > 0 && (stage.InputFromPrev || strings.TrimSpace(stage.InputKey) != "") {
			inputKey := strings.TrimSpace(stage.InputKey)
			if inputKey == "" {
				inputKey = "input"
			}
			args[inputKey] = prevOutput
			// Native search paths are relative to that search's root, not necessarily
			// the session cwd. Resolve locally before injecting into the gate.
			previous := p.Steps[i-1]
			previousName := normalizeToolName(previous.Tool)
			if toolName == "relevance_gate" && inputKey == "input" && (previousName == "file_search" || previousName == "content_search") {
				searchArgs, _ := decodeStageArgs(previous.Args)
				searchRoot, _ := searchArgs["path"].(string)
				root, err := filepath.Abs(resolveToolPath(t.manager.WorkDir(), searchRoot))
				if err != nil {
					return &Result{Success: false, Error: fmt.Sprintf("step %d: resolve search root: %v", i+1, err)}, nil
				}
				paths := relevanceSearchPaths(prevOutput)
				for j, path := range paths {
					if !filepath.IsAbs(path) {
						paths[j] = filepath.Join(root, path)
					}
				}
				args[inputKey] = paths
			}
		}

		stageParams, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("step %d: failed to serialize stage args: %w", i+1, err)
		}

		start := time.Now()
		stageResult, err := t.manager.Execute(ctx, toolName, stageParams)
		duration := time.Since(start)
		stageInfo := map[string]interface{}{
			"step":        i + 1,
			"tool":        toolName,
			"duration_ms": duration.Milliseconds(),
		}

		if err != nil {
			stageInfo["success"] = false
			stageMeta = append(stageMeta, stageInfo)
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("step %d (%s) failed: %v", i+1, toolName, err),
				Metadata: map[string]interface{}{
					"pipeline_steps": stageMeta,
				},
			}, nil
		}
		if stageResult == nil {
			stageInfo["success"] = false
			stageMeta = append(stageMeta, stageInfo)
			return &Result{
				Success: false,
				Error:   fmt.Sprintf("step %d (%s) returned no result", i+1, toolName),
				Metadata: map[string]interface{}{
					"pipeline_steps": stageMeta,
				},
			}, nil
		}
		if childImages, ok := stageResult.Metadata["admission_images"].([]interface{}); ok {
			images = append(images, childImages...)
		}
		commandMetadata = make(map[string]interface{})
		for key, value := range stageResult.Metadata {
			commandMetadata[key] = value
		}
		if toolName == "bash" {
			commandMetadata["command_output"] = true
		}
		if !stageResult.Success {
			stageInfo["success"] = false
			stageInfo["output_chars"] = len(stageResult.Output)
			stageMeta = append(stageMeta, stageInfo)
			errMsg := strings.TrimSpace(stageResult.Error)
			if errMsg == "" {
				errMsg = "tool returned unsuccessful result"
			}
			output := stageResult.Output
			if toolName != "bash" && commandMetadata["command_output"] != true && commandMetadata["command_output_kind"] != "parallel" {
				output = truncateToChars(output, maxChars)
			}
			commandMetadata["pipeline_steps"] = stageMeta
			return &Result{Success: false, Error: fmt.Sprintf("step %d (%s) failed: %s", i+1, toolName, errMsg), Output: output, Metadata: commandMetadata}, nil
		}

		prevOutput = stageResult.Output
		stageInfo["success"] = true
		stageInfo["output_chars"] = len(prevOutput)
		stageMeta = append(stageMeta, stageInfo)
	}

	// Keep gate verdicts lossless: truncation hides skipped paths and full fallback.
	finalOutput, truncated := prevOutput, false
	if command, _ := commandMetadata["command_output"].(bool); !command && commandMetadata["command_output_kind"] != "parallel" && normalizeToolName(p.Steps[len(p.Steps)-1].Tool) != "relevance_gate" {
		finalOutput, truncated = truncateWithFlag(prevOutput, maxChars)
	}
	commandMetadata["pipeline_steps"] = stageMeta
	commandMetadata["final_output_chars"] = len(prevOutput)
	commandMetadata["final_output_truncated"] = truncated
	commandMetadata["admission_images"] = images
	return &Result{Success: true, Output: finalOutput, Metadata: commandMetadata}, nil
}

func decodeStageArgs(raw json.RawMessage) (map[string]interface{}, error) {
	if len(raw) == 0 {
		return map[string]interface{}{}, nil
	}

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return map[string]interface{}{}, nil
	}

	var args map[string]interface{}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("args must be an object: %w", err)
	}
	if args == nil {
		args = map[string]interface{}{}
	}
	return args, nil
}

func truncateToChars(s string, limit int) string {
	out, _ := truncateWithFlag(s, limit)
	return out
}

func truncateWithFlag(s string, limit int) (string, bool) {
	if limit <= 0 {
		return s, false
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s, false
	}
	return string(runes[:limit]) + "\n... (output truncated)", true
}

// Ensure PipelineTool implements Tool.
var _ Tool = (*PipelineTool)(nil)
