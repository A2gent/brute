package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

// Predicates intentionally support only scalar equality and numeric thresholds,
// not an expression language. A gate forwards input identity for later reads.
type PipelinePredicate struct {
	Field string      `json:"field"`
	Op    string      `json:"op"`
	Value interface{} `json:"value"`
}

func pipelinePredicateSchema(description string) map[string]interface{} {
	return map[string]interface{}{
		"type": "object", "description": description,
		"properties": map[string]interface{}{
			"field": map[string]interface{}{"type": "string", "description": "Top-level JSON output field (classify uses answer). Missing fields or invalid JSON fail the pipeline."},
			"op":    map[string]interface{}{"type": "string", "enum": []string{"eq", "gte"}},
			"value": map[string]interface{}{"description": "Scalar comparison value; gte requires a number.", "anyOf": []interface{}{map[string]interface{}{"type": "string"}, map[string]interface{}{"type": "number"}, map[string]interface{}{"type": "boolean"}}},
		},
		"required": []string{"field", "op", "value"},
	}
}

func validatePipelineItems(stage PipelineStep, index int) error {
	if !stage.PerItem {
		if stage.KeepIf != nil || stage.DropIf != nil || stage.MaxItems != 0 {
			return fmt.Errorf("max_items, keep_if and drop_if require per_item")
		}
		return nil
	}
	if index == 0 {
		return fmt.Errorf("per_item requires a previous step")
	}
	if stage.MaxItems < 0 || stage.MaxItems > parallelMaxSteps {
		return fmt.Errorf("max_items must be between 1 and %d", parallelMaxSteps)
	}
	if stage.KeepIf != nil && stage.DropIf != nil {
		return fmt.Errorf("keep_if and drop_if cannot be combined")
	}
	if reason, ok := parallelUnsupportedTools[normalizeToolName(stage.Tool)]; ok {
		return fmt.Errorf("%s", reason)
	}
	if parallelSequentialTools[normalizeToolName(stage.Tool)] {
		return fmt.Errorf("%s is stateful and cannot run per_item", stage.Tool)
	}
	for _, predicate := range []*PipelinePredicate{stage.KeepIf, stage.DropIf} {
		if predicate == nil {
			continue
		}
		if strings.TrimSpace(predicate.Field) == "" || (predicate.Op != "eq" && predicate.Op != "gte") {
			return fmt.Errorf("predicate requires field and op eq or gte")
		}
		switch predicate.Value.(type) {
		case string, bool, float64:
		default:
			return fmt.Errorf("predicate value must be a scalar")
		}
		if predicate.Op == "gte" {
			if _, ok := predicate.Value.(float64); !ok {
				return fmt.Errorf("gte predicate requires a numeric value")
			}
		}
	}
	return nil
}

func pipelineItems(output string) ([]interface{}, error) {
	items := make([]interface{}, 0)
	if strings.HasPrefix(strings.TrimSpace(output), "[") {
		if err := json.Unmarshal([]byte(output), &items); err != nil {
			return nil, fmt.Errorf("invalid JSON items: %w", err)
		}
		return items, nil
	}
	return pipelineLines(output), nil
}

func pipelineLines(output string) []interface{} {
	items := make([]interface{}, 0)
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			items = append(items, line)
		}
	}
	return items
}

var pipelineInvalidPage = regexp.MustCompile(`^Page [0-9]+ does not exist\. Total pages: [0-9]+$`)

func (t *PipelineTool) executePipelineItems(ctx context.Context, stage, previous PipelineStep, searchArgs map[string]interface{}, output string, args map[string]interface{}) (*Result, error) {
	// Search tools decorate their text output. Strip only known decorations and
	// resolve paths once, so downstream gates preserve usable absolute identity.
	name := normalizeToolName(previous.Tool)
	mode, _ := searchArgs["mode"].(string)
	pathSearch := !previous.PerItem && (name == "find_files" || (name == "grep" && strings.EqualFold(strings.TrimSpace(mode), "files")))
	if pathSearch {
		if name == "find_files" && pipelineInvalidPage.MatchString(output) {
			return nil, fmt.Errorf("search page contains no items: %s", output)
		}
		if strings.TrimSpace(output) == "No files found" || strings.TrimSpace(output) == "No matches found" {
			output = ""
		}
		if name == "find_files" {
			output = strings.SplitN(output, "\n\n", 2)[0]
		}
	}
	var items []interface{}
	if pathSearch {
		items = pipelineLines(output)
	} else {
		var err error
		items, err = pipelineItems(output)
		if err != nil {
			return nil, err
		}
	}
	limit := stage.MaxItems
	if limit == 0 {
		limit = parallelMaxSteps
	}
	if len(items) > limit {
		return nil, fmt.Errorf("too many per-item inputs (%d > %d); narrow or paginate the search", len(items), limit)
	}
	if pathSearch {
		searchRoot, _ := searchArgs["path"].(string)
		root, err := filepath.Abs(resolveToolPath(t.manager.WorkDir(), searchRoot))
		if err != nil {
			return nil, err
		}
		for i, item := range items {
			path, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("search item %d is not a path", i+1)
			}
			if !filepath.IsAbs(path) {
				items[i] = filepath.Join(root, path)
			}
		}
	}
	if len(items) == 0 {
		return &Result{Success: true, Output: "[]"}, nil
	}
	key := strings.TrimSpace(stage.InputKey)
	if key == "" {
		key = "input"
	}
	steps := make([]ParallelStep, len(items))
	for i, item := range items {
		args[key] = item
		raw, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		steps[i] = ParallelStep{Tool: stage.Tool, Args: raw}
	}
	raw, err := json.Marshal(ParallelParams{Steps: steps})
	if err != nil {
		return nil, err
	}
	result, err := NewParallelTool(t.manager).execute(ctx, raw, false)
	if err != nil {
		return nil, err
	}
	var results []parallelStepOutput
	if err := json.Unmarshal([]byte(result.Output), &results); err != nil {
		return nil, err
	}
	forward := make([]interface{}, 0, len(items))
	metadata := make([]map[string]interface{}, 0, len(items))
	refs, _ := result.Metadata["read_cache_references"].(map[string]interface{})
	predicate := stage.KeepIf
	if predicate == nil {
		predicate = stage.DropIf
	}
	for i, child := range results {
		if !child.Success {
			return nil, fmt.Errorf("item %d failed: %s", i+1, child.Error)
		}
		metadata = append(metadata, child.Metadata)
		if predicate == nil {
			// Pipeline arrays are not understood by read-cache request repair.
			// Restore the captured body rather than forwarding a dangling stub.
			if ref, ok := refs[fmt.Sprint(child.Step)].(map[string]interface{}); ok && ref["stub"] == child.Output {
				if body, ok := ref["body"].(string); ok {
					child.Output = body
				}
			}
			forward = append(forward, child.Output)
			continue
		}
		match, err := predicate.matches(child.Output)
		if err != nil {
			return nil, fmt.Errorf("item %d predicate: %w", i+1, err)
		}
		if match == (stage.KeepIf != nil) {
			forward = append(forward, items[i])
		}
	}
	encoded, err := json.Marshal(forward)
	if err != nil {
		return nil, err
	}
	result.Output = string(encoded)
	// This is now an item array, not a parallel command envelope.
	if result.Metadata["command_output_kind"] == "parallel" {
		result.Metadata["command_output"] = true
	}
	delete(result.Metadata, "command_output_kind")
	delete(result.Metadata, "output_truncated_by")
	delete(result.Metadata, "read_cache_references")
	result.Metadata["item_metadata"] = metadata
	result.Metadata["input_items"] = len(items)
	result.Metadata["output_items"] = len(forward)
	return result, nil
}

func (p *PipelinePredicate) matches(output string) (bool, error) {
	var fields map[string]interface{}
	if err := json.Unmarshal([]byte(output), &fields); err != nil {
		return false, fmt.Errorf("invalid JSON output: %w", err)
	}
	value, ok := fields[p.Field]
	if !ok {
		return false, fmt.Errorf("missing output field %q", p.Field)
	}
	if p.Op == "eq" {
		return reflect.DeepEqual(value, p.Value), nil
	}
	number, ok := value.(float64)
	if !ok {
		return false, fmt.Errorf("field %q must be numeric", p.Field)
	}
	return number >= p.Value.(float64), nil
}
