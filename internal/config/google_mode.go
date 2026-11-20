package config

import (
	"fmt"
	"strings"
)

const (
	GoogleAuthModeAPIKey      = "api_key"
	GoogleAuthModeAntigravity = "antigravity"
)

func NormalizeGoogleAuthMode(raw string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(raw)); mode {
	case "", GoogleAuthModeAPIKey:
		return GoogleAuthModeAPIKey, nil
	case GoogleAuthModeAntigravity:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported Google auth mode %q (use %s or %s)", raw, GoogleAuthModeAPIKey, GoogleAuthModeAntigravity)
	}
}

func (c *Config) GoogleUsesAntigravity() bool {
	if c == nil {
		return false
	}
	mode, _ := NormalizeGoogleAuthMode(c.Providers[string(ProviderGoogle)].AuthMode)
	return mode == GoogleAuthModeAntigravity
}

func (c *Config) IsAntigravityRef(ref string) bool {
	return NormalizeProviderRef(ref) == string(ProviderGoogle) && c.GoogleUsesAntigravity()
}
