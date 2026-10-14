package anthropic

import "testing"

func TestContextWindow(t *testing.T) {
	cases := map[string]int{
		"opus":                  1000000,
		"sonnet":                1000000,
		"haiku":                 1000000,
		"claude-sonnet-5-5":     1000000,
		"claude-opus-4-8":       1000000,
		"claude-sonnet-4.6":     1000000,
		"claude-sonnet-4-5[1m]": 1000000,
		"claude-haiku-4-5":      0,
		"claude-sonnet-4-5":     0,
	}
	for model, want := range cases {
		if got := ContextWindow(model); got != want {
			t.Errorf("%s: got %d, want %d", model, got, want)
		}
	}
}
