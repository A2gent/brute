package claudecli

import (
	"strings"
	"testing"
)

func envValue(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], key+"=") {
			return strings.TrimPrefix(env[i], key+"="), true
		}
	}
	return "", false
}

func TestOptionsCommandEnvRaisesMCPToolTimeout(t *testing.T) {
	t.Run("default is set for custom environment", func(t *testing.T) {
		env := optionsCommandEnv(Options{Environment: []string{"FOO=bar"}})
		if v, ok := envValue(env, "MCP_TOOL_TIMEOUT"); !ok || v != defaultMCPToolTimeoutMS {
			t.Fatalf("MCP_TOOL_TIMEOUT = %q, %v", v, ok)
		}
	})
	t.Run("explicit value is preserved", func(t *testing.T) {
		env := optionsCommandEnv(Options{Environment: []string{"MCP_TOOL_TIMEOUT=1234"}})
		if v, _ := envValue(env, "MCP_TOOL_TIMEOUT"); v != "1234" {
			t.Fatalf("MCP_TOOL_TIMEOUT = %q, want 1234", v)
		}
	})
}

// Claude CLI also aborts MCP calls that send no progress for 5 min, which cut
// off delegations whose child agent was still working.
func TestOptionsCommandEnvRaisesMCPToolIdleTimeout(t *testing.T) {
	env := optionsCommandEnv(Options{Environment: []string{"FOO=bar"}})
	if v, ok := envValue(env, "CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT"); !ok || v != defaultMCPToolTimeoutMS {
		t.Fatalf("CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT = %q, %v", v, ok)
	}
	env = optionsCommandEnv(Options{Environment: []string{"CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=0", "MCP_TOOL_TIMEOUT=1234"}})
	if v, _ := envValue(env, "CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT"); v != "0" {
		t.Fatalf("explicit idle timeout overridden: %q", v)
	}
	if v, _ := envValue(env, "MCP_TOOL_TIMEOUT"); v != "1234" {
		t.Fatalf("explicit tool timeout overridden: %q", v)
	}
}
