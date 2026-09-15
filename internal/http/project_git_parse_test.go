package http

import (
	"reflect"
	"testing"
)

func TestParseProjectGitCommitFileStatusesReadsRenameOldPath(t *testing.T) {
	statuses := parseProjectGitCommitFileStatuses("" +
		"R100\tapp/assets/javascripts/store/all.js\tapp/javascript/src/store/index.js\n" +
		"M\tsrc/app.ts\n")

	got := statuses["app/javascript/src/store/index.js"]
	want := gitCommitFileMeta{
		Status:  "R100",
		OldPath: "app/assets/javascripts/store/all.js",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rename meta = %#v, want %#v", got, want)
	}
	if statuses["src/app.ts"].Status != "M" {
		t.Fatalf("modified status = %#v, want M", statuses["src/app.ts"])
	}
	if _, exists := statuses["app/assets/javascripts/store/all.js"]; exists {
		t.Fatalf("rename should be keyed by destination path, got %#v", statuses)
	}
}

func TestExpandGitRenamePathHandlesCompactGitNotation(t *testing.T) {
	oldPath, newPath, renamed := expandGitRenamePath("app/{assets/javascripts/store/all.js => javascript/src/store/index.js}")
	if !renamed {
		t.Fatal("expected compact git rename notation to be detected")
	}
	if oldPath != "app/assets/javascripts/store/all.js" {
		t.Fatalf("old path = %q", oldPath)
	}
	if newPath != "app/javascript/src/store/index.js" {
		t.Fatalf("new path = %q", newPath)
	}

	oldPath, newPath, renamed = expandGitRenamePath("app/{assets/javascripts/store/all.js -> javascript/src/store/index.js}")
	if !renamed || oldPath != "app/assets/javascripts/store/all.js" || newPath != "app/javascript/src/store/index.js" {
		t.Fatalf("arrow compact rename = %q -> %q renamed=%v", oldPath, newPath, renamed)
	}

	oldPath, newPath, renamed = expandGitRenamePath("src/{foo => bar}/util.js")
	if !renamed || oldPath != "src/foo/util.js" || newPath != "src/bar/util.js" {
		t.Fatalf("suffix compact rename = %q -> %q renamed=%v", oldPath, newPath, renamed)
	}
}

func TestMergeProjectGitCommitFilesJoinsRenameStatusWithCompactNumstat(t *testing.T) {
	statuses := parseProjectGitCommitFileStatuses("R100\tapp/assets/javascripts/store/all.js\tapp/javascript/src/store/index.js\n")
	files := mergeProjectGitCommitFiles(statuses, "2\t1\tapp/{assets/javascripts/store/all.js => javascript/src/store/index.js}\n")

	if len(files) != 1 {
		t.Fatalf("files = %#v, want a single moved file", files)
	}
	got := files[0]
	if got.Path != "app/javascript/src/store/index.js" {
		t.Fatalf("path = %q", got.Path)
	}
	if got.OldPath != "app/assets/javascripts/store/all.js" {
		t.Fatalf("old_path = %q", got.OldPath)
	}
	if got.Status != "R100" {
		t.Fatalf("status = %q, want R100", got.Status)
	}
	if got.Additions != 2 || got.Deletions != 1 {
		t.Fatalf("stats = +%d -%d", got.Additions, got.Deletions)
	}
}

func TestMergeProjectGitCommitFilesInfersMoveFromNumstatWhenStatusMissing(t *testing.T) {
	files := mergeProjectGitCommitFiles(map[string]gitCommitFileMeta{}, "0\t0\tapp/{assets/javascripts/store/all.js -> javascript/src/store/index.js}\n")
	if len(files) != 1 {
		t.Fatalf("files = %#v, want a single inferred move", files)
	}
	got := files[0]
	if got.Path != "app/javascript/src/store/index.js" || got.OldPath != "app/assets/javascripts/store/all.js" {
		t.Fatalf("inferred move paths = %#v", got)
	}
	if got.Status != "R" {
		t.Fatalf("status = %q, want R", got.Status)
	}
}
