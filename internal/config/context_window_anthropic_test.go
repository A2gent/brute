package config

import "testing"

func TestResolveContextWindowUsesAnthropicAPICache(t *testing.T) {
	ResetAnthropicModelContextCache()
	t.Cleanup(ResetAnthropicModelContextCache)
	CacheAnthropicModelContextWindows(map[string]int{
		"claude-opus-5-5": 900000,
		"opus":            900000,
	})

	for _, model := range []string{"claude-opus-5-5", "opus"} {
		if got := ResolveContextWindow(ProviderAnthropic, Provider{}, model); got != 900000 {
			t.Errorf("%s: got %d, want API-reported 900000", model, got)
		}
	}
}

func TestResolveContextWindowAnthropicModels(t *testing.T) {
	cases := map[string]int{
		"opus":                  1000000,
		"sonnet":                1000000,
		"haiku":                 1000000,
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
