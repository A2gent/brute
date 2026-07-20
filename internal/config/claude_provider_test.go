package config

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestClaudeInstanceRefHelpers(t *testing.T) {
	t.Parallel()

	if !IsClaudeProviderRef("anthropic") {
		t.Fatal("base anthropic ref should be Claude provider")
	}
	if !IsClaudeProviderRef(" ANTHROPIC%3Awork ") {
		t.Fatal("encoded custom Claude ref should be recognized")
	}
	if IsCustomClaudeInstanceRef("anthropic") {
		t.Fatal("base anthropic is not a custom instance")
	}
	if !IsClaudeProviderRef("anthropic:work") {
		t.Fatal("anthropic:work should be Claude provider")
	}
	if IsClaudeProviderRef("anthropic:") {
		t.Fatal("anthropic: with empty id must not be Claude provider")
	}
	if IsCustomClaudeInstanceRef("anthropic:") {
		t.Fatal("anthropic: with empty id must not be a custom instance")
	}
	if got := ClaudeInstanceIDFromRef("anthropic%3Ateam-a"); got != "team-a" {
		t.Fatalf("ClaudeInstanceIDFromRef = %q", got)
	}
	if got := ClaudeInstanceRefFromID(" Team A "); got != "anthropic:team-a" {
		t.Fatalf("ClaudeInstanceRefFromID = %q", got)
	}
}

func TestGetProviderDefinitionRecognizesClaudeInstanceRefs(t *testing.T) {
	t.Parallel()

	def := GetProviderDefinitionForRef("anthropic:secondary")
	if def == nil {
		t.Fatal("expected definition for custom Claude instance ref")
	}
	if def.Type != ProviderAnthropic {
		t.Fatalf("type = %q, want anthropic", def.Type)
	}
	if GetProviderDefinitionForRef("openai") == nil || GetProviderDefinitionForRef("anthropic:foo") == nil {
		t.Fatal("expected known refs to resolve")
	}
	if GetProviderDefinitionForRef("not-a-provider") != nil {
		t.Fatal("unknown provider should not resolve")
	}
}

func TestValidateClaudeEnvKey(t *testing.T) {
	t.Parallel()

	if err := ValidateClaudeEnvKey("ANTHROPIC_API_KEY"); err != nil {
		t.Fatalf("ANTHROPIC_API_KEY should be allowed: %v", err)
	}
	for _, key := range []string{"HOME", "PATH", "CLAUDE_CONFIG_DIR", "bad key", ""} {
		if err := ValidateClaudeEnvKey(key); err == nil {
			t.Fatalf("expected error for %q", key)
		}
	}
}

func TestBuildClaudeCLIEnvironmentMergesWithoutDuplicates(t *testing.T) {
	t.Setenv("FOO", "from-os")
	t.Setenv("BAR", "os-bar")

	provider := Provider{
		EnvOverrides: map[string]string{
			"FOO": "override",
			"BAZ": "baz",
		},
		SensitiveSecrets: map[string]string{
			"ANTHROPIC_API_KEY": "secret-key",
		},
		ClaudeConfigDir: "/cfg/claude",
		HomePath:        "/home/custom",
	}

	env := BuildClaudeCLIEnvironment(provider, "linux")
	got := envMap(env)

	if got["FOO"] != "override" {
		t.Fatalf("FOO = %q, want override", got["FOO"])
	}
	if got["BAR"] != "os-bar" {
		t.Fatalf("BAR = %q", got["BAR"])
	}
	if got["BAZ"] != "baz" {
		t.Fatalf("BAZ = %q", got["BAZ"])
	}
	if got["ANTHROPIC_API_KEY"] != "secret-key" {
		t.Fatalf("secret not merged: %q", got["ANTHROPIC_API_KEY"])
	}
	if got["CLAUDE_CONFIG_DIR"] != "/cfg/claude" {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q", got["CLAUDE_CONFIG_DIR"])
	}
	if got["HOME"] != "/home/custom" {
		t.Fatalf("HOME = %q on linux", got["HOME"])
	}
}

func TestBuildClaudeCLIEnvironmentNeverSetsHOMEOnDarwin(t *testing.T) {
	t.Setenv("HOME", "/Users/real")

	provider := Provider{HomePath: "~/isolated", ClaudeConfigDir: "~/.config/claude"}
	env := BuildClaudeCLIEnvironment(provider, "darwin")
	got := envMap(env)

	if got["HOME"] != "/Users/real" {
		t.Fatalf("darwin HOME = %q, want preserved os value", got["HOME"])
	}
	if got["CLAUDE_CONFIG_DIR"] != "/Users/real/.config/claude" {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q", got["CLAUDE_CONFIG_DIR"])
	}
}

func TestResolveClaudeProviderPathsExpandsAndMakesPathsAbsolute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	paths := ResolveClaudeProviderPaths(Provider{
		BinaryPath:      "~/bin/claude",
		ClaudeConfigDir: ".claude-work",
		HomePath:        "~/claude-home",
	})

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if paths.BinaryPath != filepath.Join(home, "bin", "claude") {
		t.Fatalf("binary path = %q", paths.BinaryPath)
	}
	if paths.ConfigDir != filepath.Join(cwd, ".claude-work") {
		t.Fatalf("config dir = %q", paths.ConfigDir)
	}
	if paths.HomePath != filepath.Join(home, "claude-home") {
		t.Fatalf("home path = %q", paths.HomePath)
	}
}

func TestResolveClaudeProviderPathsExpandsBareHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	paths := ResolveClaudeProviderPaths(Provider{ClaudeConfigDir: "~"})
	if paths.ConfigDir != home {
		t.Fatalf("config dir = %q, want home %q", paths.ConfigDir, home)
	}
}
func TestValidateClaudeProviderEnvMapsRejectsDangerousKeys(t *testing.T) {
	t.Parallel()

	err := ValidateClaudeProviderEnvMaps(Provider{
		EnvOverrides: map[string]string{"PATH": "/tmp"},
	})
	if err == nil || !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("expected PATH rejection, got %v", err)
	}
}

func TestSensitiveSecretKeysSorted(t *testing.T) {
	t.Parallel()

	keys := SensitiveSecretKeysSorted(Provider{
		SensitiveSecrets: map[string]string{
			"ZEBRA": "1",
			"ALPHA": "2",
		},
	})
	want := []string{"ALPHA", "ZEBRA"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
}

func TestClaudeProviderConfigFingerprintChangesWithConfig(t *testing.T) {
	t.Parallel()

	a := ClaudeProviderConfigFingerprint(Provider{BinaryPath: "/bin/a"})
	b := ClaudeProviderConfigFingerprint(Provider{BinaryPath: "/bin/b"})
	if a == b {
		t.Fatal("fingerprint should differ for different config")
	}
	if a != ClaudeProviderConfigFingerprint(Provider{BinaryPath: "/bin/a"}) {
		t.Fatal("fingerprint should be stable")
	}
}

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, item := range env {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			out[parts[0]] = parts[1]
		}
	}
	return out
}

func TestClaudeProviderConfigFingerprintDoesNotLeakSecrets(t *testing.T) {
	t.Parallel()

	a := ClaudeProviderConfigFingerprint(Provider{
		SensitiveSecrets: map[string]string{"ANTHROPIC_API_KEY": "super-secret"},
	})
	b := ClaudeProviderConfigFingerprint(Provider{
		SensitiveSecrets: map[string]string{"ANTHROPIC_API_KEY": "different-secret"},
	})
	if strings.Contains(a, "super-secret") || strings.Contains(b, "different-secret") {
		t.Fatal("fingerprint leaked secret value")
	}
	if a == b {
		t.Fatal("different secrets should produce different fingerprints")
	}
}

func TestResolveProviderSessionSettings(t *testing.T) {
	t.Parallel()

	nonClaude := ResolveProviderSessionSettings("openai", Provider{})
	if nonClaude.UseProviderSession || nonClaude.ProviderSessionIdentity != "" {
		t.Fatalf("non-Claude settings = %+v, want disabled/empty", nonClaude)
	}

	base := ResolveProviderSessionSettings("anthropic", Provider{})
	if !base.UseProviderSession || base.ProviderSessionIdentity == "" {
		t.Fatalf("base anthropic = %+v, want enabled with effective config identity", base)
	}

	disabled := false
	withDisabled := ResolveProviderSessionSettings("anthropic", Provider{StatefulResponses: &disabled})
	if withDisabled.UseProviderSession {
		t.Fatal("StatefulResponses false should disable provider sessions")
	}
	if withDisabled.ProviderSessionIdentity == "" {
		t.Fatal("base anthropic should retain effective config identity")
	}

	custom := ResolveProviderSessionSettings("anthropic:work", Provider{})
	if !custom.UseProviderSession || custom.ProviderSessionIdentity == "" {
		t.Fatalf("custom Claude ref = %+v, want enabled with identity", custom)
	}

	customDisabled := ResolveProviderSessionSettings("anthropic:work", Provider{StatefulResponses: &disabled})
	if customDisabled.UseProviderSession {
		t.Fatal("custom ref with StatefulResponses false should disable sessions")
	}
	if customDisabled.ProviderSessionIdentity == "" {
		t.Fatal("custom ref should retain identity even when sessions disabled")
	}
}

func TestClaudeProviderSessionIdentity(t *testing.T) {
	t.Parallel()

	baseDefault := ClaudeProviderSessionIdentity("anthropic", Provider{})
	if baseDefault == "" {
		t.Fatal("base anthropic must bind continuation to its effective config dir")
	}
	withDir := ClaudeProviderSessionIdentity("anthropic", Provider{ClaudeConfigDir: "/cfg"})
	if withDir == "" || withDir == baseDefault {
		t.Fatalf("base with config dir = %q", withDir)
	}
	withSecret := ClaudeProviderSessionIdentity("anthropic", Provider{SensitiveSecrets: map[string]string{"ANTHROPIC_API_KEY": "one"}})
	withOtherSecret := ClaudeProviderSessionIdentity("anthropic", Provider{SensitiveSecrets: map[string]string{"ANTHROPIC_API_KEY": "two"}})
	if withSecret == withOtherSecret {
		t.Fatal("credential changes must rotate continuation identity")
	}
	customNoDir := ClaudeProviderSessionIdentity("anthropic:work", Provider{})
	if customNoDir == "" {
		t.Fatal("custom ref must have non-empty identity")
	}
	customOther := ClaudeProviderSessionIdentity("anthropic:personal", Provider{})
	if customOther == "" || customOther == customNoDir {
		t.Fatalf("custom identities should differ: work=%q personal=%q", customNoDir, customOther)
	}
}

func TestClaudeProviderSessionIdentityUsesResolvedConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	fromTilde := ClaudeProviderSessionIdentity("anthropic:work", Provider{ClaudeConfigDir: "~/.claude-work"})
	fromAbsolute := ClaudeProviderSessionIdentity("anthropic:work", Provider{ClaudeConfigDir: filepath.Join(home, ".claude-work")})
	if fromTilde != fromAbsolute {
		t.Fatalf("equivalent config dirs produced different identities: tilde=%q absolute=%q", fromTilde, fromAbsolute)
	}
}

func TestClaudeProviderConfigFingerprintUsesResolvedPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	fromTilde := ClaudeProviderConfigFingerprint(Provider{
		BinaryPath:      "~/bin/claude",
		ClaudeConfigDir: "~/.claude-work",
		HomePath:        "~/claude-home",
	})
	fromAbsolute := ClaudeProviderConfigFingerprint(Provider{
		BinaryPath:      filepath.Join(home, "bin", "claude"),
		ClaudeConfigDir: filepath.Join(home, ".claude-work"),
		HomePath:        filepath.Join(home, "claude-home"),
	})
	if fromTilde != fromAbsolute {
		t.Fatalf("equivalent paths produced different fingerprints: tilde=%q absolute=%q", fromTilde, fromAbsolute)
	}
}

func TestListConfiguredClaudeInstanceRefs(t *testing.T) {
	cfg := &Config{
		Providers: map[string]Provider{
			"anthropic":          {Name: "Default"},
			"anthropic:work":     {Name: "Work"},
			"anthropic:personal": {Name: "Personal"},
			"openai":             {Name: "OpenAI"},
		},
	}
	refs := cfg.ListConfiguredClaudeInstanceRefs()
	sort.Strings(refs)
	want := []string{"anthropic:personal", "anthropic:work"}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %v, want %v", refs, want)
	}
}
