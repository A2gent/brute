package http

import (
	"os/exec"
	"testing"
)

func TestRunLoggedStreamsOutputIntoArchiveProgress(t *testing.T) {
	done := startArchiveProgress("progress-test")
	defer done()

	out, err := runLogged("progress-test", "echo", exec.Command("bash", "-c", "echo one; echo two >&2; printf three"))
	if err != nil {
		t.Fatalf("runLogged: %v", err)
	}
	if out == "" {
		t.Fatal("expected captured output")
	}
	v, _ := archiveProgressStore.Load("progress-test")
	lines := v.(*archiveProgress).lines
	want := []string{"$ echo", "one", "two", "three"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("lines = %v, want %v", lines, want)
		}
	}
}

func TestShareArchiveProgressLogsChildIntoRoot(t *testing.T) {
	done := startArchiveProgress("root-share")
	defer done()
	shareArchiveProgress("root-share", "child-share")
	archiveProgressLog("child-share", "hello")
	v, _ := archiveProgressStore.Load("root-share")
	if p := v.(*archiveProgress); len(p.lines) != 1 || p.lines[0] != "hello" {
		t.Fatalf("lines = %v", p.lines)
	}
}
