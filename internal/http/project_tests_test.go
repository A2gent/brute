package http

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildProjectTestsDiscoveryMarksBranchAddedGoTests(t *testing.T) {
	repoRoot := t.TempDir()
	initMindGitTestRepo(t, repoRoot)
	writeGitTestFile(t, repoRoot, "go.mod", "module example.com/project\n\ngo 1.24\n")
	runGitForMindTest(t, repoRoot, "add", "go.mod")
	runGitForMindTest(t, repoRoot, "-c", "commit.gpgsign=false", "commit", "-m", "add go module")
	runGitForMindTest(t, repoRoot, "checkout", "-b", "feature/tests")

	testPath := filepath.ToSlash(filepath.Join("internal", "calc_test.go"))
	if err := os.MkdirAll(filepath.Join(repoRoot, "internal"), 0o755); err != nil {
		t.Fatalf("failed to create test directory: %v", err)
	}
	writeGitTestFile(t, repoRoot, testPath, `package internal

import "testing"

func TestAddsNumbers(t *testing.T) {
	t.Run("positive values", func(t *testing.T) {})
}
`)
	runGitForMindTest(t, repoRoot, "add", testPath)
	runGitForMindTest(t, repoRoot, "-c", "commit.gpgsign=false", "commit", "-m", "add go test")

	discovery, err := buildProjectTestsDiscovery("project-1", "", repoRoot)
	if err != nil {
		t.Fatalf("buildProjectTestsDiscovery returned error: %v", err)
	}
	if !discovery.BranchChangesAvailable {
		t.Fatal("expected branch changes to be available")
	}
	if len(discovery.BranchTestFiles) != 1 {
		t.Fatalf("expected 1 branch test file, got %d", len(discovery.BranchTestFiles))
	}

	file := discovery.BranchTestFiles[0]
	if file.Framework != projectTestFrameworkGo {
		t.Fatalf("expected go framework, got %q", file.Framework)
	}
	if file.BranchScope != "added" {
		t.Fatalf("expected added branch scope, got %q", file.BranchScope)
	}
	if len(file.Tests) != 1 {
		t.Fatalf("expected 1 top-level test, got %d", len(file.Tests))
	}
	if !file.Tests[0].BranchAdded {
		t.Fatal("expected top-level test to be marked branch-added")
	}
	if len(file.Tests[0].Children) != 1 || !file.Tests[0].Children[0].BranchAdded {
		t.Fatal("expected subtest to be marked branch-added")
	}
}

func TestBuildProjectTestsDiscoveryIncludesUncommittedNewTestFiles(t *testing.T) {
	repoRoot := t.TempDir()
	initMindGitTestRepo(t, repoRoot)
	writeGitTestFile(t, repoRoot, "go.mod", "module example.com/project\n\ngo 1.24\n")
	runGitForMindTest(t, repoRoot, "add", "go.mod")
	runGitForMindTest(t, repoRoot, "-c", "commit.gpgsign=false", "commit", "-m", "add go module")
	runGitForMindTest(t, repoRoot, "checkout", "-b", "feature/uncommitted-tests")

	if err := os.MkdirAll(filepath.Join(repoRoot, "internal"), 0o755); err != nil {
		t.Fatalf("failed to create test directory: %v", err)
	}
	testPath := filepath.ToSlash(filepath.Join("internal", "uncommitted_test.go"))
	writeGitTestFile(t, repoRoot, testPath, `package internal

import "testing"

func TestUncommitted(t *testing.T) {}
`)

	discovery, err := buildProjectTestsDiscovery("project-1", "", repoRoot)
	if err != nil {
		t.Fatalf("buildProjectTestsDiscovery returned error: %v", err)
	}
	if !discovery.BranchChangesAvailable {
		t.Fatal("expected branch changes to include uncommitted files")
	}
	if len(discovery.BranchTestFiles) != 1 {
		t.Fatalf("expected 1 branch test file, got %d", len(discovery.BranchTestFiles))
	}
	file := discovery.BranchTestFiles[0]
	if file.Path != testPath {
		t.Fatalf("expected branch test file %q, got %q", testPath, file.Path)
	}
	if file.BranchScope != "added" {
		t.Fatalf("expected added branch scope, got %q", file.BranchScope)
	}
	if len(file.Tests) != 1 || !file.Tests[0].BranchAdded {
		t.Fatalf("expected uncommitted test to be marked branch-added: %#v", file.Tests)
	}
}

func TestBuildProjectTestsDiscoveryMarksUncommittedAddedTestLines(t *testing.T) {
	repoRoot := t.TempDir()
	initMindGitTestRepo(t, repoRoot)
	writeGitTestFile(t, repoRoot, "go.mod", "module example.com/project\n\ngo 1.24\n")
	if err := os.MkdirAll(filepath.Join(repoRoot, "internal"), 0o755); err != nil {
		t.Fatalf("failed to create test directory: %v", err)
	}
	testPath := filepath.ToSlash(filepath.Join("internal", "calc_test.go"))
	writeGitTestFile(t, repoRoot, testPath, `package internal

import "testing"

func TestExisting(t *testing.T) {}
`)
	runGitForMindTest(t, repoRoot, "add", "go.mod", testPath)
	runGitForMindTest(t, repoRoot, "-c", "commit.gpgsign=false", "commit", "-m", "add go tests")
	runGitForMindTest(t, repoRoot, "checkout", "-b", "feature/uncommitted-test-line")

	writeGitTestFile(t, repoRoot, testPath, `package internal

import "testing"

func TestExisting(t *testing.T) {}

func TestUncommittedAddition(t *testing.T) {}
`)

	discovery, err := buildProjectTestsDiscovery("project-1", "", repoRoot)
	if err != nil {
		t.Fatalf("buildProjectTestsDiscovery returned error: %v", err)
	}
	if len(discovery.BranchTestFiles) != 1 {
		t.Fatalf("expected 1 branch test file, got %d", len(discovery.BranchTestFiles))
	}
	file := discovery.BranchTestFiles[0]
	if file.BranchScope != "changed" {
		t.Fatalf("expected changed branch scope, got %q", file.BranchScope)
	}
	if len(file.Tests) != 2 {
		t.Fatalf("expected 2 tests, got %d", len(file.Tests))
	}
	if file.Tests[0].BranchAdded {
		t.Fatal("expected existing test not to be marked branch-added")
	}
	if !file.Tests[1].BranchAdded {
		t.Fatal("expected uncommitted added test line to be marked branch-added")
	}
}

func TestParseRSpecTestResultsSkipsLogPreambleAndCoverageTrailer(t *testing.T) {
	output := `2026-06-10 Sidekiq connecting with options {:size=>10}
{"version":"3.12.2","examples":[{"id":"./spec/models/widget_spec.rb[1:1]","description":"works","full_description":"Widget works","status":"passed","file_path":"./spec/models/widget_spec.rb","line_number":7,"run_time":0.012,"pending_message":null}],"summary":{"duration":0.012,"example_count":1,"failure_count":0,"pending_count":0}}
Coverage report generated for RSpec.`

	results := parseRSpecTestResults(t.TempDir(), output)
	if len(results) != 1 {
		t.Fatalf("expected 1 parsed result, got %d", len(results))
	}
	if results[0].Status != "passed" {
		t.Fatalf("expected passed status, got %q", results[0].Status)
	}
	if results[0].Path != "spec/models/widget_spec.rb" {
		t.Fatalf("expected normalized spec path, got %q", results[0].Path)
	}
	if results[0].DurationMs != 12 {
		t.Fatalf("expected 12ms duration, got %d", results[0].DurationMs)
	}
}

func TestParseSimpleCovResultsetAggregatesLineCoverage(t *testing.T) {
	repoRoot := t.TempDir()
	coverageDir := filepath.Join(repoRoot, "spec", "coverage")
	if err := os.MkdirAll(coverageDir, 0o755); err != nil {
		t.Fatalf("failed to create coverage directory: %v", err)
	}
	widgetPath := filepath.Join(repoRoot, "app", "models", "widget.rb")
	otherPath := filepath.Join(repoRoot, "app", "models", "other.rb")
	payload := map[string]any{
		"RSpec": map[string]any{
			"coverage": map[string]any{
				widgetPath: map[string]any{"lines": []any{1, nil, 0, 0}},
				otherPath:  map[string]any{"lines": []any{nil, 1}},
			},
		},
		"Unknown Test Framework": map[string]any{
			"coverage": map[string]any{
				widgetPath: map[string]any{"lines": []any{0, nil, 1, 0}},
			},
		},
	}
	content, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to encode SimpleCov payload: %v", err)
	}
	resultsetPath := filepath.Join(coverageDir, ".resultset.json")
	if err := os.WriteFile(resultsetPath, content, 0o644); err != nil {
		t.Fatalf("failed to write SimpleCov resultset: %v", err)
	}

	files, err := parseSimpleCovResultset(resultsetPath, repoRoot, map[string]bool{"app/models/widget.rb": true})
	if err != nil {
		t.Fatalf("parseSimpleCovResultset returned error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 coverage files, got %d", len(files))
	}
	widget := files[0]
	if widget.Path != "app/models/widget.rb" {
		t.Fatalf("expected changed file first, got %q", widget.Path)
	}
	if !widget.Changed {
		t.Fatal("expected widget file to be marked changed")
	}
	if widget.CoveredLines != 2 || widget.TotalLines != 3 {
		t.Fatalf("expected 2/3 covered lines, got %d/%d", widget.CoveredLines, widget.TotalLines)
	}
	if widget.Percent != 66.7 {
		t.Fatalf("expected 66.7 percent, got %.1f", widget.Percent)
	}
}

func TestCoverageRefsFromChangedFilesIncludesSegments(t *testing.T) {
	refs := coverageRefsFromChangedFiles([]ProjectTestCoverageFile{
		{
			Path:         "app/models/widget.rb",
			Changed:      true,
			CoveredLines: 2,
			TotalLines:   3,
			Percent:      66.7,
			Segments: []ProjectTestCoverageSegment{
				{StartLine: 1, EndLine: 1, Covered: true},
				{StartLine: 3, EndLine: 4, Covered: false},
			},
		},
		{
			Path:         "app/models/other.rb",
			Changed:      false,
			CoveredLines: 1,
			TotalLines:   1,
			Percent:      100,
		},
	})
	if len(refs) != 1 {
		t.Fatalf("expected 1 changed coverage ref, got %d", len(refs))
	}
	if refs[0].Path != "app/models/widget.rb" {
		t.Fatalf("expected widget ref, got %q", refs[0].Path)
	}
	if len(refs[0].Segments) != 2 {
		t.Fatalf("expected coverage segments to be included, got %d", len(refs[0].Segments))
	}
}

func TestMoveProjectCoverageDirAsideRestoresOriginalDirectory(t *testing.T) {
	repoRoot := t.TempDir()
	coverageDir := filepath.Join(repoRoot, "spec", "coverage")
	if err := os.MkdirAll(coverageDir, 0o755); err != nil {
		t.Fatalf("failed to create coverage directory: %v", err)
	}
	originalPath := filepath.Join(coverageDir, ".resultset.json")
	if err := os.WriteFile(originalPath, []byte(`{"original":true}`), 0o644); err != nil {
		t.Fatalf("failed to write original coverage file: %v", err)
	}

	restore, err := moveProjectCoverageDirAside(coverageDir)
	if err != nil {
		t.Fatalf("moveProjectCoverageDirAside returned error: %v", err)
	}
	if _, err := os.Stat(originalPath); !os.IsNotExist(err) {
		t.Fatalf("expected original coverage directory to be moved aside, stat err: %v", err)
	}
	backupMatches, err := filepath.Glob(filepath.Join(repoRoot, "spec", ".a2gent-coverage-backup-*"))
	if err != nil {
		t.Fatalf("failed to inspect repo-local coverage backups: %v", err)
	}
	if len(backupMatches) != 0 {
		t.Fatalf("expected coverage backup to stay out of repo, got %v", backupMatches)
	}
	if err := os.MkdirAll(coverageDir, 0o755); err != nil {
		t.Fatalf("failed to create temporary coverage directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(coverageDir, ".resultset.json"), []byte(`{"temporary":true}`), 0o644); err != nil {
		t.Fatalf("failed to write temporary coverage file: %v", err)
	}
	if err := restore(); err != nil {
		t.Fatalf("restore returned error: %v", err)
	}
	content, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("failed to read restored coverage file: %v", err)
	}
	if string(content) != `{"original":true}` {
		t.Fatalf("expected original coverage content, got %s", content)
	}
}
