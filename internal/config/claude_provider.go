package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const claudeInstancePrefix = "anthropic:"

var claudeEnvKeyRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

var forbiddenClaudeEnvKeys = map[string]struct{}{
	"HOME":              {},
	"PATH":              {},
	"CLAUDE_CONFIG_DIR": {},
}

// IsClaudeProviderRef reports whether ref is the legacy anthropic provider or a custom Claude instance.
func IsClaudeProviderRef(ref string) bool {
	normalized := NormalizeProviderRef(ref)
	if normalized == string(ProviderAnthropic) {
		return true
	}
	if strings.HasPrefix(normalized, claudeInstancePrefix) {
		return ClaudeInstanceIDFromRef(normalized) != ""
	}
	return false
}

// IsCustomClaudeInstanceRef reports whether ref is a non-base custom Claude CLI instance.
func IsCustomClaudeInstanceRef(ref string) bool {
	normalized := NormalizeProviderRef(ref)
	return strings.HasPrefix(normalized, claudeInstancePrefix) && ClaudeInstanceIDFromRef(normalized) != ""
}

// ClaudeInstanceIDFromRef extracts the instance id from anthropic:<id>.
func ClaudeInstanceIDFromRef(ref string) string {
	normalized := NormalizeProviderRef(ref)
	if !strings.HasPrefix(normalized, claudeInstancePrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(normalized, claudeInstancePrefix))
}

// ClaudeInstanceRefFromID builds anthropic:<normalized-id>.
func ClaudeInstanceRefFromID(id string) string {
	normalizedID := NormalizeToken(id)
	if normalizedID == "" {
		return ""
	}
	return claudeInstancePrefix + normalizedID
}

// GetProviderDefinitionForRef resolves built-in provider definitions, including custom Claude instance refs.
func GetProviderDefinitionForRef(ref string) *ProviderDefinition {
	normalized := NormalizeProviderRef(ref)
	if IsClaudeProviderRef(normalized) {
		return GetProviderDefinition(ProviderAnthropic)
	}
	return GetProviderDefinition(ProviderType(normalized))
}

// ValidateClaudeEnvKey validates override/secret env var names.
func ValidateClaudeEnvKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("env key cannot be empty")
	}
	if !claudeEnvKeyRe.MatchString(key) {
		return fmt.Errorf("invalid env key %q", key)
	}
	if _, forbidden := forbiddenClaudeEnvKeys[key]; forbidden {
		return fmt.Errorf("env key %q is managed by dedicated provider fields", key)
	}
	return nil
}

// ValidateClaudeProviderEnvMaps validates Claude override and secret env maps.
func ValidateClaudeProviderEnvMaps(provider Provider) error {
	for key := range provider.EnvOverrides {
		if err := ValidateClaudeEnvKey(key); err != nil {
			return err
		}
	}
	for key := range provider.SensitiveSecrets {
		if err := ValidateClaudeEnvKey(key); err != nil {
			return err
		}
	}
	return nil
}

// ClaudeProviderPaths contains effective paths used only at runtime. Provider keeps the
// original user-entered values so config and API responses remain understandable.
type ClaudeProviderPaths struct {
	BinaryPath string
	ConfigDir  string
	HomePath   string
}

// ResolveClaudeProviderPaths expands home-relative paths and makes all configured
// Claude paths absolute before they cross into process environment or filesystem APIs.
func ResolveClaudeProviderPaths(provider Provider) ClaudeProviderPaths {
	return ClaudeProviderPaths{
		BinaryPath: resolveClaudeProviderPath(provider.BinaryPath),
		ConfigDir:  resolveClaudeProviderPath(provider.ClaudeConfigDir),
		HomePath:   resolveClaudeProviderPath(provider.HomePath),
	}
}

func resolveClaudeProviderPath(raw string) string {
	path := strings.TrimSpace(raw)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~"+string(os.PathSeparator)) {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			if path == "~" {
				path = home
			} else {
				path = filepath.Join(home, strings.TrimPrefix(path, "~"+string(os.PathSeparator)))
			}
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(path)
}

// BuildClaudeCLIEnvironment merges os.Environ with provider overrides and secrets.
// On darwin, home_path never sets HOME; on other platforms it may.
// CLAUDE_CONFIG_DIR always comes from the dedicated config dir field.
func BuildClaudeCLIEnvironment(provider Provider, goos string) []string {
	merged := make(map[string]string)
	order := make([]string, 0)

	add := func(key, value string) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		if _, exists := merged[key]; !exists {
			order = append(order, key)
		}
		merged[key] = value
	}

	for _, item := range os.Environ() {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		add(parts[0], parts[1])
	}
	for key, value := range provider.EnvOverrides {
		add(key, value)
	}
	for key, value := range provider.SensitiveSecrets {
		add(key, value)
	}
	paths := ResolveClaudeProviderPaths(provider)
	if paths.ConfigDir != "" {
		add("CLAUDE_CONFIG_DIR", paths.ConfigDir)
	}
	if paths.HomePath != "" && strings.ToLower(strings.TrimSpace(goos)) != "darwin" {
		add("HOME", paths.HomePath)
	}

	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+merged[key])
	}
	return out
}

// SensitiveSecretKeysSorted returns sorted secret keys without values.
func SensitiveSecretKeysSorted(provider Provider) []string {
	if len(provider.SensitiveSecrets) == 0 {
		return nil
	}
	keys := make([]string, 0, len(provider.SensitiveSecrets))
	for key := range provider.SensitiveSecrets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ProviderSessionSettings holds agent-level Claude CLI session continuation flags.
type ProviderSessionSettings struct {
	UseProviderSession      bool
	ProviderSessionIdentity string
}

// ResolveProviderSessionSettings reports whether agent sessions should persist Claude CLI cursors.
// Config-level only (StatefulResponses + ref type). Apply AAGENT_CLAUDE_CLI_NO_SESSION_PERSISTENCE at entry points.
func ResolveProviderSessionSettings(ref string, provider Provider) ProviderSessionSettings {
	normalized := NormalizeProviderRef(ref)
	if !IsClaudeProviderRef(normalized) {
		return ProviderSessionSettings{}
	}
	enabled := true
	if provider.StatefulResponses != nil && !*provider.StatefulResponses {
		enabled = false
	}
	return ProviderSessionSettings{
		UseProviderSession:      enabled,
		ProviderSessionIdentity: ClaudeProviderSessionIdentity(normalized, provider),
	}
}

// ClaudeProviderSessionIdentity returns a stable identity for Claude CLI session continuation.
// Auth-affecting config is fingerprinted so a credential/config change cannot resume an old account session.
func ClaudeProviderSessionIdentity(ref string, provider Provider) string {
	normalized := NormalizeProviderRef(ref)
	if !IsClaudeProviderRef(normalized) {
		return ""
	}
	configDir := ResolveClaudeProviderPaths(provider).ConfigDir
	if configDir == "" {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			configDir = filepath.Join(home, ".claude")
		} else {
			configDir = ".claude"
		}
	}
	scope := normalized + "|" + configDir + "|" + ClaudeProviderConfigFingerprint(provider)
	sum := sha256.Sum256([]byte(scope))
	return fmt.Sprintf("claude:%s", hex.EncodeToString(sum[:8]))
}

// ClaudeProviderConfigFingerprint returns a stable fingerprint for cache invalidation.
func ClaudeProviderConfigFingerprint(provider Provider) string {
	paths := ResolveClaudeProviderPaths(provider)
	payload := struct {
		BinaryPath       string            `json:"binary_path"`
		ClaudeConfigDir  string            `json:"claude_config_dir"`
		HomePath         string            `json:"home_path"`
		EnvOverrides     map[string]string `json:"env_overrides"`
		SensitiveSecrets map[string]string `json:"sensitive_secrets"`
	}{
		BinaryPath:       paths.BinaryPath,
		ClaudeConfigDir:  paths.ConfigDir,
		HomePath:         paths.HomePath,
		EnvOverrides:     provider.EnvOverrides,
		SensitiveSecrets: hashSecretValues(provider.SensitiveSecrets),
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func hashSecretValues(secrets map[string]string) map[string]string {
	if len(secrets) == 0 {
		return nil
	}
	out := make(map[string]string, len(secrets))
	for key, value := range secrets {
		sum := sha256.Sum256([]byte(value))
		out[key] = hex.EncodeToString(sum[:8])
	}
	return out
}

// ListConfiguredClaudeInstanceRefs returns custom anthropic:<id> refs present in config.
func (c *Config) ListConfiguredClaudeInstanceRefs() []string {
	if c == nil || len(c.Providers) == 0 {
		return nil
	}
	refs := make([]string, 0)
	for ref := range c.Providers {
		if IsCustomClaudeInstanceRef(ref) {
			refs = append(refs, NormalizeProviderRef(ref))
		}
	}
	sort.Strings(refs)
	return refs
}

// GetClaudeProvider returns provider config for anthropic or anthropic:<id> refs.
func (c *Config) GetClaudeProvider(ref string) (Provider, bool) {
	if c == nil {
		return Provider{}, false
	}
	normalized := NormalizeProviderRef(ref)
	if !IsClaudeProviderRef(normalized) {
		return Provider{}, false
	}
	provider, ok := c.Providers[normalized]
	return provider, ok
}
