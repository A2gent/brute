package http

import "testing"

func TestParseProjectGitReviewOverlayLineIndexReadsChangedLineNumbers(t *testing.T) {
	diff := `diff --git a/src/app.ts b/src/app.ts
@@ -10,3 +10,4 @@ func demo() {
 context
-removed call
+added call
+added guard
 unchanged
`

	index := parseProjectGitReviewOverlayLineIndex(diff)
	if !index.Deletions[11] {
		t.Fatalf("expected deletion line 11 in index: %#v", index.Deletions)
	}
	if !index.Additions[11] || !index.Additions[12] {
		t.Fatalf("expected addition lines 11 and 12 in index: %#v", index.Additions)
	}
	if index.Additions[10] || index.Deletions[10] {
		t.Fatalf("context line 10 must not be accepted as changed: additions=%#v deletions=%#v", index.Additions, index.Deletions)
	}
}

func TestSanitizeProjectGitReviewOverlayResponseFiltersInvalidModelOutput(t *testing.T) {
	allowedFiles := map[string]projectGitReviewOverlayLineIndex{
		"src/app.ts": {
			Additions: map[int]bool{21: true, 22: true},
			Deletions: map[int]bool{18: true},
		},
	}
	raw := "```json\n" + `{
  "annotations": [
    {"file_path":"src/app.ts","side":"additions","line_number":21,"end_line_number":22,"title":"Validation protects the save flow","body":"The request now fails early when required data is missing, so users get a predictable validation path instead of reaching persistence with incomplete state."},
    {"file_path":"src/app.ts","side":"context","line_number":21,"title":"Bad side","body":"Should be filtered."},
    {"file_path":"src/other.ts","side":"additions","line_number":21,"title":"Bad file","body":"Should be filtered."},
    {"file_path":"src/app.ts","side":"deletions","line_number":21,"title":"Bad line","body":"Should be filtered."},
    {"file_path":"src/app.ts","side":"additions","line_number":21,"title":"Duplicate","body":"Should be filtered because same file/side/line."}
  ]
}` + "\n```"

	annotations := sanitizeProjectGitReviewOverlayResponse(raw, allowedFiles)
	if len(annotations) != 1 {
		t.Fatalf("expected one valid annotation, got %d: %#v", len(annotations), annotations)
	}
	annotation := annotations[0]
	if annotation.FilePath != "src/app.ts" || annotation.Side != "additions" || annotation.LineNumber != 21 || annotation.EndLineNumber != 22 {
		t.Fatalf("unexpected annotation location: %#v", annotation)
	}
	if annotation.Title != "Validation protects the save flow" {
		t.Fatalf("unexpected title %q", annotation.Title)
	}
}

func TestSanitizeProjectGitReviewOverlayResponseRejectsObviousRestatements(t *testing.T) {
	allowedFiles := map[string]projectGitReviewOverlayLineIndex{
		"src/app.ts": {Additions: map[int]bool{21: true}},
	}
	raw := `{"annotations":[
		{"file_path":"src/app.ts","side":"additions","line_number":21,"title":"Important branch change","body":"This change adds a retryFrame method in this region (+11/-0)."},
		{"file_path":"src/app.ts","side":"additions","line_number":21,"title":"Retry keeps the frame recoverable","body":"The frame can now recover after a failed load, so users can retry the embedded content without refreshing the entire page."}
	]}`

	annotations := sanitizeProjectGitReviewOverlayResponse(raw, allowedFiles)
	if len(annotations) != 1 {
		t.Fatalf("expected only the useful WHAT/WHY annotation, got %d: %#v", len(annotations), annotations)
	}
	if annotations[0].Title != "Retry keeps the frame recoverable" {
		t.Fatalf("unexpected annotation kept: %#v", annotations[0])
	}
}

func TestFilterProjectGitReviewOverlayFilesKeepsOnlyTargetFile(t *testing.T) {
	files := []ProjectGitCommitFile{
		{Path: "src/app.ts", Status: "M", Additions: 4, Deletions: 2},
		{Path: "src/other.ts", Status: "M", Additions: 1, Deletions: 0},
	}

	filtered := filterProjectGitReviewOverlayFiles("/repo", files, "src/app.ts")
	if len(filtered) != 1 {
		t.Fatalf("expected one filtered file, got %d: %#v", len(filtered), filtered)
	}
	if filtered[0].Path != "src/app.ts" {
		t.Fatalf("unexpected filtered path: %#v", filtered[0])
	}

	missing := filterProjectGitReviewOverlayFiles("/repo", files, "src/missing.ts")
	if len(missing) != 0 {
		t.Fatalf("expected missing file to filter to none, got %#v", missing)
	}
}
