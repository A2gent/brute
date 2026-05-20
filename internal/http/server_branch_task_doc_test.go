package http

import "testing"

func TestBranchTaskDocRelativePathUsesLastBranchSegment(t *testing.T) {
	t.Parallel()

	got, err := branchTaskDocRelativePath("kurapov/AR-1324-some-task")
	if err != nil {
		t.Fatalf("expected branch documentation path, got error: %v", err)
	}
	if got != "AR-1324-some-task.md" {
		t.Fatalf("expected final branch segment as markdown filename, got %q", got)
	}
}

func TestBranchTaskDocRelativePathRejectsUnsafeFinalSegment(t *testing.T) {
	t.Parallel()

	for _, branch := range []string{"", "/", "feature/..", "feature/."} {
		if got, err := branchTaskDocRelativePath(branch); err == nil {
			t.Fatalf("expected error for branch %q, got path %q", branch, got)
		}
	}
}
