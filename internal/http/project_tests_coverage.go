package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	projectTestRSpecCoverageMappingLimit  = 12
	projectTestRSpecCoverageMappingBudget = 2 * time.Minute
	projectTestGoCoverageMappingLimit     = 50
)

func buildProjectTestsCoverage(ctx context.Context, repoRoot string, repoPath string, discovery ProjectTestsDiscoveryResponse, req ProjectTestsCoverageRequest) ProjectTestsCoverageResponse {
	return buildProjectTestsCoverageWithObserver(ctx, repoRoot, repoPath, discovery, req, nil)
}

func buildProjectTestsCoverageWithObserver(ctx context.Context, repoRoot string, repoPath string, discovery ProjectTestsDiscoveryResponse, req ProjectTestsCoverageRequest, observer projectTestRunObserver) ProjectTestsCoverageResponse {
	frameworks := projectTestTargetFrameworks(discovery, req.Framework)
	response := ProjectTestsCoverageResponse{
		RootFolder: repoRoot,
		RepoPath:   repoPath,
		Reports:    []ProjectTestCoverageReport{},
	}
	if len(frameworks) == 0 {
		response.Notes = append(response.Notes, "No supported test framework was detected for this project.")
		return response
	}

	changedFiles := changedCodeFileSet(discovery)
	for _, framework := range frameworks {
		switch framework {
		case projectTestFrameworkGo:
			response.Reports = append(response.Reports, buildGoProjectTestCoverage(ctx, repoRoot, discovery, changedFiles, observer))
		case projectTestFrameworkJest:
			response.Reports = append(response.Reports, buildJavaScriptProjectTestCoverage(ctx, repoRoot, projectTestFrameworkJest, changedFiles, observer))
		case projectTestFrameworkVitest:
			response.Reports = append(response.Reports, buildJavaScriptProjectTestCoverage(ctx, repoRoot, projectTestFrameworkVitest, changedFiles, observer))
		case projectTestFrameworkRSpec:
			response.Reports = append(response.Reports, buildRSpecProjectTestCoverage(ctx, repoRoot, discovery, changedFiles, observer))
		}
	}
	return response
}

func buildRSpecProjectTestCoverage(ctx context.Context, repoRoot string, discovery ProjectTestsDiscoveryResponse, changedFiles map[string]bool, observer projectTestRunObserver) ProjectTestCoverageReport {
	report := ProjectTestCoverageReport{
		Framework: projectTestFrameworkRSpec,
		Supported: true,
		Mode:      "simplecov resultset",
		Files:     []ProjectTestCoverageFile{},
		Mappings:  []ProjectTestCoverageMapping{},
		Notes: []string{
			"RSpec file coverage is aggregate from the last SimpleCov run. Test line mapping is best-effort and prefers branch-added examples.",
		},
	}
	resultsetPath := findSimpleCovResultset(repoRoot)
	if resultsetPath == "" {
		report.Notes = append(report.Notes, "No SimpleCov resultset found. Run RSpec with SimpleCov enabled before loading coverage.")
		return report
	}
	files, err := parseSimpleCovResultset(resultsetPath, repoRoot, changedFiles)
	if err != nil {
		report.Notes = append(report.Notes, "Failed to parse SimpleCov resultset: "+err.Error())
		return report
	}
	report.Files = files
	report.TotalFiles = len(files)
	report.Generated = len(files) > 0
	report.Mappings = append(report.Mappings, buildRSpecProjectTestCoverageMappings(ctx, repoRoot, discovery, changedFiles, filepath.Dir(resultsetPath), &report, observer)...)
	return report
}

func buildRSpecProjectTestCoverageMappings(ctx context.Context, repoRoot string, discovery ProjectTestsDiscoveryResponse, changedFiles map[string]bool, coverageDir string, report *ProjectTestCoverageReport, observer projectTestRunObserver) []ProjectTestCoverageMapping {
	if len(changedFiles) == 0 {
		return nil
	}
	if projectTestCoverageContextStopped(ctx, report, "Skipped RSpec test-to-line coverage mapping") {
		return nil
	}
	branchTests := branchRSpecCoverageTests(discovery)
	if len(branchTests) == 0 {
		report.Notes = append(report.Notes, "No branch-scoped RSpec examples were found for test-to-line coverage mapping.")
		return nil
	}
	if len(branchTests) > projectTestRSpecCoverageMappingLimit {
		report.Notes = append(report.Notes, fmt.Sprintf("RSpec test-to-line coverage mapping was limited to the first %d branch-scoped examples.", projectTestRSpecCoverageMappingLimit))
		branchTests = branchTests[:projectTestRSpecCoverageMappingLimit]
	}

	restoreCoverageDir, err := moveProjectCoverageDirAside(coverageDir)
	if err != nil {
		report.Notes = append(report.Notes, "Failed to isolate SimpleCov coverage directory for per-test mapping: "+err.Error())
		return nil
	}
	defer func() {
		if restoreErr := restoreCoverageDir(); restoreErr != nil {
			report.Notes = append(report.Notes, "Failed to restore SimpleCov coverage directory after per-test mapping: "+restoreErr.Error())
		}
	}()

	mappingCtx, cancelMapping := context.WithTimeout(ctx, projectTestRSpecCoverageMappingBudget)
	defer cancelMapping()

	mappings := make([]ProjectTestCoverageMapping, 0, len(branchTests))
	for _, selection := range branchTests {
		if projectTestCoverageContextStopped(mappingCtx, report, "Stopped RSpec test-to-line coverage mapping before all examples completed") {
			break
		}
		_ = os.RemoveAll(coverageDir)
		specTarget := fmt.Sprintf("%s:%d", selection.File.Path, selection.Node.Line)
		execution := runProjectTestCommandWithObserver(mappingCtx, repoRoot, projectTestFrameworkRSpec, "bundle", []string{"exec", "rspec", "--format", "json", specTarget}, nil, observer)
		report.Commands = append(report.Commands, execution.Command)
		if projectTestCoverageContextStopped(mappingCtx, report, "Stopped RSpec test-to-line coverage mapping before all examples completed") {
			break
		}
		files, parseErr := parseSimpleCovResultset(filepath.Join(coverageDir, ".resultset.json"), repoRoot, changedFiles)
		if parseErr != nil {
			continue
		}
		coveredFiles := coverageRefsFromChangedFiles(files)
		if len(coveredFiles) == 0 {
			continue
		}
		mappings = append(mappings, ProjectTestCoverageMapping{
			TestID:       selection.Node.ID,
			TestName:     selection.Node.FullName,
			Framework:    projectTestFrameworkRSpec,
			Path:         selection.File.Path,
			CoveredFiles: coveredFiles,
		})
	}
	_ = os.RemoveAll(coverageDir)
	return mappings
}

func buildGoProjectTestCoverage(ctx context.Context, repoRoot string, discovery ProjectTestsDiscoveryResponse, changedFiles map[string]bool, observer projectTestRunObserver) ProjectTestCoverageReport {
	report := ProjectTestCoverageReport{
		Framework: projectTestFrameworkGo,
		Supported: true,
		Mode:      "go coverprofile",
		Files:     []ProjectTestCoverageFile{},
		Mappings:  []ProjectTestCoverageMapping{},
		Commands:  []ProjectTestRunCommand{},
		Notes:     []string{},
	}

	aggregate, err := os.CreateTemp("", "a2gent-go-cover-*.out")
	if err != nil {
		report.Notes = append(report.Notes, err.Error())
		return report
	}
	aggregatePath := aggregate.Name()
	_ = aggregate.Close()
	defer os.Remove(aggregatePath)

	execution := runProjectTestCommandWithObserver(ctx, repoRoot, projectTestFrameworkGo, "go", []string{"test", "-coverprofile", aggregatePath, "./..."}, nil, observer)
	report.Commands = append(report.Commands, execution.Command)
	if files, parseErr := parseGoCoverageProfile(repoRoot, aggregatePath, changedFiles); parseErr == nil {
		report.Files = files
		report.TotalFiles = len(files)
		report.Generated = len(files) > 0
	} else {
		report.Notes = append(report.Notes, "Failed to parse aggregate Go coverage: "+parseErr.Error())
	}

	branchTests := branchGoCoverageTests(discovery)
	if len(branchTests) == 0 {
		report.Notes = append(report.Notes, "No branch-scoped Go tests were found for per-test coverage mapping.")
		return report
	}
	if len(branchTests) > projectTestGoCoverageMappingLimit {
		report.Notes = append(report.Notes, fmt.Sprintf("Per-test Go coverage mapping was limited to the first %d branch-scoped tests.", projectTestGoCoverageMappingLimit))
		branchTests = branchTests[:projectTestGoCoverageMappingLimit]
	}
	for _, selection := range branchTests {
		if projectTestCoverageContextStopped(ctx, &report, "Stopped Go test-to-line coverage mapping before all tests completed") {
			break
		}
		coverageFile, err := os.CreateTemp("", "a2gent-go-cover-test-*.out")
		if err != nil {
			report.Notes = append(report.Notes, err.Error())
			continue
		}
		coveragePath := coverageFile.Name()
		_ = coverageFile.Close()
		args := []string{"test", "-run", goTestRunPattern(selection.Node), "-coverprofile", coveragePath, goPackageFromTestPath(selection.File.Path)}
		testExecution := runProjectTestCommandWithObserver(ctx, repoRoot, projectTestFrameworkGo, "go", args, nil, observer)
		report.Commands = append(report.Commands, testExecution.Command)
		if projectTestCoverageContextStopped(ctx, &report, "Stopped Go test-to-line coverage mapping before all tests completed") {
			_ = os.Remove(coveragePath)
			break
		}
		files, parseErr := parseGoCoverageProfile(repoRoot, coveragePath, changedFiles)
		_ = os.Remove(coveragePath)
		if parseErr != nil {
			continue
		}
		report.Mappings = append(report.Mappings, ProjectTestCoverageMapping{
			TestID:       selection.Node.ID,
			TestName:     selection.Node.FullName,
			Framework:    projectTestFrameworkGo,
			Path:         selection.File.Path,
			CoveredFiles: coverageRefsFromChangedFiles(files),
		})
	}
	return report
}

func projectTestCoverageContextStopped(ctx context.Context, report *ProjectTestCoverageReport, message string) bool {
	if err := ctx.Err(); err != nil {
		note := message + ": " + err.Error()
		for _, existing := range report.Notes {
			if existing == note {
				return true
			}
		}
		report.Notes = append(report.Notes, note)
		return true
	}
	return false
}

func buildJavaScriptProjectTestCoverage(ctx context.Context, repoRoot string, framework string, changedFiles map[string]bool, observer projectTestRunObserver) ProjectTestCoverageReport {
	note := "Jest coverage is aggregate. Jest does not expose a standard per-test-to-file mapping without custom instrumentation."
	mode := "istanbul aggregate"
	if framework == projectTestFrameworkVitest {
		note = "Vitest coverage is aggregate. Vitest does not expose a standard per-test-to-file mapping without custom instrumentation."
		mode = "v8/istanbul aggregate"
	}
	report := ProjectTestCoverageReport{
		Framework: framework,
		Supported: true,
		Mode:      mode,
		Files:     []ProjectTestCoverageFile{},
		Commands:  []ProjectTestRunCommand{},
		Notes:     []string{note},
	}
	coverageDir, err := os.MkdirTemp("", "a2gent-"+framework+"-coverage-*")
	if err != nil {
		report.Notes = append(report.Notes, err.Error())
		return report
	}
	defer os.RemoveAll(coverageDir)

	command, args := projectJavaScriptTestCommand(repoRoot, framework)
	switch framework {
	case projectTestFrameworkVitest:
		args = append(args, "run", "--coverage", "--coverage.reporter=json", "--coverage.reportsDirectory", coverageDir)
	default:
		args = append(args, "--runInBand", "--coverage", "--coverageReporters=json", "--coverageDirectory", coverageDir)
	}
	execution := runProjectTestCommandWithObserver(ctx, repoRoot, framework, command, args, nil, observer)
	report.Commands = append(report.Commands, execution.Command)

	files, parseErr := parseIstanbulCoverage(filepath.Join(coverageDir, "coverage-final.json"), repoRoot, changedFiles)
	if parseErr != nil {
		report.Notes = append(report.Notes, "Failed to parse "+projectTestFrameworkLabel(framework)+" coverage: "+parseErr.Error())
		return report
	}
	report.Files = files
	report.TotalFiles = len(files)
	report.Generated = len(files) > 0
	return report
}

func parseGoCoverageProfile(repoRoot string, profilePath string, changedFiles map[string]bool) ([]ProjectTestCoverageFile, error) {
	content, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, err
	}
	modulePath := ""
	if output, err := runProjectTestQuickCommand(repoRoot, "go", "list", "-m"); err == nil {
		modulePath = strings.TrimSpace(output)
	}
	byPath := map[string]*ProjectTestCoverageFile{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		colon := strings.Index(line, ":")
		space := strings.Index(line, " ")
		if colon <= 0 || space <= colon {
			continue
		}
		rawPath := line[:colon]
		fields := strings.Fields(line[space+1:])
		if len(fields) < 2 {
			continue
		}
		ranges := strings.Split(line[colon+1:space], ",")
		if len(ranges) != 2 {
			continue
		}
		startLine := parseProjectTestCoverageLine(ranges[0])
		endLine := parseProjectTestCoverageLine(ranges[1])
		if startLine <= 0 || endLine <= 0 {
			continue
		}
		count, _ := strconv.Atoi(fields[1])
		relPath := normalizeGoCoveragePath(repoRoot, modulePath, rawPath)
		file := byPath[relPath]
		if file == nil {
			file = &ProjectTestCoverageFile{Path: relPath, Changed: changedFiles[relPath]}
			byPath[relPath] = file
		}
		covered := count > 0
		file.Segments = append(file.Segments, ProjectTestCoverageSegment{
			StartLine: startLine,
			EndLine:   endLine,
			Covered:   covered,
		})
		lines := endLine - startLine + 1
		if lines < 1 {
			lines = 1
		}
		file.TotalLines += lines
		if covered {
			file.CoveredLines += lines
		}
	}
	return finishCoverageFiles(byPath), nil
}

func parseIstanbulCoverage(path string, repoRoot string, changedFiles map[string]bool) ([]ProjectTestCoverageFile, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	type loc struct {
		Line int `json:"line"`
	}
	type statementRange struct {
		Start loc `json:"start"`
		End   loc `json:"end"`
	}
	type istanbulFile struct {
		StatementMap map[string]statementRange `json:"statementMap"`
		Statements   map[string]int            `json:"s"`
	}
	parsed := map[string]istanbulFile{}
	if err := json.Unmarshal(content, &parsed); err != nil {
		return nil, err
	}
	byPath := map[string]*ProjectTestCoverageFile{}
	for rawPath, fileCoverage := range parsed {
		relPath := normalizeProjectTestResultPath(repoRoot, rawPath)
		file := &ProjectTestCoverageFile{Path: relPath, Changed: changedFiles[relPath]}
		for id, statement := range fileCoverage.StatementMap {
			count := fileCoverage.Statements[id]
			startLine := statement.Start.Line
			endLine := statement.End.Line
			if startLine <= 0 || endLine <= 0 {
				continue
			}
			covered := count > 0
			file.Segments = append(file.Segments, ProjectTestCoverageSegment{StartLine: startLine, EndLine: endLine, Covered: covered})
			lines := endLine - startLine + 1
			if lines < 1 {
				lines = 1
			}
			file.TotalLines += lines
			if covered {
				file.CoveredLines += lines
			}
		}
		byPath[relPath] = file
	}
	return finishCoverageFiles(byPath), nil
}

func findSimpleCovResultset(repoRoot string) string {
	for _, relPath := range []string{
		"coverage/.resultset.json",
		"spec/coverage/.resultset.json",
	} {
		path := filepath.Join(repoRoot, filepath.FromSlash(relPath))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func parseSimpleCovResultset(path string, repoRoot string, changedFiles map[string]bool) ([]ProjectTestCoverageFile, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	type simpleCovRun struct {
		Coverage map[string]json.RawMessage `json:"coverage"`
	}
	parsed := map[string]simpleCovRun{}
	if err := json.Unmarshal(content, &parsed); err != nil {
		return nil, err
	}
	byPath := map[string]map[int]bool{}
	for _, run := range parsed {
		for rawPath, rawCoverage := range run.Coverage {
			lines, parseErr := parseSimpleCovLines(rawCoverage)
			if parseErr != nil {
				continue
			}
			relPath := normalizeProjectTestResultPath(repoRoot, rawPath)
			if relPath == "" || strings.HasPrefix(relPath, "../") {
				continue
			}
			lineCoverage := byPath[relPath]
			if lineCoverage == nil {
				lineCoverage = map[int]bool{}
				byPath[relPath] = lineCoverage
			}
			for index, line := range lines {
				if line == nil {
					continue
				}
				lineNumber := index + 1
				if *line > 0 {
					lineCoverage[lineNumber] = true
				} else if _, exists := lineCoverage[lineNumber]; !exists {
					lineCoverage[lineNumber] = false
				}
			}
		}
	}
	files := map[string]*ProjectTestCoverageFile{}
	for path, lines := range byPath {
		lineNumbers := make([]int, 0, len(lines))
		for lineNumber := range lines {
			lineNumbers = append(lineNumbers, lineNumber)
		}
		sort.Ints(lineNumbers)
		file := &ProjectTestCoverageFile{Path: path, Changed: changedFiles[path]}
		for _, lineNumber := range lineNumbers {
			covered := lines[lineNumber]
			file.TotalLines++
			if covered {
				file.CoveredLines++
			}
			appendProjectTestCoverageLineSegment(file, lineNumber, covered)
		}
		files[path] = file
	}
	return finishCoverageFiles(files), nil
}

func parseSimpleCovLines(raw json.RawMessage) ([]*int, error) {
	var direct []*int
	if err := json.Unmarshal(raw, &direct); err == nil && direct != nil {
		return direct, nil
	}
	var wrapped struct {
		Lines []*int `json:"lines"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Lines, nil
}

func appendProjectTestCoverageLineSegment(file *ProjectTestCoverageFile, lineNumber int, covered bool) {
	if len(file.Segments) == 0 {
		file.Segments = append(file.Segments, ProjectTestCoverageSegment{StartLine: lineNumber, EndLine: lineNumber, Covered: covered})
		return
	}
	last := &file.Segments[len(file.Segments)-1]
	if last.Covered == covered && last.EndLine+1 == lineNumber {
		last.EndLine = lineNumber
		return
	}
	file.Segments = append(file.Segments, ProjectTestCoverageSegment{StartLine: lineNumber, EndLine: lineNumber, Covered: covered})
}

func finishCoverageFiles(byPath map[string]*ProjectTestCoverageFile) []ProjectTestCoverageFile {
	files := make([]ProjectTestCoverageFile, 0, len(byPath))
	for _, file := range byPath {
		if file.TotalLines > 0 {
			file.Percent = math.Round((float64(file.CoveredLines)/float64(file.TotalLines))*1000) / 10
		}
		files = append(files, *file)
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Changed != files[j].Changed {
			return files[i].Changed
		}
		return files[i].Path < files[j].Path
	})
	return files
}

func parseProjectTestCoverageLine(raw string) int {
	part := raw
	if dot := strings.Index(part, "."); dot >= 0 {
		part = part[:dot]
	}
	value, _ := strconv.Atoi(part)
	return value
}

func normalizeGoCoveragePath(repoRoot string, modulePath string, rawPath string) string {
	path := filepath.ToSlash(rawPath)
	if modulePath != "" && strings.HasPrefix(path, modulePath+"/") {
		return strings.TrimPrefix(path, modulePath+"/")
	}
	if filepath.IsAbs(path) {
		return normalizeProjectTestResultPath(repoRoot, path)
	}
	return path
}

func moveProjectCoverageDirAside(coverageDir string) (func() error, error) {
	if strings.TrimSpace(coverageDir) == "" || coverageDir == "." || coverageDir == string(filepath.Separator) {
		return nil, fmt.Errorf("invalid coverage directory %q", coverageDir)
	}
	// Keep the user's existing SimpleCov output outside the repo while per-test
	// mapping runs, so interrupted coverage refreshes cannot pollute Git changes.
	backupParent, err := os.MkdirTemp("", "a2gent-coverage-backup-*")
	if err != nil {
		return nil, err
	}
	backupDir := filepath.Join(backupParent, filepath.Base(coverageDir))
	existed := false
	if info, err := os.Stat(coverageDir); err == nil {
		if !info.IsDir() {
			_ = os.RemoveAll(backupParent)
			return nil, fmt.Errorf("%s is not a directory", coverageDir)
		}
		if err := os.Rename(coverageDir, backupDir); err != nil {
			_ = os.RemoveAll(backupParent)
			return nil, err
		}
		existed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.RemoveAll(backupParent)
		return nil, err
	}
	return func() error {
		removeErr := os.RemoveAll(coverageDir)
		defer os.RemoveAll(backupParent)
		if existed {
			if err := os.MkdirAll(filepath.Dir(coverageDir), 0o755); err != nil {
				if removeErr != nil {
					return fmt.Errorf("%v; %w", removeErr, err)
				}
				return err
			}
			if err := os.Rename(backupDir, coverageDir); err != nil {
				if removeErr != nil {
					return fmt.Errorf("%v; %w", removeErr, err)
				}
				return err
			}
		}
		return removeErr
	}, nil
}

func coverageRefsFromFiles(files []ProjectTestCoverageFile) []ProjectTestCoverageFileRef {
	refs := make([]ProjectTestCoverageFileRef, 0, len(files))
	for _, file := range files {
		if file.CoveredLines == 0 {
			continue
		}
		refs = append(refs, ProjectTestCoverageFileRef{
			Path:         file.Path,
			Changed:      file.Changed,
			CoveredLines: file.CoveredLines,
			TotalLines:   file.TotalLines,
			Percent:      file.Percent,
			Segments:     file.Segments,
		})
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].Changed != refs[j].Changed {
			return refs[i].Changed
		}
		if refs[i].Percent != refs[j].Percent {
			return refs[i].Percent > refs[j].Percent
		}
		return refs[i].Path < refs[j].Path
	})
	return refs
}

func coverageRefsFromChangedFiles(files []ProjectTestCoverageFile) []ProjectTestCoverageFileRef {
	changed := make([]ProjectTestCoverageFile, 0, len(files))
	for _, file := range files {
		if file.Changed {
			changed = append(changed, file)
		}
	}
	return coverageRefsFromFiles(changed)
}
