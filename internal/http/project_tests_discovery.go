package http

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	rspecTestLinePattern = regexp.MustCompile("^\\s*(?:RSpec\\.)?(describe|context|feature|scenario|it|specify|example)\\s*(?:\\(?\\s*)?(?:\"([^\"]*)\"|'([^']*)'|`([^`]*)`|([^, do{]+))")
	goTestFuncPattern    = regexp.MustCompile(`^\s*func\s+(Test[A-Za-z0-9_]+)\s*\(\s*t\s+\*testing\.T\s*\)`)
	goSubtestPattern     = regexp.MustCompile("\\bt\\.Run\\(\\s*(?:\"([^\"]*)\"|'([^']*)'|`([^`]*)`)")
	jestTestLinePattern  = regexp.MustCompile("\\b(describe|it|test)\\s*(?:\\.\\w+)?\\s*\\(\\s*(?:\"([^\"]*)\"|'([^']*)'|`([^`]*)`)")
)

func buildProjectTestsDiscovery(_ string, repoPath string, repoRoot string) (ProjectTestsDiscoveryResponse, error) {
	target := projectGitBranchChangesTargetInfo{}
	branchStatuses := map[string]string{}
	branchChangedCodeFiles := map[string]bool{}
	branchScopeAvailable := false
	if projectHasGitMetadata(repoRoot) {
		target = projectGitBranchChangesTarget(repoRoot)
		files, err := loadProjectGitTestingScopeChangedFiles(repoRoot, target)
		if err != nil {
			return ProjectTestsDiscoveryResponse{}, err
		}
		branchScopeAvailable = target.Available || len(files) > 0
		for _, file := range files {
			branchStatuses[file.Path] = file.Status
			if classifyProjectTestFile(file.Path) == "" {
				branchChangedCodeFiles[file.Path] = true
			}
		}
	}

	detections := detectProjectTestFrameworks(repoRoot)
	javaScriptFramework := projectJavaScriptTestFramework(detections)
	testFiles := make([]ProjectTestFile, 0)

	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			if shouldSkipProjectTestDir(name) && path != repoRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}

		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		framework := classifyProjectTestFileForJavaScriptFramework(rel, javaScriptFramework)
		if framework == "" {
			return nil
		}

		status := branchStatuses[rel]
		addedLines := map[int]bool{}
		if status == "A" {
			addedLines[-1] = true
		} else if status != "" {
			addedLines = projectGitTestingScopeAddedLineNumbers(repoRoot, target, status, rel)
		}

		nodes := parseProjectTestFile(repoRoot, rel, framework, addedLines)
		testFile := ProjectTestFile{
			ID:        framework + ":" + rel,
			Framework: framework,
			Path:      rel,
			Tests:     nodes,
		}
		if framework == projectTestFrameworkGo {
			testFile.Package = goPackageFromTestPath(rel)
		}
		if status != "" {
			testFile.BranchStatus = status
			testFile.BranchScope = projectTestBranchScope(status)
		}
		testFiles = append(testFiles, testFile)
		return nil
	})
	if err != nil {
		return ProjectTestsDiscoveryResponse{}, err
	}

	sort.SliceStable(testFiles, func(i, j int) bool {
		if testFiles[i].Framework != testFiles[j].Framework {
			return testFiles[i].Framework < testFiles[j].Framework
		}
		return testFiles[i].Path < testFiles[j].Path
	})

	branchTestFiles := make([]ProjectTestFile, 0)
	testCounts := map[string]int{}
	branchTestCounts := map[string]int{}
	for _, file := range testFiles {
		count := countProjectTestNodes(file.Tests)
		testCounts[file.Framework] += count
		if file.BranchStatus != "" {
			branchTestFiles = append(branchTestFiles, file)
			branchTestCounts[file.Framework] += count
		}
	}

	frameworks := make([]ProjectTestFrameworkInfo, 0, 4)
	for _, id := range []string{projectTestFrameworkRSpec, projectTestFrameworkGo, projectTestFrameworkVitest, projectTestFrameworkJest} {
		info := detections[id]
		if testCounts[id] > 0 {
			info.Available = true
			if info.Reason == "" {
				info.Reason = "Detected test files"
			}
		}
		info.TestCount = testCounts[id]
		info.BranchTestCount = branchTestCounts[id]
		info.SupportsIndividual = true
		switch id {
		case projectTestFrameworkGo:
			info.SupportsCoverage = true
			info.CoverageMode = "per-test branch mapping and aggregate coverprofile"
		case projectTestFrameworkVitest:
			info.SupportsCoverage = true
			info.CoverageMode = "aggregate V8/Istanbul coverage"
		case projectTestFrameworkJest:
			info.SupportsCoverage = true
			info.CoverageMode = "aggregate Istanbul coverage"
		case projectTestFrameworkRSpec:
			info.SupportsCoverage = false
			info.CoverageMode = "requires project SimpleCov setup"
		}
		frameworks = append(frameworks, info)
	}

	_ = branchChangedCodeFiles
	return ProjectTestsDiscoveryResponse{
		RootFolder:             repoRoot,
		RepoPath:               repoPath,
		CurrentBranch:          target.CurrentBranch,
		BaseBranch:             target.BaseBranch,
		BranchChangesAvailable: branchScopeAvailable,
		Frameworks:             frameworks,
		TestFiles:              testFiles,
		BranchTestFiles:        branchTestFiles,
	}, nil
}

func detectProjectTestFrameworks(repoRoot string) map[string]ProjectTestFrameworkInfo {
	frameworks := map[string]ProjectTestFrameworkInfo{
		projectTestFrameworkRSpec:  {ID: projectTestFrameworkRSpec, Label: "RSpec"},
		projectTestFrameworkGo:     {ID: projectTestFrameworkGo, Label: "Go test"},
		projectTestFrameworkJest:   {ID: projectTestFrameworkJest, Label: "Jest"},
		projectTestFrameworkVitest: {ID: projectTestFrameworkVitest, Label: "Vitest"},
	}

	if projectTestFileExists(repoRoot, ".rspec") || projectTestDirExists(repoRoot, "spec") || projectTestFileContains(repoRoot, "Gemfile", "rspec") {
		info := frameworks[projectTestFrameworkRSpec]
		info.Available = true
		info.Reason = "RSpec project markers found"
		frameworks[projectTestFrameworkRSpec] = info
	}
	if projectTestFileExists(repoRoot, "go.mod") {
		info := frameworks[projectTestFrameworkGo]
		info.Available = true
		info.Reason = "go.mod found"
		frameworks[projectTestFrameworkGo] = info
	}
	if projectTestPackageJSONHasTestRunner(repoRoot, projectTestFrameworkVitest) {
		info := frameworks[projectTestFrameworkVitest]
		info.Available = true
		info.Reason = "package.json references vitest"
		frameworks[projectTestFrameworkVitest] = info
	}
	if projectTestPackageJSONHasTestRunner(repoRoot, projectTestFrameworkJest) {
		info := frameworks[projectTestFrameworkJest]
		info.Available = true
		info.Reason = "package.json references jest"
		frameworks[projectTestFrameworkJest] = info
	}

	return frameworks
}

func classifyProjectTestFile(relPath string) string {
	return classifyProjectTestFileForJavaScriptFramework(relPath, projectTestFrameworkJest)
}

func classifyProjectTestFileForJavaScriptFramework(relPath string, javaScriptFramework string) string {
	path := filepath.ToSlash(relPath)
	lower := strings.ToLower(path)
	base := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(lower, "_spec.rb") && (strings.HasPrefix(lower, "spec/") || strings.Contains(lower, "/spec/")) {
		return projectTestFrameworkRSpec
	}
	if strings.HasSuffix(lower, "_test.go") {
		return projectTestFrameworkGo
	}
	if strings.Contains(lower, "/__tests__/") || strings.HasPrefix(lower, "__tests__/") {
		if isJestTestExtension(base) {
			return normalizeProjectJavaScriptTestFramework(javaScriptFramework)
		}
	}
	for _, marker := range []string{".test.", ".spec."} {
		if strings.Contains(base, marker) && isJestTestExtension(base) {
			return normalizeProjectJavaScriptTestFramework(javaScriptFramework)
		}
	}
	return ""
}

func projectJavaScriptTestFramework(detections map[string]ProjectTestFrameworkInfo) string {
	if detections[projectTestFrameworkVitest].Available {
		return projectTestFrameworkVitest
	}
	if detections[projectTestFrameworkJest].Available {
		return projectTestFrameworkJest
	}
	return projectTestFrameworkJest
}

func normalizeProjectJavaScriptTestFramework(framework string) string {
	switch framework {
	case projectTestFrameworkVitest:
		return projectTestFrameworkVitest
	default:
		return projectTestFrameworkJest
	}
}

func isJestTestExtension(base string) bool {
	for _, suffix := range []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

func parseProjectTestFile(repoRoot string, relPath string, framework string, addedLines map[int]bool) []ProjectTestNode {
	content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(relPath)))
	if err != nil || len(content) > 2*1024*1024 {
		return []ProjectTestNode{}
	}
	switch framework {
	case projectTestFrameworkRSpec:
		return parseRSpecTests(relPath, string(content), addedLines)
	case projectTestFrameworkGo:
		return parseGoTests(relPath, string(content), addedLines)
	case projectTestFrameworkJest:
		return parseJestTests(relPath, string(content), addedLines)
	case projectTestFrameworkVitest:
		return parseVitestTests(relPath, string(content), addedLines)
	default:
		return []ProjectTestNode{}
	}
}

func parseRSpecTests(relPath string, content string, addedLines map[int]bool) []ProjectTestNode {
	return parseIndentedTests(relPath, content, projectTestFrameworkRSpec, func(line string) (string, string, bool) {
		matches := rspecTestLinePattern.FindStringSubmatch(line)
		if matches == nil {
			return "", "", false
		}
		kind := matches[1]
		name := firstNonEmptyProjectTestMatch(matches, 2)
		if name == "" {
			name = kind
		}
		nodeType := "group"
		if kind == "it" || kind == "specify" || kind == "example" || kind == "scenario" {
			nodeType = "test"
		}
		return nodeType, name, true
	}, addedLines)
}

func parseJestTests(relPath string, content string, addedLines map[int]bool) []ProjectTestNode {
	return parseIndentedTests(relPath, content, projectTestFrameworkJest, func(line string) (string, string, bool) {
		matches := jestTestLinePattern.FindStringSubmatch(line)
		if matches == nil {
			return "", "", false
		}
		kind := matches[1]
		name := firstNonEmptyProjectTestMatch(matches, 2)
		if name == "" {
			name = kind
		}
		nodeType := "test"
		if kind == "describe" {
			nodeType = "group"
		}
		return nodeType, name, true
	}, addedLines)
}

func parseVitestTests(relPath string, content string, addedLines map[int]bool) []ProjectTestNode {
	return parseIndentedTests(relPath, content, projectTestFrameworkVitest, func(line string) (string, string, bool) {
		matches := jestTestLinePattern.FindStringSubmatch(line)
		if matches == nil {
			return "", "", false
		}
		kind := matches[1]
		name := firstNonEmptyProjectTestMatch(matches, 2)
		if name == "" {
			name = kind
		}
		nodeType := "test"
		if kind == "describe" {
			nodeType = "group"
		}
		return nodeType, name, true
	}, addedLines)
}

func parseIndentedTests(relPath string, content string, framework string, matcher func(string) (string, string, bool), addedLines map[int]bool) []ProjectTestNode {
	roots := make([]*projectTestNodeBuilder, 0)
	stack := make([]*projectTestNodeBuilder, 0)
	indents := make([]int, 0)

	lines := strings.Split(content, "\n")
	for index, line := range lines {
		nodeType, name, ok := matcher(line)
		if !ok {
			continue
		}
		lineNumber := index + 1
		indent := leadingProjectTestIndent(line)
		for len(stack) > 0 && indent <= indents[len(indents)-1] {
			stack = stack[:len(stack)-1]
			indents = indents[:len(indents)-1]
		}
		fullName := name
		if len(stack) > 0 {
			parts := make([]string, 0, len(stack)+1)
			for _, parent := range stack {
				parts = append(parts, parent.Name)
			}
			parts = append(parts, name)
			fullName = strings.Join(parts, " / ")
		}
		node := &projectTestNodeBuilder{
			ID:          projectTestNodeID(framework, relPath, lineNumber),
			Framework:   framework,
			Name:        name,
			FullName:    fullName,
			Path:        relPath,
			Line:        lineNumber,
			Type:        nodeType,
			BranchAdded: addedLines[-1] || addedLines[lineNumber],
		}
		if len(stack) == 0 {
			roots = append(roots, node)
		} else {
			stack[len(stack)-1].Children = append(stack[len(stack)-1].Children, node)
		}
		if nodeType == "group" {
			stack = append(stack, node)
			indents = append(indents, indent)
		}
	}

	return projectTestBuildersToNodes(roots)
}

func parseGoTests(relPath string, content string, addedLines map[int]bool) []ProjectTestNode {
	roots := make([]*projectTestNodeBuilder, 0)
	var current *projectTestNodeBuilder
	subtestStack := make([]*projectTestNodeBuilder, 0)
	subtestIndents := make([]int, 0)
	lines := strings.Split(content, "\n")

	for index, line := range lines {
		lineNumber := index + 1
		if matches := goTestFuncPattern.FindStringSubmatch(line); matches != nil {
			name := matches[1]
			current = &projectTestNodeBuilder{
				ID:          projectTestNodeID(projectTestFrameworkGo, relPath, lineNumber),
				Framework:   projectTestFrameworkGo,
				Name:        name,
				FullName:    name,
				Path:        relPath,
				Line:        lineNumber,
				Type:        "test",
				BranchAdded: addedLines[-1] || addedLines[lineNumber],
				Metadata: map[string]string{
					"package": goPackageFromTestPath(relPath),
				},
			}
			roots = append(roots, current)
			subtestStack = subtestStack[:0]
			subtestIndents = subtestIndents[:0]
			continue
		}
		if current == nil {
			continue
		}
		matches := goSubtestPattern.FindStringSubmatch(line)
		if matches == nil {
			continue
		}
		indent := leadingProjectTestIndent(line)
		for len(subtestStack) > 0 && indent <= subtestIndents[len(subtestIndents)-1] {
			subtestStack = subtestStack[:len(subtestStack)-1]
			subtestIndents = subtestIndents[:len(subtestIndents)-1]
		}
		name := firstNonEmptyProjectTestMatch(matches, 1)
		if name == "" {
			name = "subtest"
		}
		parent := current
		fullParts := []string{current.Name}
		if len(subtestStack) > 0 {
			parent = subtestStack[len(subtestStack)-1]
			fullParts = strings.Split(parent.FullName, " / ")
		}
		fullParts = append(fullParts, name)
		node := &projectTestNodeBuilder{
			ID:          projectTestNodeID(projectTestFrameworkGo, relPath, lineNumber),
			Framework:   projectTestFrameworkGo,
			Name:        name,
			FullName:    strings.Join(fullParts, " / "),
			Path:        relPath,
			Line:        lineNumber,
			Type:        "test",
			BranchAdded: addedLines[-1] || addedLines[lineNumber],
			Metadata: map[string]string{
				"package": goPackageFromTestPath(relPath),
			},
		}
		parent.Children = append(parent.Children, node)
		subtestStack = append(subtestStack, node)
		subtestIndents = append(subtestIndents, indent)
	}

	return projectTestBuildersToNodes(roots)
}

func projectTestBuildersToNodes(builders []*projectTestNodeBuilder) []ProjectTestNode {
	nodes := make([]ProjectTestNode, 0, len(builders))
	for _, builder := range builders {
		node := ProjectTestNode{
			ID:          builder.ID,
			Framework:   builder.Framework,
			Name:        builder.Name,
			FullName:    builder.FullName,
			Path:        builder.Path,
			Line:        builder.Line,
			Type:        builder.Type,
			BranchAdded: builder.BranchAdded,
			Metadata:    builder.Metadata,
			Children:    projectTestBuildersToNodes(builder.Children),
		}
		nodes = append(nodes, node)
	}
	return nodes
}
