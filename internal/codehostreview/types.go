package codehostreview

import "time"

// Repository is provider-neutral so Caesar and agent tools do not depend on a
// Bitbucket-specific API shape. Future GitHub adapters use the same contract.
type Repository struct {
	Provider string `json:"provider"`
	Owner    string `json:"owner"`
	Name     string `json:"name"`
}

type PullRequest struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	State        string `json:"state"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	URL          string `json:"url,omitempty"`
}

type Comment struct {
	ID         string    `json:"id"`
	ParentID   string    `json:"parent_id,omitempty"`
	Body       string    `json:"body"`
	Author     string    `json:"author"`
	AvatarURL  string    `json:"avatar_url,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
	FilePath   string    `json:"file_path,omitempty"`
	Side       string    `json:"side,omitempty"`
	LineNumber int       `json:"line_number,omitempty"`
	StartLine  int       `json:"start_line,omitempty"`
	Deleted    bool      `json:"deleted,omitempty"`
	Resolved   bool      `json:"resolved,omitempty"`
	URL        string    `json:"url,omitempty"`
}

type Review struct {
	Provider    string       `json:"provider"`
	Repository  Repository   `json:"repository"`
	PullRequest *PullRequest `json:"pull_request,omitempty"`
	Comments    []Comment    `json:"comments"`
}

type GetReviewRequest struct {
	Repository      Repository
	Branch          string
	PullRequestID   string
	IntegrationID   string
	IntegrationName string
}

type ReplyRequest struct {
	Repository      Repository
	PullRequestID   string
	CommentID       string
	Body            string
	IntegrationID   string
	IntegrationName string
}
