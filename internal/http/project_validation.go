// project_validation.go keeps project normalization and validation helpers focused after the split.
package http

import (
	"fmt"
	"net/url"
	"strings"
)

func normalizeFolder(folder *string) *string {
	if folder == nil {
		return nil
	}
	normalized := strings.TrimSpace(*folder)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func normalizeProjectURLPatternsForResponse(patterns []string) []string {
	normalized, err := normalizeProjectURLPatterns(patterns)
	if err != nil {
		return []string{}
	}
	return normalized
}

func normalizeProjectURLPatterns(patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return []string{}, nil
	}
	if len(patterns) > 500 {
		return nil, fmt.Errorf("project URL patterns are limited to 500 entries")
	}
	out := make([]string, 0, len(patterns))
	seen := make(map[string]struct{}, len(patterns))
	for _, raw := range patterns {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}
		if _, exists := seen[pattern]; exists {
			continue
		}
		if err := validateProjectURLPattern(pattern); err != nil {
			return nil, err
		}
		seen[pattern] = struct{}{}
		out = append(out, pattern)
	}
	return out, nil
}

func validateProjectURLPattern(pattern string) error {

	if len(pattern) > 2048 {
		return fmt.Errorf("invalid project URL pattern %q: pattern is too long", pattern)
	}
	if strings.ContainsAny(pattern, " \t\r\n{}()[]\\") {
		return fmt.Errorf("invalid project URL pattern %q: use absolute URLs with literal components and '*' wildcards only", pattern)
	}
	separator := strings.Index(pattern, "://")
	if separator <= 0 {
		return fmt.Errorf("invalid project URL pattern %q: pattern must be an absolute URL such as https://example.com/*", pattern)
	}
	scheme := pattern[:separator]
	if strings.Contains(scheme, "*") || !isLiteralURLScheme(scheme) {
		return fmt.Errorf("invalid project URL pattern %q: URL scheme must be literal", pattern)
	}
	remainder := pattern[separator+3:]
	if remainder == "" || strings.HasPrefix(remainder, "/") {
		return fmt.Errorf("invalid project URL pattern %q: URL host is required", pattern)
	}
	endOfAuthority := len(remainder)
	for _, marker := range []string{"/", "?", "#"} {
		if idx := strings.Index(remainder, marker); idx >= 0 && idx < endOfAuthority {
			endOfAuthority = idx
		}
	}
	authority := remainder[:endOfAuthority]
	if authority == "" {
		return fmt.Errorf("invalid project URL pattern %q: URL host is required", pattern)
	}
	if strings.Contains(authority, "@") {
		return fmt.Errorf("invalid project URL pattern %q: credentials are not supported in project URL patterns", pattern)
	}

	probe := strings.ReplaceAll(pattern, "*", "wildcard")
	if parsed, err := url.Parse(probe); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid project URL pattern %q: pattern must be parseable as an absolute URL", pattern)
	}
	return nil
}

func isLiteralURLScheme(scheme string) bool {
	if scheme == "" {
		return false
	}
	for i, r := range scheme {
		if i == 0 {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
				return false
			}
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '+' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func normalizeProjectSettings(settings map[string]string) map[string]string {
	if len(settings) == 0 {
		return map[string]string{}
	}
	normalized := make(map[string]string, len(settings))
	for key, value := range settings {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		normalized[trimmedKey] = strings.TrimSpace(value)
	}
	return normalized
}

func normalizeFolders(folders []string) []string {
	if len(folders) == 0 {
		return []string{}
	}

	normalized := make([]string, 0, len(folders))
	seen := make(map[string]struct{}, len(folders))
	for _, raw := range folders {
		folder := strings.TrimSpace(raw)
		if folder == "" {
			continue
		}
		if _, exists := seen[folder]; exists {
			continue
		}
		seen[folder] = struct{}{}
		normalized = append(normalized, folder)
	}

	return normalized
}
