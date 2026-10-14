package config

import "testing"

func TestResolveContextWindowAnthropicModels(t *testing.T) {
	cases := map[string]int{
		"claude-sonnet-5-5":     1000000,
		"claude-opus-4-8":       1000000,
		"claude-sonnet-4.6":     1000000,
		"claude-sonnet-4-5[1m]": 1000000,
		"claude-haiku-4-5":      200000,
		"claude-sonnet-4-5":     200000,
	}
	for model, want := range cases {
		if got := ResolveContextWindow(ProviderAnthropic, Provider{}, model); got != want {
			t.Errorf("%s: got %d, want %d", model, got, want)
		}
	}
}
