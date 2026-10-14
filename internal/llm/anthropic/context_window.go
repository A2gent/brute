package anthropic

import "strings"

// oneMillionModels lists model families with a 1M window in Claude Code, per
// https://support.claude.com/en/articles/8606394. Anything else (e.g. Haiku 4.5, Sonnet 4.5)
// keeps the 200K provider default. Dots and dashes are normalized so "4.6" matches "4-6".
var oneMillionModels = []string{
	"fable-5", "fable-5-1", "mythos-5", "mythos-5-1", "opus-5", "sonnet-5", "haiku-5-5", "opus-4-6", "opus-4-7", "opus-4-8", "sonnet-4-6",
}

// ContextWindow returns the model-specific context window, or 0 when the model has no
// known override and the provider default should apply.
func ContextWindow(model string) int {
	model = strings.ToLower(strings.TrimSpace(model))
	switch model {
	case "opus", "sonnet", "haiku":
		return 1000000
	}
	if strings.Contains(model, "[1m]") {
		return 1000000
	}
	normalized := strings.ReplaceAll(model, ".", "-")
	for _, family := range oneMillionModels {
		if strings.Contains(normalized, family) {
			return 1000000
		}
	}
	return 0
}
