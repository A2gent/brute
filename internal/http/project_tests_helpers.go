package http

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func projectTestTargetFrameworks(discovery ProjectTestsDiscoveryResponse, requested string) []string {
	normalized := strings.TrimSpace(strings.ToLower(requested))
	if normalized != "" && normalized != "all" {
		return []string{normalized}
	}
	frameworks := make([]string, 0)
	for _, framework := range discovery.Frameworks {
		if framework.Available && framework.TestCount > 0 {
			frameworks = append(frameworks, framework.ID)
		}
	}
	return frameworks
}

func findProjectTestSelection(discovery ProjectTestsDiscoveryResponse, req ProjectTestsRunRequest) (projectTestSelection, bool) {
	testID := strings.TrimSpace(req.TestID)
	path := filepath.ToSlash(strings.TrimSpace(req.Path))
	for _, file := range discovery.TestFiles {
		if path != "" && file.Path == path && testID == "" {
			return projectTestSelection{File: file}, true
		}
		if testID == "" {
			continue
		}
		if node, ok := findProjectTestNode(file.Tests, testID); ok {
			return projectTestSelection{File: file, Node: node}, true
		}
	}
	if path != "" {
		for _, file := range discovery.TestFiles {
			if file.Path == path {
				return projectTestSelection{File: file}, true
			}
		}
	}
	return projectTestSelection{}, false
}

func findProjectTestNode(nodes []ProjectTestNode, id string) (ProjectTestNode, bool) {
	for _, node := range nodes {
		if node.ID == id {
			return node, true
		}
		if child, ok := findProjectTestNode(node.Children, id); ok {
			return child, true
		}
	}
	return ProjectTestNode{}, false
}

func branchProjectTestPaths(discovery ProjectTestsDiscoveryResponse, framework string) []string {
	paths := make([]string, 0)
	for _, file := range discovery.BranchTestFiles {
		if file.Framework == framework && file.BranchStatus != "D" {
			paths = append(paths, file.Path)
		}
	}
	sort.Strings(paths)
	return paths
}

func branchProjectTestPackages(discovery ProjectTestsDiscoveryResponse, framework string) []string {
	seen := map[string]bool{}
	packages := make([]string, 0)
	for _, file := range discovery.BranchTestFiles {
		if file.Framework != framework || file.BranchStatus == "D" {
			continue
		}
		pkg := goPackageFromTestPath(file.Path)
		if !seen[pkg] {
			seen[pkg] = true
			packages = append(packages, pkg)
		}
	}
	sort.Strings(packages)
	return packages
}

func branchGoCoverageTests(discovery ProjectTestsDiscoveryResponse) []projectTestSelection {
	selections := make([]projectTestSelection, 0)
	for _, file := range discovery.BranchTestFiles {
		if file.Framework != projectTestFrameworkGo || file.BranchStatus == "D" {
			continue
		}
		for _, node := range flattenProjectTestNodes(file.Tests) {
			if strings.HasPrefix(node.Name, "Test") {
				selections = append(selections, projectTestSelection{File: file, Node: node})
			}
		}
	}
	sort.SliceStable(selections, func(i, j int) bool {
		if selections[i].File.Path != selections[j].File.Path {
			return selections[i].File.Path < selections[j].File.Path
		}
		return selections[i].Node.Line < selections[j].Node.Line
	})
	return selections
}

func branchRSpecCoverageTests(discovery ProjectTestsDiscoveryResponse) []projectTestSelection {
	selections := make([]projectTestSelection, 0)
	for _, file := range discovery.BranchTestFiles {
		if file.Framework != projectTestFrameworkRSpec || file.BranchStatus == "D" {
			continue
		}
		for _, node := range flattenProjectTestNodes(file.Tests) {
			if node.Type == "test" && node.Line > 0 {
				selections = append(selections, projectTestSelection{File: file, Node: node})
			}
		}
	}
	sort.SliceStable(selections, func(i, j int) bool {
		if selections[i].File.Path != selections[j].File.Path {
			return selections[i].File.Path < selections[j].File.Path
		}
		return selections[i].Node.Line < selections[j].Node.Line
	})
	return selections
}

func flattenProjectTestNodes(nodes []ProjectTestNode) []ProjectTestNode {
	flat := make([]ProjectTestNode, 0)
	for _, node := range nodes {
		flat = append(flat, node)
		flat = append(flat, flattenProjectTestNodes(node.Children)...)
	}
	return flat
}

func countProjectTestNodes(nodes []ProjectTestNode) int {
	count := 0
	for _, node := range nodes {
		if node.Type == "test" {
			count++
		}
		count += countProjectTestNodes(node.Children)
	}
	return count
}

func changedCodeFileSet(discovery ProjectTestsDiscoveryResponse) map[string]bool {
	changed := map[string]bool{}
	for _, file := range discovery.BranchTestFiles {
		_ = file
	}
	if discovery.BranchChangesAvailable && projectHasGitMetadata(discovery.RootFolder) {
		target := projectGitBranchChangesTarget(discovery.RootFolder)
		files, err := loadProjectGitTestingScopeChangedFiles(discovery.RootFolder, target)
		if err == nil {
			for _, file := range files {
				if classifyProjectTestFile(file.Path) == "" {
					changed[file.Path] = true
				}
			}
		}
	}
	return changed
}

func summarizeProjectTestResults(results []ProjectTestResult) ProjectTestRunSummary {
	summary := ProjectTestRunSummary{Total: len(results)}
	for _, result := range results {
		switch result.Status {
		case "passed":
			summary.Passed++
		case "skipped", "pending":
			summary.Skipped++
		default:
			summary.Failed++
		}
	}
	slowest := append([]ProjectTestResult{}, results...)
	sort.SliceStable(slowest, func(i, j int) bool {
		return slowest[i].DurationMs > slowest[j].DurationMs
	})
	if len(slowest) > 10 {
		slowest = slowest[:10]
	}
	summary.Slowest = slowest
	return summary
}

func sortProjectTestResults(results []ProjectTestResult) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Path != results[j].Path {
			return results[i].Path < results[j].Path
		}
		if results[i].Line != results[j].Line {
			return results[i].Line < results[j].Line
		}
		return results[i].FullName < results[j].FullName
	})
}

func projectJavaScriptTestCommand(repoRoot string, framework string) (string, []string) {
	binary := "jest"
	if framework == projectTestFrameworkVitest {
		binary = "vitest"
	}
	local := filepath.Join(repoRoot, "node_modules", ".bin", binary)
	if info, err := os.Stat(local); err == nil && !info.IsDir() {
		return local, []string{}
	}
	return "npx", []string{"--no-install", binary}
}

func projectTestRuntimeCommand(repoRoot string, framework string, command string, args []string) (string, []string) {
	if !projectTestUsesManagedRuntime(repoRoot, framework, command) {
		return command, args
	}
	if projectTestUsesNodeRuntime(framework, command) {
		if runtimeCommand, runtimeArgs, ok := projectTestNVMRuntimeCommand(repoRoot, command, args); ok {
			return runtimeCommand, runtimeArgs
		}
	}
	if projectTestCommandExists("mise") {
		wrappedArgs := append([]string{"exec", "--", command}, args...)
		return "mise", wrappedArgs
	}
	if projectTestCommandExists("asdf") {
		wrappedArgs := append([]string{"exec", command}, args...)
		return "asdf", wrappedArgs
	}
	if framework == projectTestFrameworkRSpec && projectTestCommandExists("rbenv") {
		wrappedArgs := append([]string{"exec", command}, args...)
		return "rbenv", wrappedArgs
	}
	return command, args
}

func projectTestUsesManagedRuntime(repoRoot string, framework string, command string) bool {
	if projectTestUsesNodeRuntime(framework, command) && projectTestNVMRuntimeAvailable(repoRoot) && projectTestProjectDeclaresRuntime(repoRoot, framework, command) {
		return true
	}
	if projectTestCommandExists("mise") && projectTestProjectDeclaresRuntime(repoRoot, framework, command) {
		return true
	}
	if projectTestCommandExists("asdf") && projectTestProjectDeclaresRuntime(repoRoot, framework, command) {
		return true
	}
	if framework == projectTestFrameworkRSpec && projectTestCommandExists("rbenv") && projectTestFileExists(repoRoot, ".ruby-version") {
		return true
	}
	return false
}

func projectTestProjectDeclaresRuntime(repoRoot string, framework string, command string) bool {
	switch framework {
	case projectTestFrameworkRSpec:
		return projectTestFileExists(repoRoot, ".ruby-version") || projectToolVersionsContains(repoRoot, "ruby")
	case projectTestFrameworkJest, projectTestFrameworkVitest:
		return projectTestFileExists(repoRoot, ".nvmrc") ||
			projectTestFileExists(repoRoot, ".node-version") ||
			projectToolVersionsContains(repoRoot, "nodejs") ||
			projectToolVersionsContains(repoRoot, "node")
	case projectTestFrameworkGo:
		return projectToolVersionsContains(repoRoot, "golang") || projectToolVersionsContains(repoRoot, "go")
	}

	base := filepath.Base(command)
	if base == "bundle" || base == "rspec" || base == "ruby" {
		return projectTestFileExists(repoRoot, ".ruby-version") || projectToolVersionsContains(repoRoot, "ruby")
	}
	if base == "node" || base == "npm" || base == "npx" || base == "yarn" || base == "pnpm" || base == "jest" || base == "vitest" {
		return projectTestFileExists(repoRoot, ".nvmrc") ||
			projectTestFileExists(repoRoot, ".node-version") ||
			projectToolVersionsContains(repoRoot, "nodejs") ||
			projectToolVersionsContains(repoRoot, "node")
	}
	if base == "go" {
		return projectToolVersionsContains(repoRoot, "golang") || projectToolVersionsContains(repoRoot, "go")
	}
	return false
}

func projectTestUsesNodeRuntime(framework string, command string) bool {
	if framework == projectTestFrameworkJest || framework == projectTestFrameworkVitest {
		return true
	}
	switch filepath.Base(command) {
	case "node", "npm", "npx", "yarn", "pnpm", "jest", "vitest":
		return true
	default:
		return false
	}
}

func projectTestNVMRuntimeAvailable(repoRoot string) bool {
	nvmDir := projectTestNVMDir()
	return nvmDir != "" && projectTestFileExists(repoRoot, ".nvmrc") && projectTestFileExists(nvmDir, "nvm.sh")
}

func projectTestNVMRuntimeCommand(repoRoot string, command string, args []string) (string, []string, bool) {
	version := projectTestNVMVersion(repoRoot)
	if version == "" || !projectTestNVMRuntimeAvailable(repoRoot) {
		return "", nil, false
	}
	parts := []string{
		"source " + shellQuote(filepath.Join(projectTestNVMDir(), "nvm.sh")),
		"nvm exec --silent " + shellQuote(version) + " " + shellQuote(command),
	}
	for _, arg := range args {
		parts[1] += " " + shellQuote(arg)
	}
	return "bash", []string{"-lc", strings.Join(parts, " && ")}, true
}

func projectTestNVMDir() string {
	if dir := strings.TrimSpace(os.Getenv("NVM_DIR")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".nvm")
}

func projectTestNVMVersion(repoRoot string) string {
	content, err := os.ReadFile(filepath.Join(repoRoot, ".nvmrc"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}

func projectToolVersionsContains(repoRoot string, toolName string) bool {
	content, err := os.ReadFile(filepath.Join(repoRoot, ".tool-versions"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if fields[0] == toolName {
			return true
		}
	}
	return false
}

func projectTestCommandExists(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}

func projectTestFileExists(repoRoot string, relPath string) bool {
	info, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(relPath)))
	return err == nil && !info.IsDir()
}

func projectTestDirExists(repoRoot string, relPath string) bool {
	info, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(relPath)))
	return err == nil && info.IsDir()
}

func projectTestFileContains(repoRoot string, relPath string, needle string) bool {
	content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(relPath)))
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(content)), strings.ToLower(needle))
}

type projectTestPackageJSON struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func projectTestPackageJSONHasTestRunner(repoRoot string, runner string) bool {
	content, err := os.ReadFile(filepath.Join(repoRoot, "package.json"))
	if err != nil {
		return false
	}
	var pkg projectTestPackageJSON
	if err := json.Unmarshal(content, &pkg); err != nil {
		return false
	}
	for name := range pkg.Dependencies {
		if projectTestPackageNameMatchesRunner(name, runner) {
			return true
		}
	}
	for name := range pkg.DevDependencies {
		if projectTestPackageNameMatchesRunner(name, runner) {
			return true
		}
	}
	for _, script := range pkg.Scripts {
		if projectTestScriptInvokesRunner(script, runner) {
			return true
		}
	}
	return false
}

func projectTestPackageNameMatchesRunner(name string, runner string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	switch runner {
	case projectTestFrameworkVitest:
		return name == "vitest" || strings.HasPrefix(name, "@vitest/")
	case projectTestFrameworkJest:
		return name == "jest" ||
			name == "jest-cli" ||
			strings.HasPrefix(name, "@jest/") ||
			strings.HasPrefix(name, "jest-") ||
			strings.HasSuffix(name, "-jest")
	default:
		return false
	}
}

func projectTestScriptInvokesRunner(script string, runner string) bool {
	for _, token := range strings.FieldsFunc(script, func(r rune) bool {
		switch r {
		case '/', '.', '_', '-', '@':
			return false
		default:
			return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9')
		}
	}) {
		token = strings.Trim(token, `"'`)
		if filepath.Base(token) == runner {
			return true
		}
	}
	return false
}

func shouldSkipProjectTestDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "tmp", "dist", "build", "coverage", ".next", ".turbo":
		return true
	default:
		return strings.HasPrefix(name, ".cache")
	}
}

func leadingProjectTestIndent(line string) int {
	count := 0
	for _, r := range line {
		switch r {
		case ' ':
			count++
		case '\t':
			count += 2
		default:
			return count
		}
	}
	return count
}

func firstNonEmptyProjectTestMatch(matches []string, start int) string {
	for i := start; i < len(matches); i++ {
		value := strings.TrimSpace(matches[i])
		if value != "" {
			return value
		}
	}
	return ""
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func projectTestNodeID(framework string, path string, line int) string {
	return fmt.Sprintf("%s:%s:%d", framework, filepath.ToSlash(path), line)
}

func projectTestFrameworkLabel(framework string) string {
	switch framework {
	case projectTestFrameworkRSpec:
		return "RSpec"
	case projectTestFrameworkGo:
		return "Go test"
	case projectTestFrameworkVitest:
		return "Vitest"
	case projectTestFrameworkJest:
		return "Jest"
	default:
		return framework
	}
}

func projectTestBranchScope(status string) string {
	switch status {
	case "A":
		return "added"
	case "D":
		return "deleted"
	case "R":
		return "renamed"
	default:
		return "changed"
	}
}

func goPackageFromTestPath(relPath string) string {
	dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relPath)))
	if dir == "." || dir == "" {
		return "."
	}
	return "./" + dir
}

func goTestRunPattern(node ProjectTestNode) string {
	parts := strings.Split(node.FullName, " / ")
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		quoted = append(quoted, "^"+regexp.QuoteMeta(part)+"$")
	}
	if len(quoted) == 0 {
		return "^" + regexp.QuoteMeta(node.Name) + "$"
	}
	return strings.Join(quoted, "/")
}

func normalizeProjectTestStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pass", "passed", "success":
		return "passed"
	case "skip", "skipped", "pending", "todo", "disabled":
		return "skipped"
	default:
		return "failed"
	}
}

func nonEmptyProjectTestID(primary string, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}

func nonEmptyProjectTestName(primary string, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return strings.TrimSpace(primary)
	}
	return strings.TrimSpace(fallback)
}

func normalizeProjectTestResultPath(repoRoot string, raw string) string {
	trimmed := filepath.Clean(strings.TrimSpace(raw))
	if trimmed == "" || trimmed == "." {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		if rel, err := filepath.Rel(repoRoot, trimmed); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(trimmed)
}

func findGoTestPath(discovery ProjectTestsDiscoveryResponse, testName string) (string, int) {
	baseName := strings.Split(testName, "/")[0]
	for _, file := range discovery.TestFiles {
		if file.Framework != projectTestFrameworkGo {
			continue
		}
		for _, node := range flattenProjectTestNodes(file.Tests) {
			if node.Name == baseName || node.FullName == strings.ReplaceAll(testName, "/", " / ") {
				return node.Path, node.Line
			}
		}
	}
	return "", 0
}

func truncateProjectTestOutput(output string, limit int) string {
	if len(output) <= limit {
		return output
	}
	return output[:limit] + "\n...[truncated]"
}

func runProjectTestQuickCommand(repoRoot string, command string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runtimeCommand, runtimeArgs := projectTestRuntimeCommand(repoRoot, "", command, args)
	cmd := exec.CommandContext(ctx, runtimeCommand, runtimeArgs...)
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
