package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const gitReviewOverlayDeletedFileInstruction = `For a fully deleted file, always provide at least one deletions annotation that explains why the whole file was deleted. Infer the branch-level reason from the selected file's complete diff and the full changed-files list, such as a replacement, consolidation, or obsolete flow, rather than merely saying that the file or its code was removed.`

const defaultGitReviewOverlayPromptTemplate = `You explain why each changed part of one file exists for a visual code-review overlay.
Return JSON only. No markdown fences, prefaces, or commentary.
Schema:
{
  "annotations": [
    {
      "file_path": "path/from/input",
      "side": "additions" | "deletions",
      "line_number": 123,
      "end_line_number": 125,
      "title": "human outcome, max 80 chars",
      "body": "1-4 concise sentences explaining what changed, why it likely exists, and what behavior/risk/review point it creates"
    }
  ]
}
Rules:
- The input is the currently selected review file. Cover every changed code block or isolated changed line that has meaningful behavior, including added, deleted, and updated/replaced code.
- Treat adjacent deletion/addition groups as updated code. Explain the old behavior on deletions when it matters, and the new behavior on additions when it matters.
- ` + gitReviewOverlayDeletedFileInstruction + `
- Use one annotation per logical changed block. Use line-level annotations for isolated one-line changes. Split annotations when changed lines are separated or serve different purposes.
- line_number and end_line_number must refer only to changed lines visible in the diff. Use additions for new lines and deletions for removed lines.
- Explain behavior, intent, data flow, user-visible effects, risk, test impact, or integration impact in human-readable words.
- Include small changes when their purpose can be inferred from surrounding context. Omit only generated noise, pure formatting, or changes whose purpose cannot be inferred.
- Do NOT restate the diff, quote code, list symbols, mention +N/-N counts, or say only that code was added/removed/changed.
- Do NOT use generic titles like "Important branch change", "New file added", "Change added", "Line updated", or "Code updated".
- Write title and body in English, even if surrounding session or project text uses another language.
- If you cannot explain WHAT the code does and WHY it matters, omit that annotation.

Current branch: {{branch}}
Base branch: {{base_branch}}

Changed files:
{{files}}

Diff snippets:
{{diffs}}`

type projectGitReviewOverlayLineIndex struct {
	Additions map[int]bool
	Deletions map[int]bool
	// Store changed-line snippets so fallback notes can still explain the diff when the model fails.
	AdditionText map[int]string
	DeletionText map[int]string
}

type projectGitReviewOverlayModelResponse struct {
	Annotations []ProjectGitReviewOverlayAnnotation `json:"annotations"`
}

type projectGitReviewOverlayDiffContext struct {
	Sections     []string
	AllowedLines map[string]projectGitReviewOverlayLineIndex
	DiffHashes   map[string]string
}

func (s *Server) handleGetProjectGitReviewOverlay(w http.ResponseWriter, r *http.Request) {
	projectID, repoPath, targetRepoRoot, target, ok := s.resolveProjectGitPRDescriptionTarget(w, r, strings.TrimSpace(r.URL.Query().Get("repoPath")))
	if !ok {
		return
	}
	response := ProjectGitReviewOverlayResponse{
		CurrentBranch: target.CurrentBranch,
		BaseBranch:    target.BaseBranch,
		Annotations:   []ProjectGitReviewOverlayAnnotation{},
	}
	if !target.Available {
		s.jsonResponse(w, http.StatusOK, response)
		return
	}

	files, err := loadProjectGitBranchChangedFiles(targetRepoRoot, target)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read branch changes: "+err.Error())
		return
	}
	diffContext := buildProjectGitReviewOverlayDiffContext(targetRepoRoot, target, files, "", 0)
	annotations, err := s.loadCachedProjectGitReviewOverlayAnnotations(projectID, repoPath, target, diffContext)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to load review overlay: "+err.Error())
		return
	}
	response.Annotations = annotations
	s.jsonResponse(w, http.StatusOK, response)
}

func (s *Server) handleGenerateProjectGitReviewOverlay(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	var req ProjectGitReviewOverlayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	projectID, repoPath, targetRepoRoot, target, ok := s.resolveProjectGitPRDescriptionTarget(w, r, strings.TrimSpace(req.RepoPath))
	if !ok {
		return
	}
	if !target.Available {
		s.errorResponse(w, http.StatusBadRequest, "Branch comparison is not available for this repository")
		return
	}
	targetFilePath, err := resolveGitRepoFilePath(targetRepoRoot, req.FilePath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "file_path is required and must stay inside the repository")
		return
	}

	files, err := loadProjectGitBranchChangedFiles(targetRepoRoot, target)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read branch changes: "+err.Error())
		return
	}
	branchFilesMarkdown := projectGitCommitFilesMarkdown(files)
	files = filterProjectGitReviewOverlayFiles(targetRepoRoot, files, targetFilePath)
	if len(files) == 0 {
		s.jsonResponse(w, http.StatusOK, ProjectGitReviewOverlayResponse{
			CurrentBranch: target.CurrentBranch,
			BaseBranch:    target.BaseBranch,
			Annotations:   []ProjectGitReviewOverlayAnnotation{},
		})
		return
	}

	annotations := s.generateProjectGitReviewOverlayAnnotations(r.Context(), projectID, repoPath, targetRepoRoot, target, files, targetFilePath, branchFilesMarkdown)
	s.jsonResponse(w, http.StatusOK, ProjectGitReviewOverlayResponse{
		CurrentBranch: target.CurrentBranch,
		BaseBranch:    target.BaseBranch,
		Annotations:   annotations,
	})
}

func (s *Server) generateProjectGitReviewOverlayAnnotations(ctx context.Context, projectID string, repoPath string, repoRoot string, target projectGitBranchChangesTargetInfo, files []ProjectGitCommitFile, targetFilePath string, branchFilesMarkdown string) []ProjectGitReviewOverlayAnnotation {
	diffContext := buildProjectGitReviewOverlayDiffContext(repoRoot, target, files, targetFilePath, 1)
	if len(diffContext.Sections) == 0 {
		return []ProjectGitReviewOverlayAnnotation{}
	}
	fallbackAnnotations := buildFallbackProjectGitReviewOverlayAnnotations(files, diffContext.AllowedLines)

	settings, settingsErr := s.store.GetSettings()
	if settingsErr != nil {
		logging.Warn("Failed to load settings for review overlay generation: %v", settingsErr)
		settings = map[string]string{}
	}
	templates := serverPromptTemplatesFromSettings(settings)
	prompt := buildGitReviewOverlayPrompt(
		templates.GitReviewOverlayPromptTemplate,
		target.CurrentBranch,
		target.BaseBranch,
		branchFilesMarkdown,
		strings.Join(diffContext.Sections, "\n\n"),
	)

	configuredTarget := s.resolvePromptLLMTarget(settings, promptLLMCaseGitReviewOverlay)
	activeProviderType := config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))
	activeModel := s.resolveModelForProvider(activeProviderType)

	generationCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()

	response, err := s.generateGitReviewOverlayWithProvider(generationCtx, configuredTarget.ProviderType, configuredTarget.Model, prompt)
	if err != nil && configuredTarget.ProviderType != activeProviderType {
		logging.Warn("Review overlay generation failed with configured provider %s: %v. Retrying active provider %s", configuredTarget.ProviderType, err, activeProviderType)
		response, err = s.generateGitReviewOverlayWithProvider(generationCtx, activeProviderType, activeModel, prompt)
	}
	if err != nil {
		logging.Warn("Review overlay generation failed: %v", err)
		if saveErr := s.saveProjectGitReviewOverlayAnnotations(projectID, repoPath, target, diffContext.DiffHashes, fallbackAnnotations); saveErr != nil {
			logging.Warn("Failed to cache fallback review overlay annotations: %v", saveErr)
		}
		return fallbackAnnotations
	}

	annotations := sanitizeProjectGitReviewOverlayResponse(response.Content, diffContext.AllowedLines)
	if len(annotations) == 0 {
		logging.Warn("Review overlay generation produced no useful annotations after sanitization; using fallback. Raw response: %s", truncateText(response.Content, 1200))
		annotations = fallbackAnnotations
	}
	if err := s.saveProjectGitReviewOverlayAnnotations(projectID, repoPath, target, diffContext.DiffHashes, annotations); err != nil {
		logging.Warn("Failed to cache review overlay annotations: %v", err)
	}
	return annotations
}

func (s *Server) generateGitReviewOverlayWithProvider(ctx context.Context, providerType config.ProviderType, model string, prompt string) (*llm.ChatResponse, error) {
	target, err := s.resolveExecutionTarget(ctx, providerType, model, prompt, nil)
	if err != nil {
		return nil, err
	}
	return target.Client.Chat(ctx, &llm.ChatRequest{
		Model: target.Model,
		Messages: []llm.Message{
			{Role: "user", Content: prompt},
		},
		Temperature: 0.2,
		MaxTokens:   4000,
	})
}

func buildGitReviewOverlayPrompt(template string, branch string, baseBranch string, files string, diffs string) string {
	prompt := template
	prompt = strings.ReplaceAll(prompt, "{{branch}}", strings.TrimSpace(branch))
	prompt = strings.ReplaceAll(prompt, "{{base_branch}}", strings.TrimSpace(baseBranch))
	prompt = strings.ReplaceAll(prompt, "{{files}}", strings.TrimSpace(files))
	prompt = strings.ReplaceAll(prompt, "{{diffs}}", strings.TrimSpace(diffs))
	if !strings.Contains(prompt, gitReviewOverlayDeletedFileInstruction) {
		prompt += "\n\nRequired deleted-file rule:\n- " + gitReviewOverlayDeletedFileInstruction
	}
	return strings.TrimSpace(prompt)
}
