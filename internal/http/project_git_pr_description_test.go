package http

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/storage"
)

func TestDefaultGitPRDescriptionPromptIncludesBranchDocumentation(t *testing.T) {

	if !strings.Contains(defaultGitPRDescriptionPromptTemplate, "{{branch_documentation}}") {
		t.Fatalf("default PR prompt must include branch documentation placeholder")
	}
}
func TestBuildGitPRDescriptionPromptIncludesBranchDocumentation(t *testing.T) {
	t.Parallel()

	prompt := buildGitPRDescriptionPrompt(
		"Branch documentation:\n{{branch_documentation}}\n\nChanged files:\n{{files}}",
		"feature/pr-context",
		"main",
		"Explain the customer-facing intent.",
		"M app.go",
		"- abc Add feature",
		"app.go | 2 ++",
		"+new behavior",
	)

	if !strings.Contains(prompt, "Branch documentation:\nExplain the customer-facing intent.") {
		t.Fatalf("expected branch documentation in prompt, got %q", prompt)
	}
	if strings.Contains(prompt, "{{branch_documentation}}") {
		t.Fatalf("expected branch documentation placeholder to be rendered, got %q", prompt)
	}
}

func TestReadProjectGitPRDescriptionBranchDocumentationUsesProjectSettings(t *testing.T) {
	t.Parallel()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	docsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(docsDir, "PR-42-better-summary.md"), []byte("  Branch goal and acceptance criteria.  \n"), 0o644); err != nil {
		t.Fatalf("failed to write branch documentation: %v", err)
	}

	now := time.Now()
	project := &storage.Project{
		ID:   "project-pr-branch-doc",
		Name: "PR branch documentation",
		Settings: map[string]string{
			projectBranchTaskDocDirectorySettingKey: docsDir,
			projectBranchTaskDocModeSettingKey:      "path",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	server := &Server{config: config.DefaultConfig(), store: store}
	got := server.readProjectGitPRDescriptionBranchDocumentation(project.ID, "kurapov/PR-42-better-summary")
	if got != "Branch goal and acceptance criteria." {
		t.Fatalf("unexpected branch documentation: %q", got)
	}
}
