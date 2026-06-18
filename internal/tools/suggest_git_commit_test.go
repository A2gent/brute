package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSuggestGitCommitTool_Execute(t *testing.T) {
	tool := NewSuggestGitCommitTool()

	t.Run("formats valid commit suggestion fence", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"title":     " Commit UI flow ",
			"message":   "Update session commit suggestions\n\n- Let agents provide commit context",
			"files":     []string{" caesar/src/App.tsx ", "brute/internal/tools/suggest_git_commit.go", "caesar/src/App.tsx"},
			"repo_path": " packages/app ",
		})

		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error: %s", result.Error)
		}
		if !strings.HasPrefix(result.Output, "```a2gent-git-commit\n") || !strings.HasSuffix(result.Output, "\n```") {
			t.Fatalf("expected a2gent-git-commit fenced block, got %q", result.Output)
		}
		if !strings.Contains(result.Output, `"title": "Commit UI flow"`) {
			t.Fatalf("expected normalized title, got %q", result.Output)
		}
		if strings.Count(result.Output, "caesar/src/App.tsx") != 1 {
			t.Fatalf("expected duplicate files to be removed, got %q", result.Output)
		}
		if !strings.Contains(result.Output, `"repo_path": "packages/app"`) {
			t.Fatalf("expected normalized repo path, got %q", result.Output)
		}

		metadataSuggestion, ok := result.Metadata["suggestion"].(SuggestGitCommitParams)
		if !ok {
			t.Fatalf("expected suggestion metadata, got %#v", result.Metadata["suggestion"])
		}
		if metadataSuggestion.Message == "" || len(metadataSuggestion.Files) != 2 {
			t.Fatalf("unexpected suggestion metadata: %#v", metadataSuggestion)
		}
	})

	t.Run("requires message and files", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"message": " ",
			"files":   []string{"src/app.ts"},
		})

		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success || !strings.Contains(result.Error, "message is required") {
			t.Fatalf("expected message validation failure, got %#v", result)
		}

		raw, _ = json.Marshal(map[string]interface{}{
			"message": "Update app",
			"files":   []string{" "},
		})
		result, err = tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success || !strings.Contains(result.Error, "files is required") {
			t.Fatalf("expected files validation failure, got %#v", result)
		}
	})

	t.Run("rejects file paths with newlines", func(t *testing.T) {
		raw, _ := json.Marshal(map[string]interface{}{
			"message": "Update app",
			"files":   []string{"src/app.ts\nsrc/other.ts"},
		})

		result, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if result.Success || !strings.Contains(result.Error, "files must not contain newlines") {
			t.Fatalf("expected newline validation failure, got %#v", result)
		}
	})
}
