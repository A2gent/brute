package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const gitCommitSuggestionFenceLanguage = "a2gent-git-commit"

// SuggestGitCommitParams describes a commit suggestion controlled by the agent.
type SuggestGitCommitParams struct {
	Title    string   `json:"title,omitempty"`
	Message  string   `json:"message"`
	Files    []string `json:"files"`
	RepoPath string   `json:"repo_path,omitempty"`
}

// SuggestGitCommitTool formats a commit suggestion for the Caesar session UI.
type SuggestGitCommitTool struct{}

func NewSuggestGitCommitTool() *SuggestGitCommitTool {
	return &SuggestGitCommitTool{}
}

func (t *SuggestGitCommitTool) Name() string {
	return "suggest_git_commit"
}

func (t *SuggestGitCommitTool) Description() string {
	return `Suggest a Git commit for the current session by producing an a2gent-git-commit card.

Use this once near the end of a task after you have made or verified changes that the user may want to commit. The suggestion should use your session context, not a fresh guess from the diff.

Important usage:
- Include a commit message that accurately summarizes why the session changed the code.
- Include only project-relative files that belong to this session's logical change set.
- Exclude unrelated files, generated noise, and files changed by other concurrent sessions.
- Do not call this tool if there are no relevant file changes to commit.
- Do not say the files were staged or committed; Caesar only highlights suggested files and lets the user stage them explicitly.

The tool returns a fenced block that must be included verbatim in your final assistant message. Caesar renders that block as the session commit suggestion UI.`
}

func (t *SuggestGitCommitTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Optional short title for the commit suggestion. Defaults to 'Suggested commit'.",
			},
			"message": map[string]interface{}{
				"type":        "string",
				"description": "Full Git commit message. Use an imperative summary line and optional body with concise details.",
			},
			"files": map[string]interface{}{
				"type":        "array",
				"description": "Project-relative paths that should be staged for this suggested commit.",
				"items": map[string]interface{}{
					"type": "string",
				},
			},
			"repo_path": map[string]interface{}{
				"type":        "string",
				"description": "Optional repository path when the project contains multiple Git repositories. Leave empty for the project root.",
			},
		},
		"required": []string{"message", "files"},
	}
}

func (t *SuggestGitCommitTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var p SuggestGitCommitParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	suggestion, errMessage := normalizeSuggestGitCommitParams(p)
	if errMessage != "" {
		return &Result{Success: false, Error: errMessage}, nil
	}

	payload, err := json.MarshalIndent(suggestion, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal git commit suggestion: %w", err)
	}

	fencedBlock := fmt.Sprintf("```%s\n%s\n```", gitCommitSuggestionFenceLanguage, payload)
	return &Result{
		Success: true,
		Output:  fencedBlock,
		Metadata: map[string]interface{}{
			"kind":       "git_commit_suggestion",
			"language":   gitCommitSuggestionFenceLanguage,
			"suggestion": suggestion,
		},
	}, nil
}

func normalizeSuggestGitCommitParams(p SuggestGitCommitParams) (SuggestGitCommitParams, string) {
	suggestion := SuggestGitCommitParams{
		Title:    strings.TrimSpace(p.Title),
		Message:  strings.TrimSpace(p.Message),
		RepoPath: strings.TrimSpace(p.RepoPath),
	}

	if suggestion.Title == "" {
		suggestion.Title = "Suggested commit"
	}
	if suggestion.Message == "" {
		return SuggestGitCommitParams{}, "message is required"
	}

	seenFiles := make(map[string]struct{}, len(p.Files))
	for _, file := range p.Files {
		trimmed := strings.TrimSpace(file)
		if trimmed == "" {
			continue
		}
		if strings.ContainsAny(trimmed, "\r\n") {
			return SuggestGitCommitParams{}, "files must not contain newlines"
		}
		if _, seen := seenFiles[trimmed]; seen {
			continue
		}
		seenFiles[trimmed] = struct{}{}
		suggestion.Files = append(suggestion.Files, trimmed)
	}
	if len(suggestion.Files) == 0 {
		return SuggestGitCommitParams{}, "files is required"
	}

	return suggestion, ""
}

var _ Tool = (*SuggestGitCommitTool)(nil)
