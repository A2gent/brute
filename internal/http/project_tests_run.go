package http

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func executeProjectTests(ctx context.Context, repoRoot string, repoPath string, discovery ProjectTestsDiscoveryResponse, req ProjectTestsRunRequest) ProjectTestsRunResponse {
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		if strings.TrimSpace(req.TestID) != "" || strings.TrimSpace(req.Path) != "" {
			mode = "test"
		} else {
			mode = "project"
		}
	}

	response := ProjectTestsRunResponse{
		RootFolder: repoRoot,
		RepoPath:   repoPath,
		Mode:       mode,
		Commands:   []ProjectTestRunCommand{},
		Results:    []ProjectTestResult{},
	}

	frameworks := projectTestTargetFrameworks(discovery, req.Framework)
	if len(frameworks) == 0 {
		response.Results = append(response.Results, ProjectTestResult{
			ID:        "no-framework",
			Framework: strings.TrimSpace(req.Framework),
			Name:      "No test framework detected",
			FullName:  "No test framework detected",
			Status:    "failed",
			Error:     "No supported test framework was detected for this project.",
		})
		response.Summary = summarizeProjectTestResults(response.Results)
		return response
	}

	for _, framework := range frameworks {
		results, commands := executeProjectFrameworkTests(ctx, repoRoot, discovery, framework, mode, req)
		response.Results = append(response.Results, results...)
		response.Commands = append(response.Commands, commands...)
	}
	response.Summary = summarizeProjectTestResults(response.Results)
	for _, command := range response.Commands {
		response.Summary.DurationMs += command.DurationMs
	}
	return response
}

func executeProjectFrameworkTests(ctx context.Context, repoRoot string, discovery ProjectTestsDiscoveryResponse, framework string, mode string, req ProjectTestsRunRequest) ([]ProjectTestResult, []ProjectTestRunCommand) {
	var command string
	var args []string
	var outputPath string
	var planErr error

	switch framework {
	case projectTestFrameworkRSpec:
		command, args, planErr = buildRSpecTestCommand(repoRoot, discovery, mode, req)
	case projectTestFrameworkGo:
		command, args, planErr = buildGoTestCommand(discovery, mode, req)
	case projectTestFrameworkJest:
		command, args, outputPath, planErr = buildJavaScriptTestCommand(repoRoot, discovery, projectTestFrameworkJest, mode, req)
	case projectTestFrameworkVitest:
		command, args, outputPath, planErr = buildJavaScriptTestCommand(repoRoot, discovery, projectTestFrameworkVitest, mode, req)
	default:
		planErr = fmt.Errorf("unsupported framework %q", framework)
	}
	if planErr != nil {
		status := projectTestPlanErrorStatus(planErr)
		errorText := ""
		outputText := ""
		if status == "skipped" {
			outputText = planErr.Error()
		} else {
			errorText = planErr.Error()
		}
		result := ProjectTestResult{
			ID:        framework + ":plan-error",
			Framework: framework,
			Name:      projectTestFrameworkLabel(framework),
			FullName:  planErr.Error(),
			Status:    status,
			Output:    outputText,
			Error:     errorText,
		}
		return []ProjectTestResult{result}, []ProjectTestRunCommand{}
	}

	execution := runProjectTestCommand(ctx, repoRoot, framework, command, args, nil)
	execution.OutputPath = outputPath

	results := parseProjectTestCommandResults(repoRoot, discovery, framework, execution)
	return results, []ProjectTestRunCommand{execution.Command}
}

func buildRSpecTestCommand(repoRoot string, discovery ProjectTestsDiscoveryResponse, mode string, req ProjectTestsRunRequest) (string, []string, error) {
	command := "rspec"
	args := []string{"--format", "json"}
	if projectTestFileExists(repoRoot, "Gemfile") {
		if _, err := exec.LookPath("bundle"); err == nil {
			command = "bundle"
			args = []string{"exec", "rspec", "--format", "json"}
		}
	}

	selection, hasSelection := findProjectTestSelection(discovery, req)
	switch mode {
	case "project":
		return command, args, nil
	case "branch":
		paths := branchProjectTestPaths(discovery, projectTestFrameworkRSpec)
		if len(paths) == 0 {
			return "", nil, projectTestNoBranchFilesError(projectTestFrameworkRSpec)
		}
		args = append(args, paths...)
		return command, args, nil
	case "file":
		path := strings.TrimSpace(req.Path)
		if path == "" && hasSelection {
			path = selection.File.Path
		}
		if path == "" {
			return "", nil, errors.New("test file path is required")
		}
		args = append(args, filepath.ToSlash(path))
		return command, args, nil
	default:
		if !hasSelection {
			return "", nil, errors.New("test selection is required")
		}
		target := selection.File.Path
		if selection.Node.Line > 0 {
			target = fmt.Sprintf("%s:%d", selection.File.Path, selection.Node.Line)
		}
		args = append(args, target)
		return command, args, nil
	}
}

func projectTestPlanErrorStatus(err error) string {
	var planErr projectTestPlanError
	if errors.As(err, &planErr) && strings.TrimSpace(planErr.status) != "" {
		return planErr.status
	}
	return "failed"
}

func projectTestNoBranchFilesError(framework string) error {
	return projectTestPlanError{
		message: fmt.Sprintf("no %s test files changed on this branch", projectTestFrameworkLabel(framework)),
		status:  "skipped",
	}
}

func buildGoTestCommand(discovery ProjectTestsDiscoveryResponse, mode string, req ProjectTestsRunRequest) (string, []string, error) {
	args := []string{"test", "-json"}
	selection, hasSelection := findProjectTestSelection(discovery, req)
	switch mode {
	case "project":
		args = append(args, "./...")
	case "branch":
		packages := branchProjectTestPackages(discovery, projectTestFrameworkGo)
		if len(packages) == 0 {
			return "", nil, projectTestNoBranchFilesError(projectTestFrameworkGo)
		}
		args = append(args, packages...)
	case "file":
		path := strings.TrimSpace(req.Path)
		if path == "" && hasSelection {
			path = selection.File.Path
		}
		if path == "" {
			return "", nil, errors.New("test file path is required")
		}
		args = append(args, goPackageFromTestPath(path))
	default:
		if !hasSelection {
			return "", nil, errors.New("test selection is required")
		}
		args = append(args, "-run", goTestRunPattern(selection.Node), goPackageFromTestPath(selection.File.Path))
	}
	return "go", args, nil
}

func buildJavaScriptTestCommand(repoRoot string, discovery ProjectTestsDiscoveryResponse, framework string, mode string, req ProjectTestsRunRequest) (string, []string, string, error) {
	outputFile, err := os.CreateTemp("", "a2gent-"+framework+"-results-*.json")
	if err != nil {
		return "", nil, "", err
	}
	outputPath := outputFile.Name()
	_ = outputFile.Close()

	command, args := projectJavaScriptTestCommand(repoRoot, framework)
	switch framework {
	case projectTestFrameworkVitest:
		args = append(args, "run", "--reporter=json", "--outputFile", outputPath)
	default:
		args = append(args, "--runInBand", "--json", "--outputFile", outputPath, "--testLocationInResults")
	}

	selection, hasSelection := findProjectTestSelection(discovery, req)
	switch mode {
	case "project":
	case "branch":
		paths := branchProjectTestPaths(discovery, framework)
		if len(paths) == 0 {
			return "", nil, "", projectTestNoBranchFilesError(framework)
		}
		args = append(args, paths...)
	case "file":
		path := strings.TrimSpace(req.Path)
		if path == "" && hasSelection {
			path = selection.File.Path
		}
		if path == "" {
			return "", nil, "", errors.New("test file path is required")
		}
		args = append(args, filepath.ToSlash(path))
	default:
		if !hasSelection {
			return "", nil, "", errors.New("test selection is required")
		}
		args = append(args, selection.File.Path, "--testNamePattern", selection.Node.FullName)
	}
	return command, args, outputPath, nil
}

func runProjectTestCommand(ctx context.Context, repoRoot string, framework string, command string, args []string, env []string) projectTestCommandExecution {
	start := time.Now()
	runtimeCommand, runtimeArgs := projectTestRuntimeCommand(repoRoot, framework, command, args)
	cmd := exec.CommandContext(ctx, runtimeCommand, runtimeArgs...)
	cmd.Dir = repoRoot
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	output, err := cmd.CombinedOutput()
	duration := time.Since(start).Milliseconds()
	trimmedOutput := truncateProjectTestOutput(strings.TrimRight(string(output), "\r\n"), 128*1024)
	exitCode := 0
	errorText := ""
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		if ctx.Err() != nil {
			errorText = ctx.Err().Error()
		} else {
			errorText = err.Error()
		}
	}
	return projectTestCommandExecution{
		Command: ProjectTestRunCommand{
			Framework:  framework,
			Command:    runtimeCommand,
			Args:       runtimeArgs,
			Display:    strings.Join(append([]string{runtimeCommand}, runtimeArgs...), " "),
			ExitCode:   exitCode,
			DurationMs: duration,
			Output:     trimmedOutput,
			Error:      errorText,
		},
	}
}

func parseProjectTestCommandResults(repoRoot string, discovery ProjectTestsDiscoveryResponse, framework string, execution projectTestCommandExecution) []ProjectTestResult {
	switch framework {
	case projectTestFrameworkRSpec:
		if results := parseRSpecTestResults(repoRoot, execution.Command.Output); len(results) > 0 {
			return results
		}
	case projectTestFrameworkGo:
		if results := parseGoTestResults(discovery, execution.Command.Output); len(results) > 0 {
			return results
		}
	case projectTestFrameworkJest:
		if results := parseJavaScriptTestResults(repoRoot, execution.OutputPath, projectTestFrameworkJest); len(results) > 0 {
			_ = os.Remove(execution.OutputPath)
			return results
		}
		_ = os.Remove(execution.OutputPath)
	case projectTestFrameworkVitest:
		if results := parseJavaScriptTestResults(repoRoot, execution.OutputPath, projectTestFrameworkVitest); len(results) > 0 {
			_ = os.Remove(execution.OutputPath)
			return results
		}
		_ = os.Remove(execution.OutputPath)
	}

	status := "passed"
	if execution.Command.ExitCode != 0 {
		status = "failed"
	}
	return []ProjectTestResult{{
		ID:         framework + ":command",
		Framework:  framework,
		Name:       execution.Command.Display,
		FullName:   execution.Command.Display,
		Status:     status,
		DurationMs: execution.Command.DurationMs,
		Output:     execution.Command.Output,
		Error:      execution.Command.Error,
	}}
}

func parseRSpecTestResults(repoRoot string, output string) []ProjectTestResult {
	type rspecException struct {
		ClassName string   `json:"class"`
		Message   string   `json:"message"`
		Backtrace []string `json:"backtrace"`
	}
	type rspecExample struct {
		ID              string          `json:"id"`
		Description     string          `json:"description"`
		FullDescription string          `json:"full_description"`
		Status          string          `json:"status"`
		FilePath        string          `json:"file_path"`
		LineNumber      int             `json:"line_number"`
		RunTime         float64         `json:"run_time"`
		Exception       *rspecException `json:"exception"`
	}
	type rspecJSON struct {
		Examples []rspecExample `json:"examples"`
	}
	start := strings.Index(output, `{"version"`)
	if start < 0 {
		return nil
	}
	var parsed rspecJSON
	decoder := json.NewDecoder(strings.NewReader(output[start:]))
	if err := decoder.Decode(&parsed); err != nil {
		return nil
	}
	results := make([]ProjectTestResult, 0, len(parsed.Examples))
	for _, example := range parsed.Examples {
		path := normalizeProjectTestResultPath(repoRoot, example.FilePath)
		status := normalizeProjectTestStatus(example.Status)
		errorText := ""
		if example.Exception != nil {
			errorText = strings.TrimSpace(example.Exception.Message)
			if len(example.Exception.Backtrace) > 0 {
				errorText = strings.TrimSpace(errorText + "\n" + strings.Join(example.Exception.Backtrace, "\n"))
			}
		}
		results = append(results, ProjectTestResult{
			ID:         nonEmptyProjectTestID(example.ID, projectTestNodeID(projectTestFrameworkRSpec, path, example.LineNumber)),
			Framework:  projectTestFrameworkRSpec,
			Name:       nonEmptyProjectTestName(example.Description, example.FullDescription),
			FullName:   nonEmptyProjectTestName(example.FullDescription, example.Description),
			Path:       path,
			Line:       example.LineNumber,
			Status:     status,
			DurationMs: int64(math.Round(example.RunTime * 1000)),
			Error:      errorText,
		})
	}
	return results
}

func parseGoTestResults(discovery ProjectTestsDiscoveryResponse, output string) []ProjectTestResult {
	type goJSONEvent struct {
		Action  string  `json:"Action"`
		Package string  `json:"Package"`
		Test    string  `json:"Test"`
		Elapsed float64 `json:"Elapsed"`
		Output  string  `json:"Output"`
	}
	type goResultState struct {
		event  goJSONEvent
		output strings.Builder
	}
	states := map[string]*goResultState{}
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var event goJSONEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Test == "" {
			continue
		}
		key := event.Package + "\x00" + event.Test
		state := states[key]
		if state == nil {
			state = &goResultState{}
			states[key] = state
		}
		if event.Output != "" {
			state.output.WriteString(event.Output)
		}
		if event.Action == "pass" || event.Action == "fail" || event.Action == "skip" {
			state.event = event
		}
	}

	results := make([]ProjectTestResult, 0, len(states))
	for _, state := range states {
		if state.event.Action == "" {
			continue
		}
		path, line := findGoTestPath(discovery, state.event.Test)
		results = append(results, ProjectTestResult{
			ID:         projectTestFrameworkGo + ":" + state.event.Package + ":" + state.event.Test,
			Framework:  projectTestFrameworkGo,
			Name:       state.event.Test,
			FullName:   state.event.Test,
			Path:       path,
			Line:       line,
			Package:    state.event.Package,
			Status:     normalizeProjectTestStatus(state.event.Action),
			DurationMs: int64(math.Round(state.event.Elapsed * 1000)),
			Output:     strings.TrimSpace(state.output.String()),
		})
	}
	sortProjectTestResults(results)
	return results
}

func parseJavaScriptTestResults(repoRoot string, outputPath string, framework string) []ProjectTestResult {
	if outputPath == "" {
		return nil
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		return nil
	}
	type jestLocation struct {
		Line   int `json:"line"`
		Column int `json:"column"`
	}
	type jestAssertion struct {
		Title           string        `json:"title"`
		FullName        string        `json:"fullName"`
		Status          string        `json:"status"`
		Duration        float64       `json:"duration"`
		FailureMessages []string      `json:"failureMessages"`
		Location        *jestLocation `json:"location"`
	}
	type jestFileResult struct {
		Name             string          `json:"name"`
		Status           string          `json:"status"`
		AssertionResults []jestAssertion `json:"assertionResults"`
	}
	type jestJSON struct {
		TestResults []jestFileResult `json:"testResults"`
	}
	var parsed jestJSON
	if err := json.Unmarshal(content, &parsed); err != nil {
		return nil
	}
	results := make([]ProjectTestResult, 0)
	for _, fileResult := range parsed.TestResults {
		path := normalizeProjectTestResultPath(repoRoot, fileResult.Name)
		for _, assertion := range fileResult.AssertionResults {
			line := 0
			if assertion.Location != nil {
				line = assertion.Location.Line
			}
			results = append(results, ProjectTestResult{
				ID:         projectTestNodeID(framework, path, line),
				Framework:  framework,
				Name:       nonEmptyProjectTestName(assertion.Title, assertion.FullName),
				FullName:   nonEmptyProjectTestName(assertion.FullName, assertion.Title),
				Path:       path,
				Line:       line,
				Status:     normalizeProjectTestStatus(assertion.Status),
				DurationMs: int64(math.Round(assertion.Duration)),
				Error:      strings.Join(assertion.FailureMessages, "\n"),
			})
		}
	}
	sortProjectTestResults(results)
	return results
}
