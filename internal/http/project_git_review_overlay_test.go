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
    {"file_path":"src/app.ts","side":"additions","line_number":21,"end_line_number":22,"title":"New validation path","body":"Explains why the new guard exists and how it changes the request flow."},
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
	if annotation.Title != "New validation path" {
		t.Fatalf("unexpected title %q", annotation.Title)
	}
}
