package claudecli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls []fakeCall
}

type fakeCall struct {
	binary string
	args   []string
	env    []string
}

func (f *fakeRunner) Run(ctx context.Context, binary string, args []string, env []string) (string, string, error) {
	f.calls = append(f.calls, fakeCall{binary: binary, args: append([]string(nil), args...), env: append([]string(nil), env...)})
	switch {
	case len(args) == 1 && args[0] == "--version":
		return "claude 1.2.3", "", nil
	case len(args) == 3 && args[0] == "auth" && args[1] == "status" && args[2] == "--json":
		return `{"loggedIn":true,"email":"alice@example.com","subscriptionType":"pro","authMethod":"oauth","apiProvider":"anthropic"}`, "", nil
	case len(args) == 1 && args[0] == "--help":
		return "models:\n  opus\ncommands:\n  auth", "", nil
	default:
		return "", "", nil
	}
}

func TestProbeHealthUsesExactArgsAndEnv(t *testing.T) {
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "claude")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	runner := &fakeRunner{}
	env := []string{"FOO=bar", "CLAUDE_CONFIG_DIR=/cfg"}
	report := ProbeHealth(context.Background(), Options{
		Executable:  binary,
		Environment: env,
	}, runner)

	if report.Status != HealthHealthy {
		t.Fatalf("status = %q, want healthy (%+v)", report.Status, report)
	}
	if report.Version != "claude 1.2.3" {
		t.Fatalf("version = %q", report.Version)
	}
	if report.Subscription != "pro" {
		t.Fatalf("subscription = %q", report.Subscription)
	}
	if report.TokenSource != "oauth" {
		t.Fatalf("token_source = %q", report.TokenSource)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(runner.calls))
	}
	wantArgs := [][]string{{"--version"}, {"auth", "status", "--json"}, {"--help"}}
	for i, call := range runner.calls {
		if call.binary != binary {
			t.Fatalf("call[%d] binary = %q", i, call.binary)
		}
		if !reflect.DeepEqual(call.args, wantArgs[i]) {
			t.Fatalf("call[%d] args = %v, want %v", i, call.args, wantArgs[i])
		}
		if !reflect.DeepEqual(call.env, env) {
			t.Fatalf("call[%d] env = %v, want %v", i, call.env, env)
		}
	}
}

func TestProbeHealthUnavailableWhenVersionFails(t *testing.T) {
	runner := &versionFailRunner{}
	report := ProbeHealth(context.Background(), Options{Executable: "/bin/claude"}, runner)
	if report.Status != HealthUnavailable {
		t.Fatalf("status = %q", report.Status)
	}
	if strings.Contains(report.Error, "/home/user") {
		t.Fatalf("error leaked path: %q", report.Error)
	}
}

type versionFailRunner struct{}

func (versionFailRunner) Run(ctx context.Context, binary string, args []string, env []string) (string, string, error) {
	if len(args) == 1 && args[0] == "--version" {
		return "", "secret /home/user path", context.DeadlineExceeded
	}
	return "", "", nil
}

func TestContinuationIdentityIsDeterministic(t *testing.T) {
	a := ContinuationIdentity("/cfg/a")
	b := ContinuationIdentity("/cfg/a")
	c := ContinuationIdentity("/cfg/b")
	if a == "" || a != b || a == c {
		t.Fatalf("identities: a=%q b=%q c=%q", a, b, c)
	}
	if !strings.HasPrefix(a, "claude:") {
		t.Fatalf("identity = %q", a)
	}
}

func TestNormalizeExecutablePrefersProviderBinary(t *testing.T) {
	t.Setenv("AAGENT_CLAUDE_CLI_PATH", "/env/claude")
	if got := normalizeExecutable("/provider/claude"); got != "/provider/claude" {
		t.Fatalf("provider binary = %q", got)
	}
	if got := normalizeExecutable(""); got != "/env/claude" {
		t.Fatalf("env fallback = %q", got)
	}
}
