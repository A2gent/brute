package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// headTail keeps both ends through the existing filter instead of letting its
// default head-only window hide definitions at the end of a candidate file.
func (t *FilterTool) headTail(ctx context.Context, input string, maxBytes int) (string, error) {
	input = relevanceHeadTail(input, maxBytes)
	if strings.TrimSpace(input) == "" {
		return "", nil
	}
	params, err := json.Marshal(FilterParams{Input: input, IncludeEmpty: true, MaxLines: filterHardMaxLines, MaxOutputChars: filterHardMaxOutChars})
	if err != nil {
		return "", err
	}
	result, err := t.Execute(ctx, params)
	if err != nil {
		return "", err
	}
	if result == nil || !result.Success {
		return "", fmt.Errorf("head/tail filter failed")
	}
	return result.Output, nil
}

func relevanceHeadTail(input string, maxBytes int) string {
	if len(input) <= maxBytes {
		return input
	}
	budget := maxBytes - len("\n[truncated]\n")
	return relevanceJoinEnds(input[:budget/2], input[len(input)-(budget-budget/2):])
}

func relevanceJoinEnds(head, tail string) string {
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	for len(tail) > 0 && !utf8.ValidString(tail) {
		tail = tail[1:]
	}
	return head + "\n[truncated]\n" + tail
}
