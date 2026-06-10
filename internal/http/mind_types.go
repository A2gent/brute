package http

type MindConfigResponse struct {
	RootFolder string `json:"root_folder"`
}

type UpdateMindConfigRequest struct {
	RootFolder string `json:"root_folder"`
}

type MindTreeEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	HasChild bool   `json:"has_child,omitempty"`
}

type MindTreeResponse struct {
	RootFolder string          `json:"root_folder"`
	Path       string          `json:"path"`
	Entries    []MindTreeEntry `json:"entries"`
}

type MindFileResponse struct {
	RootFolder string `json:"root_folder"`
	Path       string `json:"path"`
	Content    string `json:"content"`
}

type MindFileDeleteResponse struct {
	RootFolder string `json:"root_folder"`
	Path       string `json:"path"`
}

type UpdateMindFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type MoveMindFileRequest struct {
	FromPath string `json:"from_path"`
	ToPath   string `json:"to_path"`
}

type MoveMindFileResponse struct {
	RootFolder string `json:"root_folder"`
	FromPath   string `json:"from_path"`
	ToPath     string `json:"to_path"`
}

type CreateFolderRequest struct {
	Path string `json:"path"`
}

type CreateFolderResponse struct {
	RootFolder string `json:"root_folder"`
	Path       string `json:"path"`
}

type RenameEntryRequest struct {
	OldPath string `json:"old_path"`
	NewName string `json:"new_name"`
}

type RenameEntryResponse struct {
	RootFolder string `json:"root_folder"`
	OldPath    string `json:"old_path"`
	NewPath    string `json:"new_path"`
}

type ProjectGitChangedFile struct {
	Path           string `json:"path"`
	Status         string `json:"status"`
	IndexStatus    string `json:"index_status"`
	WorktreeStatus string `json:"worktree_status"`
	Staged         bool   `json:"staged"`
	Untracked      bool   `json:"untracked"`
	HasConflict    bool   `json:"has_conflict"`
}

type ProjectGitStatusResponse struct {
	RootFolder              string                  `json:"root_folder"`
	HasGit                  bool                    `json:"has_git"`
	CurrentBranch           string                  `json:"current_branch,omitempty"`
	BranchChangesAvailable  bool                    `json:"branch_changes_available"`
	BranchChangesBaseBranch string                  `json:"branch_changes_base_branch,omitempty"`
	Files                   []ProjectGitChangedFile `json:"files"`
}

type ProjectGitCommitRequest struct {
	Message  string `json:"message"`
	RepoPath string `json:"repo_path,omitempty"`
}

type ProjectGitFileRequest struct {
	RepoPath string `json:"repo_path,omitempty"`
	Path     string `json:"path"`
}

type ProjectGitCommitResponse struct {
	RootFolder     string `json:"root_folder"`
	Commit         string `json:"commit"`
	FilesCommitted int    `json:"files_committed"`
}

type ProjectGitCommitMessageRequest struct {
	RepoPath string `json:"repo_path,omitempty"`
}

type ProjectGitPRDescriptionRequest struct {
	RepoPath string `json:"repo_path,omitempty"`
}

type ProjectGitPRDescriptionSaveRequest struct {
	RepoPath string `json:"repo_path,omitempty"`
	Content  string `json:"content"`
}

type ProjectGitFileDiffResponse struct {
	Path    string `json:"path"`
	Preview string `json:"preview"`
}

type ProjectGitBranch struct {
	Name      string `json:"name"`
	Current   bool   `json:"current"`
	Remote    bool   `json:"remote"`
	Ahead     int    `json:"ahead"`
	Behind    int    `json:"behind"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type ProjectGitHistoryCommit struct {
	Hash       string   `json:"hash"`
	ShortHash  string   `json:"short_hash"`
	Subject    string   `json:"subject"`
	AuthorName string   `json:"author_name"`
	AuthoredAt string   `json:"authored_at"`
	Refs       []string `json:"refs"`
	Parents    []string `json:"parents"`
	Branch     string   `json:"branch,omitempty"`
}

type ProjectGitHistoryResponse struct {
	RootFolder    string                    `json:"root_folder"`
	CurrentBranch string                    `json:"current_branch"`
	Branches      []ProjectGitBranch        `json:"branches"`
	Commits       []ProjectGitHistoryCommit `json:"commits"`
}

type ProjectGitCommitFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
}

type ProjectGitCommitFilesResponse struct {
	Commit string                 `json:"commit"`
	Files  []ProjectGitCommitFile `json:"files"`
}

type ProjectGitCommitDiffResponse struct {
	Commit  string `json:"commit"`
	Path    string `json:"path"`
	Preview string `json:"preview"`
}

type ProjectGitBranchChangesResponse struct {
	RootFolder    string                 `json:"root_folder"`
	CurrentBranch string                 `json:"current_branch"`
	BaseBranch    string                 `json:"base_branch"`
	Available     bool                   `json:"available"`
	Files         []ProjectGitCommitFile `json:"files"`
}

type ProjectGitBranchDiffResponse struct {
	CurrentBranch string `json:"current_branch"`
	BaseBranch    string `json:"base_branch"`
	Path          string `json:"path"`
	Preview       string `json:"preview"`
}

type ProjectGitCommitMessageResponse struct {
	Message string `json:"message"`
}

type ProjectGitPRDescriptionResponse struct {
	ProjectID     string `json:"project_id"`
	RepoPath      string `json:"repo_path"`
	CurrentBranch string `json:"current_branch"`
	BaseBranch    string `json:"base_branch"`
	Available     bool   `json:"available"`
	Content       string `json:"content"`
	CreatedAt     string `json:"created_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

type ProjectGitPushRequest struct {
	RepoPath string `json:"repo_path,omitempty"`
}

type ProjectGitPullRequest struct {
	RepoPath string `json:"repo_path,omitempty"`
}

type ProjectGitCheckoutRequest struct {
	RepoPath string `json:"repo_path,omitempty"`
	Branch   string `json:"branch"`
	Create   bool   `json:"create,omitempty"`
}

type ProjectGitInitRequest struct {
	RepoPath  string `json:"repo_path,omitempty"`
	RemoteURL string `json:"remote_url,omitempty"`
}

type ProjectGitPushResponse struct {
	Output string `json:"output,omitempty"`
}

type ProjectGitPullResponse struct {
	Output string `json:"output,omitempty"`
}

type ProjectGitInitResponse struct {
	RootFolder string `json:"root_folder"`
	HasGit     bool   `json:"has_git"`
	RemoteURL  string `json:"remote_url,omitempty"`
}

type ProjectFileNameMatch struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

type ProjectContentMatch struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Preview string `json:"preview"`
}

type ProjectSearchResponse struct {
	RootFolder      string                 `json:"root_folder"`
	Query           string                 `json:"query"`
	FileNameMatches []ProjectFileNameMatch `json:"filename_matches"`
	ContentMatches  []ProjectContentMatch  `json:"content_matches"`
}
