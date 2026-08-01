package brute

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGitHooksRunUnitTests(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Git hooks require the repository's supported Bash/WSL environment")
	}

	root := repositoryRoot(t)
	fakeBin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "go-args")
	fakeGo := filepath.Join(fakeBin, "go")
	if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HOOK_TEST_LOG\"\n[ -z \"${HOOK_TEST_FAIL:-}\" ]\n"), 0o755); err != nil {
		t.Fatalf("write fake go: %v", err)
	}

	for _, hook := range []string{"pre-commit", "pre-push"} {
		t.Run(hook, func(t *testing.T) {
			hookPath := filepath.Join(root, ".githooks", hook)
			info, err := os.Stat(hookPath)
			if err != nil {
				t.Fatalf("stat hook: %v", err)
			}
			if info.Mode()&0o111 == 0 {
				t.Fatalf("hook %s is not executable", hookPath)
			}

			cmd := exec.Command(hookPath)
			cmd.Dir = root
			cmd.Env = append(os.Environ(),
				"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"HOOK_TEST_LOG="+logPath,
			)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("run hook: %v\n%s", err, output)
			}

			failCmd := exec.Command(hookPath)
			failCmd.Dir = root
			failCmd.Env = append(os.Environ(),
				"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"HOOK_TEST_LOG="+logPath,
				"HOOK_TEST_FAIL=1",
			)
			if err := failCmd.Run(); err == nil {
				t.Fatal("hook must block Git when unit tests fail")
			}
		})
	}

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read go invocation log: %v", err)
	}
	if want := strings.Repeat("test -race ./...\n", 4); string(got) != want {
		t.Fatalf("unexpected go invocations:\n%s\nwant:\n%s", got, want)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	return filepath.Dir(filename)
}
