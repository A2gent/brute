package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/llm/jev"
)

const (
	classifyMaxStateBytes    = 64 * 1024
	classifyTimeout          = 30 * time.Second
	classifyTruncationMarker = "\n[truncated]\n"
)

type ClassifyTool struct {
	workDir string
	client  *jev.Client
	timeout time.Duration
}

type ClassifyParams struct {
	Input    string            `json:"input,omitempty"`
	Path     string            `json:"path,omitempty"`
	Question string            `json:"question"`
	Type     string            `json:"type"`
	Criteria map[string]string `json:"criteria,omitempty"`
}

func NewClassifyTool(workDir string, client *jev.Client) *ClassifyTool {
	return &ClassifyTool{workDir: workDir, client: client, timeout: classifyTimeout}
}

func (t *ClassifyTool) Name() string { return "classify" }
func (t *ClassifyTool) Description() string {
	return "Classify text or a file with Jev (TypeSafe System One). Returns compact JSON with answer, confidence and probabilities. State is capped at 64 KiB by preserving its head and tail; requests time out after 30 seconds. Noul returns a yes probability, with null confidence/probabilities (not supplied by the API)."
}
func (t *ClassifyTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"input":    map[string]interface{}{"type": "string", "description": "Text to classify. Supply exactly one of input or path."},
			"path":     map[string]interface{}{"type": "string", "description": "File to classify, relative to the session working directory or absolute."},
			"question": map[string]interface{}{"type": "string", "description": "Classification instructions/question."},
			"type":     map[string]interface{}{"type": "string", "enum": []string{"choice", "score", "noul"}},
			"criteria": map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "string"}, "description": "Choice: option key to description (2-10 options). Score: consecutive keys 0..N with low-to-high descriptions (2-10 levels). Noul: optional true/false descriptions."},
		},
		"required": []string{"question", "type"},
	}
}

func (t *ClassifyTool) Execute(ctx context.Context, raw json.RawMessage) (*Result, error) {
	fail := func(err error) (*Result, error) { return &Result{Success: false, Error: err.Error()}, nil }
	var params ClassifyParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return fail(fmt.Errorf("invalid classify parameters: %w", err))
	}
	if strings.TrimSpace(params.Question) == "" {
		return fail(fmt.Errorf("question is required"))
	}
	if (params.Input == "") == (params.Path == "") {
		return fail(fmt.Errorf("provide exactly one of input or path"))
	}
	var criteria any
	switch params.Type {
	case "choice", "score":
		if len(params.Criteria) < 2 || len(params.Criteria) > 10 {
			return fail(fmt.Errorf("%s criteria must contain 2-10 entries", params.Type))
		}
		for _, value := range params.Criteria {
			if strings.TrimSpace(value) == "" {
				return fail(fmt.Errorf("criteria descriptions must not be empty"))
			}
		}
		criteria = params.Criteria
		if params.Type == "score" {
			// Score uses an ordered array on the wire, not a map. Explicit indices avoid
			// nondeterministic map iteration changing the meaning of the returned score.
			levels := make([]string, len(params.Criteria))
			for i := range levels {
				value, ok := params.Criteria[strconv.Itoa(i)]
				if !ok {
					return fail(fmt.Errorf("score criteria keys must be consecutive integers starting at 0"))
				}
				levels[i] = value
			}
			criteria = levels
		}
	case "noul":
		if len(params.Criteria) > 0 {
			criteria = params.Criteria
		}
	default:
		return fail(fmt.Errorf("type must be choice, score or noul"))
	}
	if t.client == nil {
		return fail(fmt.Errorf("Jev API key is not configured"))
	}
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	state, err := t.state(params)
	if err != nil {
		return fail(err)
	}
	response, err := t.client.SystemOne(ctx, jev.SystemOneRequest{
		State: state, Questions: map[string]jev.Question{"answer": {Type: params.Type, Instructions: params.Question, Criteria: criteria}},
	})
	if err != nil {
		return fail(err)
	}
	answer, ok := response.Answers["answer"]
	if !ok {
		return fail(fmt.Errorf("Jev returned no answer"))
	}
	var value any
	var confidence any = answer.Confidence
	switch params.Type {
	case "choice":
		if answer.Choice == "" {
			return fail(fmt.Errorf("Jev returned no choice answer"))
		}
		value = answer.Choice
	case "score":
		if answer.Score == nil {
			return fail(fmt.Errorf("Jev returned no score answer"))
		}
		value = *answer.Score
	case "noul":
		if answer.Noul == nil {
			return fail(fmt.Errorf("Jev returned no noul answer"))
		}
		value = *answer.Noul
		confidence = nil
	}
	output, err := json.Marshal(struct {
		Answer        any                `json:"answer"`
		Confidence    any                `json:"confidence"`
		Probabilities map[string]float64 `json:"probabilities"`
	}{value, confidence, answer.Probabilities})
	if err != nil {
		return fail(err)
	}
	return &Result{Success: true, Output: string(output)}, nil
}

func (t *ClassifyTool) state(params ClassifyParams) (string, error) {
	if params.Input != "" {
		if !utf8.ValidString(params.Input) {
			return "", fmt.Errorf("input must be UTF-8 text")
		}
		if len(params.Input) <= classifyMaxStateBytes {
			return params.Input, nil
		}
		headBytes, tailBytes := classifyHeadTailSizes()
		return classifyHeadTail(params.Input[:headBytes], params.Input[len(params.Input)-tailBytes:])
	}
	path := params.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(t.workDir, path)
	}
	file, err := openClassifyFile(path)
	if err != nil {
		return "", fmt.Errorf("read classify path: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("read classify path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("classify path must be a regular file")
	}
	// Read only the retained ends so classifying a large log does not load it all.
	if info.Size() <= classifyMaxStateBytes {
		data := make([]byte, int(info.Size()))
		if _, err := file.ReadAt(data, 0); err != nil {
			return "", fmt.Errorf("read classify path: %w", err)
		}
		if !utf8.Valid(data) {
			return "", fmt.Errorf("classify path must contain UTF-8 text")
		}
		return string(data), nil
	}
	headBytes, tailBytes := classifyHeadTailSizes()
	head, tail := make([]byte, headBytes), make([]byte, tailBytes)
	if _, err := file.ReadAt(head, 0); err != nil {
		return "", fmt.Errorf("read classify path: %w", err)
	}
	if _, err := file.ReadAt(tail, info.Size()-int64(tailBytes)); err != nil {
		return "", fmt.Errorf("read classify path: %w", err)
	}
	return classifyHeadTail(string(head), string(tail))
}

func classifyHeadTailSizes() (int, int) {
	budget := classifyMaxStateBytes - len(classifyTruncationMarker)
	return budget / 2, budget - budget/2
}

func classifyHeadTail(head, tail string) (string, error) {
	// Byte caps can split a UTF-8 rune at either truncation boundary.
	for i := 0; i < utf8.UTFMax-1 && len(head) > 0; i++ {
		r, size := utf8.DecodeLastRuneInString(head)
		if r != utf8.RuneError || size != 1 {
			break
		}
		head = head[:len(head)-1]
	}
	for i := 0; i < utf8.UTFMax-1 && len(tail) > 0 && !utf8.RuneStart(tail[0]); i++ {
		tail = tail[1:]
	}
	if !utf8.ValidString(head) || !utf8.ValidString(tail) {
		return "", fmt.Errorf("state must contain UTF-8 text")
	}
	return head + classifyTruncationMarker + tail, nil
}
