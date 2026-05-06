package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm"
	"github.com/A2gent/brute/internal/logging"
)

const mindRootFolderSettingKey = "AAGENT_MY_MIND_ROOT_FOLDER"
const gitCommitProviderSettingKey = "AAGENT_GIT_COMMIT_PROVIDER"
const gitCommitPromptTemplateSettingKey = "AAGENT_GIT_COMMIT_PROMPT_TEMPLATE"
const defaultGitCommitPromptTemplate = "Generate a descriptive Git commit message based on provided files and diffs.\nReturn plain text only (no markdown, no code fences).\nFormat:\n1) First line: imperative summary (max 72 chars).\n2) Blank line.\n3) 2-4 bullet points with specific technical changes.\n\nChanged files:\n{{files}}\n\nDiff snippets:\n{{diffs}}"
const projectSearchMaxResults = 5
const maxProjectEditableFileBytes = 512 * 1024
const maxProjectEditableFileLines = 20000

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
}

type ProjectGitStatusResponse struct {
	RootFolder string                  `json:"root_folder"`
	HasGit     bool                    `json:"has_git"`
	Files      []ProjectGitChangedFile `json:"files"`
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

type ProjectGitFileDiffResponse struct {
	Path    string `json:"path"`
	Preview string `json:"preview"`
}

type ProjectGitBranch struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
	Remote  bool   `json:"remote"`
	Ahead   int    `json:"ahead"`
	Behind  int    `json:"behind"`
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

type ProjectGitCommitMessageResponse struct {
	Message string `json:"message"`
}

type ProjectGitPushRequest struct {
	RepoPath string `json:"repo_path,omitempty"`
}

type ProjectGitPullRequest struct {
	RepoPath string `json:"repo_path,omitempty"`
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

func (s *Server) handleGetMindConfig(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to load settings: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MindConfigResponse{RootFolder: strings.TrimSpace(settings[mindRootFolderSettingKey])})
}

func (s *Server) handleUpdateMindConfig(w http.ResponseWriter, r *http.Request) {
	var req UpdateMindConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	settings, err := s.store.GetSettings()
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to load settings: "+err.Error())
		return
	}
	if settings == nil {
		settings = map[string]string{}
	}

	rootFolder := strings.TrimSpace(req.RootFolder)
	if rootFolder == "" {
		delete(settings, mindRootFolderSettingKey)
	} else {
		resolvedRoot, err := filepath.Abs(rootFolder)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Invalid root folder path")
			return
		}
		info, err := os.Stat(resolvedRoot)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				s.errorResponse(w, http.StatusBadRequest, "Selected root folder does not exist")
				return
			}
			s.errorResponse(w, http.StatusBadRequest, "Failed to access selected root folder: "+err.Error())
			return
		}
		if !info.IsDir() {
			s.errorResponse(w, http.StatusBadRequest, "Selected root path is not a folder")
			return
		}
		settings[mindRootFolderSettingKey] = resolvedRoot
	}

	if err := s.store.SaveSettings(settings); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save settings: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MindConfigResponse{RootFolder: strings.TrimSpace(settings[mindRootFolderSettingKey])})
}

func (s *Server) handleBrowseMindDirectories(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		if homeDir, err := os.UserHomeDir(); err == nil {
			path = homeDir
		} else {
			path = string(os.PathSeparator)
		}
	}

	resolvedPath, err := filepath.Abs(path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid path")
		return
	}

	entries, err := os.ReadDir(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to list directory: "+err.Error())
		return
	}

	respEntries := make([]MindTreeEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		fullPath := filepath.Join(resolvedPath, entry.Name())
		hasChild := directoryHasChildren(fullPath)
		respEntries = append(respEntries, MindTreeEntry{
			Name:     entry.Name(),
			Path:     fullPath,
			Type:     "directory",
			HasChild: hasChild,
		})
	}

	sort.Slice(respEntries, func(i, j int) bool {
		return strings.ToLower(respEntries[i].Name) < strings.ToLower(respEntries[j].Name)
	})

	s.jsonResponse(w, http.StatusOK, MindTreeResponse{
		RootFolder: resolvedPath,
		Path:       resolvedPath,
		Entries:    respEntries,
	})
}

func (s *Server) handleListMindTree(w http.ResponseWriter, r *http.Request) {
	rootFolder, err := s.loadMindRootFolder()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	resolvedPath, normalizedRelPath, err := resolveMindPath(rootFolder, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	entries, err := os.ReadDir(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to list directory: "+err.Error())
		return
	}

	respEntries := make([]MindTreeEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()

		entryRelPath := name
		if normalizedRelPath != "" {
			entryRelPath = filepath.Join(normalizedRelPath, name)
		}

		if entry.IsDir() {
			respEntries = append(respEntries, MindTreeEntry{
				Name:     name,
				Path:     filepath.ToSlash(entryRelPath),
				Type:     "directory",
				HasChild: directoryHasChildren(filepath.Join(resolvedPath, name)),
			})
			continue
		}

		respEntries = append(respEntries, MindTreeEntry{
			Name: name,
			Path: filepath.ToSlash(entryRelPath),
			Type: "file",
		})
	}

	sort.Slice(respEntries, func(i, j int) bool {
		if respEntries[i].Type != respEntries[j].Type {
			return respEntries[i].Type == "directory"
		}
		return strings.ToLower(respEntries[i].Name) < strings.ToLower(respEntries[j].Name)
	})

	s.jsonResponse(w, http.StatusOK, MindTreeResponse{
		RootFolder: rootFolder,
		Path:       filepath.ToSlash(normalizedRelPath),
		Entries:    respEntries,
	})
}

func (s *Server) handleGetMindFile(w http.ResponseWriter, r *http.Request) {
	rootFolder, err := s.loadMindRootFolder()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	resolvedPath, normalizedRelPath, err := resolveMindPath(rootFolder, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "File path is required")
		return
	}
	if !isMarkdownFile(normalizedRelPath) {
		s.errorResponse(w, http.StatusBadRequest, "Only markdown files can be opened")
		return
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to access file: "+err.Error())
		return
	}
	if info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Path is a directory")
		return
	}

	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to read file: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MindFileResponse{
		RootFolder: rootFolder,
		Path:       filepath.ToSlash(normalizedRelPath),
		Content:    string(content),
	})
}

func (s *Server) handleUpsertMindFile(w http.ResponseWriter, r *http.Request) {
	rootFolder, err := s.loadMindRootFolder()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req UpdateMindFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	resolvedPath, normalizedRelPath, err := resolveMindPath(rootFolder, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "File path is required")
		return
	}
	if !isMarkdownFile(normalizedRelPath) {
		s.errorResponse(w, http.StatusBadRequest, "Only markdown files can be created or edited")
		return
	}

	parentDir := filepath.Dir(resolvedPath)
	parentInfo, err := os.Stat(parentDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusBadRequest, "Parent folder does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access parent folder: "+err.Error())
		return
	}
	if !parentInfo.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Parent path is not a folder")
		return
	}

	if info, statErr := os.Stat(resolvedPath); statErr == nil && info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Path is a directory")
		return
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to access file: "+statErr.Error())
		return
	}

	if err := os.WriteFile(resolvedPath, []byte(req.Content), 0o644); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to write file: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MindFileResponse{
		RootFolder: rootFolder,
		Path:       filepath.ToSlash(normalizedRelPath),
		Content:    req.Content,
	})
}

func (s *Server) handleDeleteMindFile(w http.ResponseWriter, r *http.Request) {
	rootFolder, err := s.loadMindRootFolder()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	resolvedPath, normalizedRelPath, err := resolveMindPath(rootFolder, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "File path is required")
		return
	}
	if !isMarkdownFile(normalizedRelPath) {
		s.errorResponse(w, http.StatusBadRequest, "Only markdown files can be deleted")
		return
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusNotFound, "File does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access file: "+err.Error())
		return
	}
	if info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Path is a directory")
		return
	}

	if err := os.Remove(resolvedPath); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete file: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MindFileDeleteResponse{
		RootFolder: rootFolder,
		Path:       filepath.ToSlash(normalizedRelPath),
	})
}

func (s *Server) loadMindRootFolder() (string, error) {
	settings, err := s.store.GetSettings()
	if err != nil {
		return "", errors.New("failed to load settings")
	}

	rootFolder := strings.TrimSpace(settings[mindRootFolderSettingKey])
	if rootFolder == "" {
		return "", errors.New("My Mind root folder is not configured")
	}

	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		return "", errors.New("configured My Mind root folder is invalid")
	}

	info, err := os.Stat(resolvedRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("configured My Mind root folder does not exist")
		}
		return "", errors.New("failed to access configured My Mind root folder")
	}
	if !info.IsDir() {
		return "", errors.New("configured My Mind root path is not a folder")
	}

	return resolvedRoot, nil
}

func resolveMindPath(rootFolder, relPath string) (string, string, error) {
	normalized := filepath.Clean(strings.TrimSpace(relPath))
	if normalized == "." {
		normalized = ""
	}
	if filepath.IsAbs(normalized) {
		return "", "", errors.New("path must be relative to My Mind root")
	}

	resolvedPath := rootFolder
	if normalized != "" {
		resolvedPath = filepath.Join(rootFolder, normalized)
	}
	resolvedPath = filepath.Clean(resolvedPath)

	relToRoot, err := filepath.Rel(rootFolder, resolvedPath)
	if err != nil {
		return "", "", errors.New("invalid path")
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(os.PathSeparator)) {
		return "", "", errors.New("path escapes My Mind root folder")
	}

	if relToRoot == "." {
		relToRoot = ""
	}

	return resolvedPath, relToRoot, nil
}

func isMarkdownFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".markdown"
}

func isProjectEditableFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return !isProjectBlockedMediaFileExtension(ext)
}

func isProjectBlockedMediaFileExtension(ext string) bool {
	switch ext {
	case ".avif", ".bmp", ".gif", ".heic", ".heif", ".ico", ".jpeg", ".jpg", ".png", ".svg", ".svgz", ".tif", ".tiff", ".webp":
		return true
	case ".3g2", ".3gp", ".avi", ".flv", ".m4v", ".mkv", ".mov", ".mp4", ".mpeg", ".mpg", ".ogv", ".webm", ".wmv":
		return true
	default:
		return false
	}
}

func validateProjectFileContent(content []byte, action string) error {
	if len(content) > maxProjectEditableFileBytes {
		return fmt.Errorf("File is too large to %s (max 512 KiB)", action)
	}
	if bytes.Contains(content, []byte{0}) || !utf8.Valid(content) {
		return fmt.Errorf("File must be UTF-8 text to %s", action)
	}
	if countProjectFileLines(content) > maxProjectEditableFileLines {
		return fmt.Errorf("File has too many lines to %s (max 20,000 lines)", action)
	}
	return nil
}

func countProjectFileLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	lineCount := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		lineCount++
	}
	return lineCount
}

func directoryHasChildren(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

func (s *Server) handleMoveMindFile(w http.ResponseWriter, r *http.Request) {
	rootFolder, err := s.loadMindRootFolder()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req MoveMindFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	fromResolved, fromNormalized, err := resolveMindPath(rootFolder, req.FromPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid source path: "+err.Error())
		return
	}
	if fromNormalized == "" {
		s.errorResponse(w, http.StatusBadRequest, "Source path is required")
		return
	}

	fromInfo, err := os.Stat(fromResolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusNotFound, "Source does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access source: "+err.Error())
		return
	}

	isDir := fromInfo.IsDir()
	if !isDir && !isMarkdownFile(fromNormalized) {
		s.errorResponse(w, http.StatusBadRequest, "Only markdown files and folders can be moved")
		return
	}

	toResolved, toNormalized, err := resolveMindPath(rootFolder, req.ToPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid destination path: "+err.Error())
		return
	}
	if toNormalized == "" {
		s.errorResponse(w, http.StatusBadRequest, "Destination path is required")
		return
	}

	if !isDir && !isMarkdownFile(toNormalized) {
		s.errorResponse(w, http.StatusBadRequest, "Destination must be a markdown file")
		return
	}

	if fromResolved == toResolved {
		s.errorResponse(w, http.StatusBadRequest, "Source and destination paths are the same")
		return
	}

	if isDir && strings.HasPrefix(toResolved+string(os.PathSeparator), fromResolved+string(os.PathSeparator)) {
		s.errorResponse(w, http.StatusBadRequest, "Cannot move a folder into itself")
		return
	}

	toParentDir := filepath.Dir(toResolved)
	toParentInfo, err := os.Stat(toParentDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusBadRequest, "Destination folder does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access destination folder: "+err.Error())
		return
	}
	if !toParentInfo.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Destination parent path is not a folder")
		return
	}

	if _, err := os.Stat(toResolved); err == nil {
		s.errorResponse(w, http.StatusConflict, "A file or folder already exists at the destination path")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to check destination: "+err.Error())
		return
	}

	if err := os.Rename(fromResolved, toResolved); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to move: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MoveMindFileResponse{
		RootFolder: rootFolder,
		FromPath:   filepath.ToSlash(fromNormalized),
		ToPath:     filepath.ToSlash(toNormalized),
	})
}

func (s *Server) handleCreateMindFolder(w http.ResponseWriter, r *http.Request) {
	rootFolder, err := s.loadMindRootFolder()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req CreateFolderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	resolvedPath, normalizedRelPath, err := resolveMindPath(rootFolder, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "Folder path is required")
		return
	}

	if _, err := os.Stat(resolvedPath); err == nil {
		s.errorResponse(w, http.StatusConflict, "A file or folder already exists at this path")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to check path: "+err.Error())
		return
	}

	if err := os.MkdirAll(resolvedPath, 0o755); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create folder: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, CreateFolderResponse{
		RootFolder: rootFolder,
		Path:       filepath.ToSlash(normalizedRelPath),
	})
}

func (s *Server) handleRenameMindEntry(w http.ResponseWriter, r *http.Request) {
	rootFolder, err := s.loadMindRootFolder()
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req RenameEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	oldResolved, oldNormalized, err := resolveMindPath(rootFolder, req.OldPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid path: "+err.Error())
		return
	}
	if oldNormalized == "" {
		s.errorResponse(w, http.StatusBadRequest, "Path is required")
		return
	}

	newName := strings.TrimSpace(req.NewName)
	if newName == "" {
		s.errorResponse(w, http.StatusBadRequest, "New name is required")
		return
	}
	if strings.ContainsAny(newName, "/\\") {
		s.errorResponse(w, http.StatusBadRequest, "Name cannot contain path separators")
		return
	}

	info, err := os.Stat(oldResolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusNotFound, "File or folder does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access path: "+err.Error())
		return
	}

	if !info.IsDir() && !isMarkdownFile(newName) {
		s.errorResponse(w, http.StatusBadRequest, "File must have .md or .markdown extension")
		return
	}

	parentDir := filepath.Dir(oldResolved)
	newResolved := filepath.Join(parentDir, newName)

	if oldResolved == newResolved {
		s.errorResponse(w, http.StatusBadRequest, "New name is the same as the old name")
		return
	}

	if _, err := os.Stat(newResolved); err == nil {
		s.errorResponse(w, http.StatusConflict, "A file or folder with this name already exists")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to check new path: "+err.Error())
		return
	}

	if err := os.Rename(oldResolved, newResolved); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to rename: "+err.Error())
		return
	}

	newRelPath, err := filepath.Rel(rootFolder, newResolved)
	if err != nil {
		newRelPath = newName
	}

	s.jsonResponse(w, http.StatusOK, RenameEntryResponse{
		RootFolder: rootFolder,
		OldPath:    filepath.ToSlash(oldNormalized),
		NewPath:    filepath.ToSlash(newRelPath),
	})
}

// handleListProjectTree lists files and folders in a project's folder
func (s *Server) handleListProjectTree(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectID")
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project folder is not configured")
		return
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}
	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is invalid")
		return
	}

	info, err := os.Stat(resolvedRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusBadRequest, "Project folder does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access project folder: "+err.Error())
		return
	}
	if !info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is not a directory")
		return
	}

	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	resolvedPath, normalizedRelPath, err := resolveMindPath(resolvedRoot, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	entries, err := os.ReadDir(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to list directory: "+err.Error())
		return
	}

	respEntries := make([]MindTreeEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()

		entryRelPath := name
		if normalizedRelPath != "" {
			entryRelPath = filepath.Join(normalizedRelPath, name)
		}

		if entry.IsDir() {
			respEntries = append(respEntries, MindTreeEntry{
				Name:     name,
				Path:     filepath.ToSlash(entryRelPath),
				Type:     "directory",
				HasChild: directoryHasChildren(filepath.Join(resolvedPath, name)),
			})
			continue
		}

		respEntries = append(respEntries, MindTreeEntry{
			Name: name,
			Path: filepath.ToSlash(entryRelPath),
			Type: "file",
		})
	}

	sort.Slice(respEntries, func(i, j int) bool {
		if respEntries[i].Type != respEntries[j].Type {
			return respEntries[i].Type == "directory"
		}
		return strings.ToLower(respEntries[i].Name) < strings.ToLower(respEntries[j].Name)
	})

	s.jsonResponse(w, http.StatusOK, MindTreeResponse{
		RootFolder: resolvedRoot,
		Path:       filepath.ToSlash(normalizedRelPath),
		Entries:    respEntries,
	})
}

func (s *Server) handleProjectSearch(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("query"))
	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if query == "" {
		s.jsonResponse(w, http.StatusOK, ProjectSearchResponse{
			RootFolder:      resolvedRoot,
			Query:           "",
			FileNameMatches: []ProjectFileNameMatch{},
			ContentMatches:  []ProjectContentMatch{},
		})
		return
	}

	fileNameCandidates := make([]rankedFileNameMatch, 0, 32)
	contentCandidates := make([]rankedContentMatch, 0, 32)
	queryLower := strings.ToLower(query)

	walkErr := filepath.WalkDir(resolvedRoot, func(fullPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}

		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			if entry.IsDir() && fullPath != resolvedRoot {
				return filepath.SkipDir
			}
			if !entry.IsDir() {
				return nil
			}
		}

		if entry.IsDir() {
			return nil
		}

		if !isMarkdownFile(name) {
			return nil
		}

		relPath, relErr := filepath.Rel(resolvedRoot, fullPath)
		if relErr != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)

		if score := scoreProjectFileNameMatch(relPath, queryLower); score > 0 {
			fileNameCandidates = append(fileNameCandidates, rankedFileNameMatch{
				ProjectFileNameMatch: ProjectFileNameMatch{
					Path: relPath,
					Name: name,
				},
				score: score,
			})
		}

		content, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			return nil
		}
		lineNumber, lineText, score := findProjectContentMatch(content, queryLower)
		if score > 0 {
			contentCandidates = append(contentCandidates, rankedContentMatch{
				ProjectContentMatch: ProjectContentMatch{
					Path:    relPath,
					Line:    lineNumber,
					Preview: truncateText(strings.TrimSpace(lineText), 220),
				},
				score: score,
			})
		}

		return nil
	})
	if walkErr != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to search project files: "+walkErr.Error())
		return
	}

	sort.Slice(fileNameCandidates, func(i, j int) bool {
		if fileNameCandidates[i].score != fileNameCandidates[j].score {
			return fileNameCandidates[i].score > fileNameCandidates[j].score
		}
		return fileNameCandidates[i].Path < fileNameCandidates[j].Path
	})
	sort.Slice(contentCandidates, func(i, j int) bool {
		if contentCandidates[i].score != contentCandidates[j].score {
			return contentCandidates[i].score > contentCandidates[j].score
		}
		return contentCandidates[i].Path < contentCandidates[j].Path
	})

	fileNameMatches := make([]ProjectFileNameMatch, 0, projectSearchMaxResults)
	for _, candidate := range fileNameCandidates {
		fileNameMatches = append(fileNameMatches, candidate.ProjectFileNameMatch)
		if len(fileNameMatches) >= projectSearchMaxResults {
			break
		}
	}

	contentMatches := make([]ProjectContentMatch, 0, projectSearchMaxResults)
	for _, candidate := range contentCandidates {
		contentMatches = append(contentMatches, candidate.ProjectContentMatch)
		if len(contentMatches) >= projectSearchMaxResults {
			break
		}
	}

	s.jsonResponse(w, http.StatusOK, ProjectSearchResponse{
		RootFolder:      resolvedRoot,
		Query:           query,
		FileNameMatches: fileNameMatches,
		ContentMatches:  contentMatches,
	})
}

// handleGetProjectFile retrieves a file from a project's folder
func (s *Server) handleGetProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectID")
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project folder is not configured")
		return
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}
	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is invalid")
		return
	}

	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	resolvedPath, normalizedRelPath, err := resolveMindPath(resolvedRoot, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "File path is required")
		return
	}
	if !isProjectEditableFile(normalizedRelPath) {
		s.errorResponse(w, http.StatusBadRequest, "Images and videos cannot be opened in the project editor")
		return
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to access file: "+err.Error())
		return
	}
	if info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Path is a directory")
		return
	}
	if info.Size() > maxProjectEditableFileBytes {
		s.errorResponse(w, http.StatusBadRequest, "File is too large to open (max 512 KiB)")
		return
	}

	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to read file: "+err.Error())
		return
	}
	if err := validateProjectFileContent(content, "open"); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MindFileResponse{
		RootFolder: resolvedRoot,
		Path:       filepath.ToSlash(normalizedRelPath),
		Content:    string(content),
	})
}

// handleUpsertProjectFile creates or updates a file in a project's folder
func (s *Server) handleUpsertProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectID")
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project folder is not configured")
		return
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}
	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is invalid")
		return
	}

	var req UpdateMindFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	resolvedPath, normalizedRelPath, err := resolveMindPath(resolvedRoot, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "File path is required")
		return
	}
	if !isProjectEditableFile(normalizedRelPath) {
		s.errorResponse(w, http.StatusBadRequest, "Images and videos cannot be created or edited in the project editor")
		return
	}
	if err := validateProjectFileContent([]byte(req.Content), "create or edit"); err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	parentDir := filepath.Dir(resolvedPath)
	parentInfo, err := os.Stat(parentDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusBadRequest, "Parent folder does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access parent folder: "+err.Error())
		return
	}
	if !parentInfo.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Parent path is not a folder")
		return
	}

	if info, statErr := os.Stat(resolvedPath); statErr == nil && info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Path is a directory")
		return
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to access file: "+statErr.Error())
		return
	}

	if err := os.WriteFile(resolvedPath, []byte(req.Content), 0o644); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to write file: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MindFileResponse{
		RootFolder: resolvedRoot,
		Path:       filepath.ToSlash(normalizedRelPath),
		Content:    req.Content,
	})
}

// handleDeleteProjectFile deletes a file from a project's folder
func (s *Server) handleDeleteProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectID")
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project folder is not configured")
		return
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}
	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is invalid")
		return
	}

	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	resolvedPath, normalizedRelPath, err := resolveMindPath(resolvedRoot, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "File path is required")
		return
	}
	if !isProjectEditableFile(normalizedRelPath) {
		s.errorResponse(w, http.StatusBadRequest, "Images and videos cannot be deleted from the project editor")
		return
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusNotFound, "File does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access file: "+err.Error())
		return
	}
	if info.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Path is a directory")
		return
	}

	if err := os.Remove(resolvedPath); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete file: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MindFileDeleteResponse{
		RootFolder: resolvedRoot,
		Path:       filepath.ToSlash(normalizedRelPath),
	})
}

// handleMoveProjectFile moves a file or folder within a project's folder
func (s *Server) handleMoveProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectID")
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project folder is not configured")
		return
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}
	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is invalid")
		return
	}

	var req MoveMindFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	fromResolved, fromNormalized, err := resolveMindPath(resolvedRoot, req.FromPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid source path: "+err.Error())
		return
	}
	if fromNormalized == "" {
		s.errorResponse(w, http.StatusBadRequest, "Source path is required")
		return
	}

	fromInfo, err := os.Stat(fromResolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusNotFound, "Source does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access source: "+err.Error())
		return
	}

	isDir := fromInfo.IsDir()
	if !isDir && !isProjectEditableFile(fromNormalized) {
		s.errorResponse(w, http.StatusBadRequest, "Images and videos cannot be moved from the project editor")
		return
	}

	toResolved, toNormalized, err := resolveMindPath(resolvedRoot, req.ToPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid destination path: "+err.Error())
		return
	}
	if toNormalized == "" {
		s.errorResponse(w, http.StatusBadRequest, "Destination path is required")
		return
	}

	if !isDir && !isProjectEditableFile(toNormalized) {
		s.errorResponse(w, http.StatusBadRequest, "Destination cannot be an image or video file")
		return
	}

	if fromResolved == toResolved {
		s.errorResponse(w, http.StatusBadRequest, "Source and destination paths are the same")
		return
	}

	if isDir && strings.HasPrefix(toResolved+string(os.PathSeparator), fromResolved+string(os.PathSeparator)) {
		s.errorResponse(w, http.StatusBadRequest, "Cannot move a folder into itself")
		return
	}

	toParentDir := filepath.Dir(toResolved)
	toParentInfo, err := os.Stat(toParentDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusBadRequest, "Destination folder does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access destination folder: "+err.Error())
		return
	}
	if !toParentInfo.IsDir() {
		s.errorResponse(w, http.StatusBadRequest, "Destination parent path is not a folder")
		return
	}

	if _, err := os.Stat(toResolved); err == nil {
		s.errorResponse(w, http.StatusConflict, "A file or folder already exists at the destination path")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to check destination: "+err.Error())
		return
	}

	if err := os.Rename(fromResolved, toResolved); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to move: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, MoveMindFileResponse{
		RootFolder: resolvedRoot,
		FromPath:   filepath.ToSlash(fromNormalized),
		ToPath:     filepath.ToSlash(toNormalized),
	})
}

func (s *Server) handleCreateProjectFolder(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectID")
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project folder is not configured")
		return
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}
	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is invalid")
		return
	}

	var req CreateFolderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	resolvedPath, normalizedRelPath, err := resolveMindPath(resolvedRoot, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if normalizedRelPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "Folder path is required")
		return
	}

	if _, err := os.Stat(resolvedPath); err == nil {
		s.errorResponse(w, http.StatusConflict, "A file or folder already exists at this path")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to check path: "+err.Error())
		return
	}

	if err := os.MkdirAll(resolvedPath, 0o755); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create folder: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, CreateFolderResponse{
		RootFolder: resolvedRoot,
		Path:       filepath.ToSlash(normalizedRelPath),
	})
}

func (s *Server) handleRenameProjectEntry(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectID")
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project not found")
		return
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		s.errorResponse(w, http.StatusBadRequest, "Project folder is not configured")
		return
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}
	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Project folder path is invalid")
		return
	}

	var req RenameEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	oldResolved, oldNormalized, err := resolveMindPath(resolvedRoot, req.OldPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid path: "+err.Error())
		return
	}
	if oldNormalized == "" {
		s.errorResponse(w, http.StatusBadRequest, "Path is required")
		return
	}

	newName := strings.TrimSpace(req.NewName)
	if newName == "" {
		s.errorResponse(w, http.StatusBadRequest, "New name is required")
		return
	}
	if strings.ContainsAny(newName, "/\\") {
		s.errorResponse(w, http.StatusBadRequest, "Name cannot contain path separators")
		return
	}

	info, err := os.Stat(oldResolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.errorResponse(w, http.StatusNotFound, "File or folder does not exist")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to access path: "+err.Error())
		return
	}

	if !info.IsDir() && !isProjectEditableFile(newName) {
		s.errorResponse(w, http.StatusBadRequest, "File cannot be renamed to an image or video extension")
		return
	}

	parentDir := filepath.Dir(oldResolved)
	newResolved := filepath.Join(parentDir, newName)

	if oldResolved == newResolved {
		s.errorResponse(w, http.StatusBadRequest, "New name is the same as the old name")
		return
	}

	if _, err := os.Stat(newResolved); err == nil {
		s.errorResponse(w, http.StatusConflict, "A file or folder with this name already exists")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		s.errorResponse(w, http.StatusBadRequest, "Failed to check new path: "+err.Error())
		return
	}

	if err := os.Rename(oldResolved, newResolved); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to rename: "+err.Error())
		return
	}

	newRelPath, err := filepath.Rel(resolvedRoot, newResolved)
	if err != nil {
		newRelPath = newName
	}

	s.jsonResponse(w, http.StatusOK, RenameEntryResponse{
		RootFolder: resolvedRoot,
		OldPath:    filepath.ToSlash(oldNormalized),
		NewPath:    filepath.ToSlash(newRelPath),
	})
}

func (s *Server) handleProjectGitStatus(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	repoPath := strings.TrimSpace(r.URL.Query().Get("repoPath"))
	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, repoPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	if !projectHasGitMetadata(targetRepoRoot) {
		s.jsonResponse(w, http.StatusOK, ProjectGitStatusResponse{
			RootFolder: targetRepoRoot,
			HasGit:     false,
			Files:      []ProjectGitChangedFile{},
		})
		return
	}

	porcelainOutput, err := runGitCommandPreserveLeading(targetRepoRoot, "status", "--porcelain=v1")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read git status: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitStatusResponse{
		RootFolder: targetRepoRoot,
		HasGit:     true,
		Files:      parseGitPorcelain(porcelainOutput),
	})
}

func (s *Server) handleProjectGitCommit(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitCommitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	message := strings.TrimSpace(req.Message)
	if message == "" {
		s.errorResponse(w, http.StatusBadRequest, "Commit message is required")
		return
	}

	porcelainOutput, err := runGitCommandPreserveLeading(targetRepoRoot, "status", "--porcelain=v1")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read git status: "+err.Error())
		return
	}
	changedFiles := parseGitPorcelain(porcelainOutput)
	if len(changedFiles) == 0 {
		s.errorResponse(w, http.StatusConflict, "No changed files")
		return
	}

	stagedOutput, err := runGitCommand(targetRepoRoot, "diff", "--cached", "--name-only")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to inspect staged files: "+err.Error())
		return
	}
	stagedFiles := splitNonEmptyLines(stagedOutput)
	if len(stagedFiles) == 0 {
		s.errorResponse(w, http.StatusConflict, "No staged files to commit")
		return
	}

	if _, err := runGitCommand(targetRepoRoot, "commit", "-m", message); err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "nothing to commit") || strings.Contains(lower, "no changes added to commit") {
			s.errorResponse(w, http.StatusConflict, "No staged files to commit")
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to create commit: "+err.Error())
		return
	}

	commit, err := runGitCommand(targetRepoRoot, "rev-parse", "--short", "HEAD")
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Commit created, but failed to read commit hash: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitCommitResponse{
		RootFolder:     targetRepoRoot,
		Commit:         strings.TrimSpace(commit),
		FilesCommitted: len(stagedFiles),
	})
}

func (s *Server) handleProjectGitStageFile(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	normalizedPath, err := resolveGitRepoFilePath(targetRepoRoot, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	if _, err := runGitCommand(targetRepoRoot, "add", "--", normalizedPath); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to stage file: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleProjectGitStageAllFiles(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	// Stage all tracked/untracked changes, including deletions.
	if _, err := runGitCommand(targetRepoRoot, "add", "--all"); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to stage all files: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleProjectGitUnstageFile(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	normalizedPath, err := resolveGitRepoFilePath(targetRepoRoot, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	if _, err := runGitCommand(targetRepoRoot, "restore", "--staged", "--", normalizedPath); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to unstage file: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleProjectGitDiscardFile(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	normalizedPath, err := resolveGitRepoFilePath(targetRepoRoot, req.Path)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	statusOutput, statusErr := runGitCommandPreserveLeading(targetRepoRoot, "status", "--porcelain=v1", "--", normalizedPath)
	if statusErr != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read file git status: "+statusErr.Error())
		return
	}
	statusFiles := parseGitPorcelain(statusOutput)
	var fileStatus *ProjectGitChangedFile
	for i := range statusFiles {
		if filepath.ToSlash(strings.TrimSpace(statusFiles[i].Path)) == filepath.ToSlash(normalizedPath) {
			fileStatus = &statusFiles[i]
			break
		}
	}
	if fileStatus == nil {
		s.errorResponse(w, http.StatusConflict, "File has no changes to discard")
		return
	}

	fullPath := filepath.Join(targetRepoRoot, filepath.FromSlash(normalizedPath))
	if fileStatus.Untracked || fileStatus.IndexStatus == "A" {
		if _, rmErr := runGitCommand(targetRepoRoot, "rm", "--cached", "--ignore-unmatch", "--", normalizedPath); rmErr != nil {
			logging.Warn("git rm --cached ignore-unmatch failed for discard path %s: %v", normalizedPath, rmErr)
		}
		if removeErr := os.Remove(fullPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			s.errorResponse(w, http.StatusBadRequest, "Failed to discard file: "+removeErr.Error())
			return
		}
		s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	if _, restoreErr := runGitCommand(targetRepoRoot, "restore", "--staged", "--worktree", "--", normalizedPath); restoreErr != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to discard file changes: "+restoreErr.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleProjectGitFileDiff(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}
	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if relPath == "" {
		s.errorResponse(w, http.StatusBadRequest, "path is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(r.URL.Query().Get("repoPath")))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	normalizedPath, err := resolveGitRepoFilePath(targetRepoRoot, relPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	preview, err := buildGitFileDiffPreview(targetRepoRoot, normalizedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to load file diff: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitFileDiffResponse{
		Path:    normalizedPath,
		Preview: preview,
	})
}

func (s *Server) handleProjectGitHistory(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(r.URL.Query().Get("repoPath")))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	limit := 120
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsedLimit, parseErr := strconv.Atoi(rawLimit)
		if parseErr != nil || parsedLimit <= 0 {
			s.errorResponse(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if parsedLimit > 500 {
			parsedLimit = 500
		}
		limit = parsedLimit
	}

	currentBranch := ""
	if value, branchErr := runGitCommand(targetRepoRoot, "rev-parse", "--abbrev-ref", "HEAD"); branchErr == nil {
		currentBranch = strings.TrimSpace(value)
	}

	branches, err := buildProjectGitBranches(targetRepoRoot, currentBranch)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read git branches: "+err.Error())
		return
	}

	commitsOutput, err := runGitCommandPreserveLeading(
		targetRepoRoot,
		"log",
		"--decorate=short",
		"--date=iso-strict",
		"--pretty=format:%H%x1f%h%x1f%s%x1f%an%x1f%aI%x1f%D%x1f%P",
		"-n",
		strconv.Itoa(limit),
	)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read git history: "+err.Error())
		return
	}

	commits := parseProjectGitHistoryCommits(commitsOutput, currentBranch)
	s.jsonResponse(w, http.StatusOK, ProjectGitHistoryResponse{
		RootFolder:    targetRepoRoot,
		CurrentBranch: currentBranch,
		Branches:      branches,
		Commits:       commits,
	})
}

func (s *Server) handleProjectGitCommitFiles(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	commitHash := strings.TrimSpace(r.URL.Query().Get("commit"))
	if commitHash == "" {
		s.errorResponse(w, http.StatusBadRequest, "commit is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(r.URL.Query().Get("repoPath")))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	statusOutput, err := runGitCommandPreserveLeading(targetRepoRoot, "diff-tree", "--no-commit-id", "--name-status", "-r", "--root", commitHash)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read commit files: "+err.Error())
		return
	}
	statuses := parseProjectGitCommitFileStatuses(statusOutput)

	statsOutput, err := runGitCommandPreserveLeading(targetRepoRoot, "show", "--numstat", "--format=", "--no-color", commitHash)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read commit file stats: "+err.Error())
		return
	}
	files := mergeProjectGitCommitFiles(statuses, statsOutput)

	s.jsonResponse(w, http.StatusOK, ProjectGitCommitFilesResponse{
		Commit: commitHash,
		Files:  files,
	})
}

func (s *Server) handleProjectGitCommitDiff(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	commitHash := strings.TrimSpace(r.URL.Query().Get("commit"))
	if commitHash == "" {
		s.errorResponse(w, http.StatusBadRequest, "commit is required")
		return
	}

	pathParam := strings.TrimSpace(r.URL.Query().Get("path"))
	if pathParam == "" {
		s.errorResponse(w, http.StatusBadRequest, "path is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(r.URL.Query().Get("repoPath")))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	normalizedPath, err := resolveGitRepoFilePath(targetRepoRoot, pathParam)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	preview, err := runGitCommandPreserveLeading(targetRepoRoot, "show", "--no-color", "--pretty=format:", commitHash, "--", normalizedPath)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read commit diff: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitCommitDiffResponse{
		Commit:  commitHash,
		Path:    normalizedPath,
		Preview: preview,
	})
}

func (s *Server) handleProjectGitCommitMessageSuggestion(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitCommitMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	porcelainOutput, err := runGitCommandPreserveLeading(targetRepoRoot, "status", "--porcelain=v1")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to read git status: "+err.Error())
		return
	}
	files := parseGitPorcelain(porcelainOutput)
	if len(files) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	targetFiles := make([]ProjectGitChangedFile, 0, len(files))
	for _, file := range files {
		if file.Staged {
			targetFiles = append(targetFiles, file)
		}
	}
	if len(targetFiles) == 0 {
		targetFiles = files
	}
	fallbackMessage := buildFallbackCommitMessage(targetFiles)

	diffSections := make([]string, 0, len(targetFiles))
	for _, file := range targetFiles {
		preview, previewErr := buildGitFileDiffPreview(targetRepoRoot, file.Path)
		if previewErr != nil {
			continue
		}
		diffSections = append(diffSections, fmt.Sprintf("File: %s\n%s", file.Path, truncateText(preview, 1600)))
	}
	if len(diffSections) == 0 {
		if fallbackMessage == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.jsonResponse(w, http.StatusOK, ProjectGitCommitMessageResponse{Message: fallbackMessage})
		return
	}

	fileList := make([]string, 0, len(targetFiles))
	for _, file := range targetFiles {
		fileList = append(fileList, fmt.Sprintf("- %s (%s)", file.Path, file.Status))
	}

	settings, settingsErr := s.store.GetSettings()
	if settingsErr != nil {
		logging.Warn("Failed to load settings for git commit generation: %v", settingsErr)
		settings = map[string]string{}
	}
	template := strings.TrimSpace(settings[gitCommitPromptTemplateSettingKey])
	if template == "" {
		template = defaultGitCommitPromptTemplate
	}
	prompt := buildGitCommitPrompt(template, strings.Join(fileList, "\n"), strings.Join(diffSections, "\n\n"))

	providerRef := strings.TrimSpace(settings[gitCommitProviderSettingKey])
	if providerRef == "" {
		providerRef = s.config.ActiveProvider
	}
	configuredProviderType := config.ProviderType(config.NormalizeProviderRef(providerRef))
	activeProviderType := config.ProviderType(config.NormalizeProviderRef(s.config.ActiveProvider))

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	response, err := s.generateGitCommitMessageWithProvider(ctx, configuredProviderType, prompt)
	if err != nil && configuredProviderType != activeProviderType {
		logging.Warn("Commit message generation failed with configured provider %s: %v. Retrying active provider %s", configuredProviderType, err, activeProviderType)
		response, err = s.generateGitCommitMessageWithProvider(ctx, activeProviderType, prompt)
	}
	if err != nil {
		logging.Warn("Commit message generation failed: %v", err)
		if fallbackMessage == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.jsonResponse(w, http.StatusOK, ProjectGitCommitMessageResponse{Message: fallbackMessage})
		return
	}

	message := sanitizeGeneratedCommitMessage(response.Content)
	if message == "" {
		if fallbackMessage == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.jsonResponse(w, http.StatusOK, ProjectGitCommitMessageResponse{Message: fallbackMessage})
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitCommitMessageResponse{Message: message})
}

func (s *Server) generateGitCommitMessageWithProvider(ctx context.Context, providerType config.ProviderType, prompt string) (*llm.ChatResponse, error) {
	model := s.resolveModelForProvider(providerType)
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
		MaxTokens:   220,
	})
}

func (s *Server) handleProjectGitPush(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitPushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	output, err := runGitCommand(targetRepoRoot, "push")
	if err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "no upstream branch") || strings.Contains(lower, "has no upstream branch") {
			branchName, branchErr := runGitCommand(targetRepoRoot, "rev-parse", "--abbrev-ref", "HEAD")
			if branchErr != nil {
				s.errorResponse(w, http.StatusBadRequest, "Push failed (no upstream), and current branch could not be detected: "+branchErr.Error())
				return
			}
			branch := strings.TrimSpace(branchName)
			if branch == "" || branch == "HEAD" {
				s.errorResponse(w, http.StatusBadRequest, "Push failed: no upstream branch and current branch is detached")
				return
			}
			output, err = runGitCommand(targetRepoRoot, "push", "--set-upstream", "origin", branch)
			if err != nil {
				s.errorResponse(w, http.StatusBadRequest, "Failed to push with upstream setup: "+err.Error())
				return
			}
			s.jsonResponse(w, http.StatusOK, ProjectGitPushResponse{Output: output})
			return
		}
		s.errorResponse(w, http.StatusBadRequest, "Failed to push: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitPushResponse{Output: output})
}

func (s *Server) handleProjectGitPull(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitPullRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if !projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusBadRequest, "Target folder does not contain a .git directory")
		return
	}

	strategy := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("strategy")))
	args := []string{"pull"}
	switch strategy {
	case "", "auto":
		// default git pull strategy (respect repo config)
	case "rebase":
		args = append(args, "--rebase")
	case "ff-only":
		args = append(args, "--ff-only")
	case "merge":
		args = append(args, "--no-rebase")
	default:
		s.errorResponse(w, http.StatusBadRequest, "Unsupported pull strategy")
		return
	}

	output, err := runGitCommand(targetRepoRoot, args...)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to pull: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitPullResponse{Output: output})
}

func (s *Server) handleProjectGitInit(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.URL.Query().Get("projectID"))
	if projectID == "" {
		s.errorResponse(w, http.StatusBadRequest, "projectID is required")
		return
	}

	resolvedRoot, err := s.resolveProjectRootFolder(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}

	var req ProjectGitInitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	targetRepoRoot, err := resolveProjectGitTargetRoot(resolvedRoot, strings.TrimSpace(req.RepoPath))
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	if projectHasGitMetadata(targetRepoRoot) {
		s.errorResponse(w, http.StatusConflict, "Target folder is already a Git repository")
		return
	}

	if _, err := runGitCommand(targetRepoRoot, "init"); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to initialize git repository: "+err.Error())
		return
	}
	// Ensure first push can auto-establish upstream for new branches.
	if _, err := runGitCommand(targetRepoRoot, "config", "push.autoSetupRemote", "true"); err != nil {
		logging.Warn("Failed to set push.autoSetupRemote for %s: %v", targetRepoRoot, err)
	}

	remoteURL := strings.TrimSpace(req.RemoteURL)
	if remoteURL != "" {
		if _, err := runGitCommand(targetRepoRoot, "remote", "add", "origin", remoteURL); err != nil {
			s.errorResponse(w, http.StatusBadRequest, "Repository initialized, but failed to add remote origin: "+err.Error())
			return
		}
	}

	s.jsonResponse(w, http.StatusOK, ProjectGitInitResponse{
		RootFolder: targetRepoRoot,
		HasGit:     true,
		RemoteURL:  remoteURL,
	})
}

func (s *Server) resolveProjectRootFolder(projectID string) (string, error) {
	project, err := s.store.GetProject(projectID)
	if err != nil {
		return "", errors.New("project not found")
	}

	if project.Folder == nil || strings.TrimSpace(*project.Folder) == "" {
		return "", errors.New("project folder is not configured")
	}

	rootFolder := strings.TrimSpace(*project.Folder)
	if !filepath.IsAbs(rootFolder) {
		rootFolder = filepath.Join(".", rootFolder)
	}

	resolvedRoot, err := filepath.Abs(rootFolder)
	if err != nil {
		return "", errors.New("project folder path is invalid")
	}

	info, err := os.Stat(resolvedRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("project folder does not exist")
		}
		return "", fmt.Errorf("failed to access project folder: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("project folder path is not a directory")
	}

	return resolvedRoot, nil
}

func projectHasGitMetadata(projectRoot string) bool {
	_, err := os.Stat(filepath.Join(projectRoot, ".git"))
	return err == nil
}

func resolveProjectGitTargetRoot(projectRoot string, repoPath string) (string, error) {
	trimmedPath := strings.TrimSpace(repoPath)
	if trimmedPath == "" {
		return projectRoot, nil
	}

	resolvedPath, _, err := resolveMindPath(projectRoot, trimmedPath)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("target repo path does not exist")
		}
		return "", fmt.Errorf("failed to access target repo path: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("target repo path is not a directory")
	}

	return resolvedPath, nil
}

func runGitCommand(projectRoot string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", projectRoot}, args...)
	cmd := exec.Command("git", commandArgs...)
	output, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(output))
	if err != nil {
		if trimmed == "" {
			trimmed = err.Error()
		}
		return "", errors.New(trimmed)
	}
	return trimmed, nil
}

func runGitCommandPreserveLeading(projectRoot string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", projectRoot}, args...)
	cmd := exec.Command("git", commandArgs...)
	output, err := cmd.CombinedOutput()
	text := strings.TrimRight(string(output), "\r\n")
	if err != nil {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			trimmed = err.Error()
		}
		return "", errors.New(trimmed)
	}
	return text, nil
}

func runGitCommandWithExitCode(projectRoot string, args ...string) (string, int, error) {
	commandArgs := append([]string{"-C", projectRoot}, args...)
	cmd := exec.Command("git", commandArgs...)
	output, err := cmd.CombinedOutput()
	trimmed := strings.TrimRight(string(output), "\r\n")
	if err == nil {
		return trimmed, 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return trimmed, exitErr.ExitCode(), err
	}
	return trimmed, -1, err
}

func resolveGitRepoFilePath(repoRoot string, relPath string) (string, error) {
	normalizedPath := filepath.Clean(strings.TrimSpace(relPath))
	if normalizedPath == "" || normalizedPath == "." {
		return "", errors.New("file path is required")
	}
	if filepath.IsAbs(normalizedPath) {
		return "", errors.New("file path must be relative")
	}

	resolvedPath := filepath.Clean(filepath.Join(repoRoot, normalizedPath))
	relToRoot, err := filepath.Rel(repoRoot, resolvedPath)
	if err != nil {
		return "", errors.New("invalid file path")
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(os.PathSeparator)) {
		return "", errors.New("file path escapes repository root")
	}

	return filepath.ToSlash(relToRoot), nil
}

func buildGitFileDiffPreview(repoRoot string, relPath string) (string, error) {
	normalizedPath, err := resolveGitRepoFilePath(repoRoot, relPath)
	if err != nil {
		return "", err
	}

	sections := make([]string, 0, 3)

	if stagedDiff, diffErr := runGitCommand(repoRoot, "diff", "--no-color", "--cached", "--", normalizedPath); diffErr == nil && strings.TrimSpace(stagedDiff) != "" {
		sections = append(sections, "Staged changes:\n"+stagedDiff)
	}
	if unstagedDiff, diffErr := runGitCommand(repoRoot, "diff", "--no-color", "--", normalizedPath); diffErr == nil && strings.TrimSpace(unstagedDiff) != "" {
		sections = append(sections, "Unstaged changes:\n"+unstagedDiff)
	}

	if len(sections) == 0 && isGitFileUntracked(repoRoot, normalizedPath) {
		newFilePreview := buildUntrackedFilePreview(repoRoot, normalizedPath)
		if strings.TrimSpace(newFilePreview) != "" {
			sections = append(sections, "Untracked file preview:\n"+newFilePreview)
		}
	}

	if len(sections) == 0 {
		return "No diff available for this file.", nil
	}
	return truncateText(strings.Join(sections, "\n\n"), 12000), nil
}

func isGitFileUntracked(repoRoot string, relPath string) bool {
	output, _, err := runGitCommandWithExitCode(repoRoot, "status", "--porcelain=v1", "--", relPath)
	if err != nil && strings.TrimSpace(output) == "" {
		return false
	}
	files := parseGitPorcelain(output)
	for _, file := range files {
		if filepath.ToSlash(strings.TrimSpace(file.Path)) == filepath.ToSlash(strings.TrimSpace(relPath)) {
			return file.Untracked
		}
	}
	return false
}

func buildUntrackedFilePreview(repoRoot string, relPath string) string {
	fullPath := filepath.Join(repoRoot, filepath.FromSlash(relPath))
	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		return ""
	}
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return ""
	}

	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	maxLines := 140
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	for i, line := range lines {
		lines[i] = "+" + line
	}
	return strings.Join(lines, "\n")
}

func buildProjectGitBranches(repoRoot string, currentBranch string) ([]ProjectGitBranch, error) {
	output, err := runGitCommandPreserveLeading(
		repoRoot,
		"for-each-ref",
		"--format=%(refname:short)%x1f%(HEAD)%x1f%(upstream:track)%x1f%(refname)",
		"refs/heads",
		"refs/remotes",
	)
	if err != nil {
		return nil, err
	}

	branches := make([]ProjectGitBranch, 0)
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			continue
		}

		parts := strings.Split(trimmedLine, "\x1f")
		if len(parts) < 4 {
			continue
		}

		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}

		refname := strings.TrimSpace(parts[3])
		remote := strings.HasPrefix(refname, "refs/remotes/")
		if remote && strings.HasSuffix(name, "/HEAD") {
			continue
		}

		ahead, behind := parseGitAheadBehind(parts[2])
		current := strings.TrimSpace(parts[1]) == "*" || (!remote && name == currentBranch)

		branches = append(branches, ProjectGitBranch{
			Name:    name,
			Current: current,
			Remote:  remote,
			Ahead:   ahead,
			Behind:  behind,
		})
	}

	sort.SliceStable(branches, func(i, j int) bool {
		left := branches[i]
		right := branches[j]
		if left.Current != right.Current {
			return left.Current
		}
		if left.Remote != right.Remote {
			return !left.Remote
		}
		return left.Name < right.Name
	})

	return branches, nil
}

func parseGitAheadBehind(track string) (int, int) {
	trimmed := strings.TrimSpace(track)
	if trimmed == "" {
		return 0, 0
	}

	trimmed = strings.TrimPrefix(trimmed, "[")
	trimmed = strings.TrimSuffix(trimmed, "]")

	ahead := 0
	behind := 0
	parts := strings.Split(trimmed, ",")
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if strings.HasPrefix(item, "ahead ") {
			if value, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(item, "ahead "))); err == nil {
				ahead = value
			}
		}
		if strings.HasPrefix(item, "behind ") {
			if value, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(item, "behind "))); err == nil {
				behind = value
			}
		}
	}

	return ahead, behind
}

func parseProjectGitHistoryCommits(output string, currentBranch string) []ProjectGitHistoryCommit {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	commits := make([]ProjectGitHistoryCommit, 0, len(lines))

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			continue
		}
		parts := strings.Split(trimmedLine, "\x1f")
		if len(parts) < 7 {
			continue
		}

		refs := parseProjectGitDecorations(parts[5])
		branch := pickProjectGitPrimaryRef(refs, currentBranch)

		parents := make([]string, 0)
		for _, parent := range strings.Fields(strings.TrimSpace(parts[6])) {
			if parent != "" {
				parents = append(parents, parent)
			}
		}

		commits = append(commits, ProjectGitHistoryCommit{
			Hash:       strings.TrimSpace(parts[0]),
			ShortHash:  strings.TrimSpace(parts[1]),
			Subject:    strings.TrimSpace(parts[2]),
			AuthorName: strings.TrimSpace(parts[3]),
			AuthoredAt: strings.TrimSpace(parts[4]),
			Refs:       refs,
			Parents:    parents,
			Branch:     branch,
		})
	}

	return commits
}

func parseProjectGitDecorations(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	segments := strings.Split(trimmed, ",")
	refs := make([]string, 0, len(segments))
	for _, segment := range segments {
		part := strings.TrimSpace(segment)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "HEAD -> ") {
			branch := strings.TrimSpace(strings.TrimPrefix(part, "HEAD -> "))
			if branch != "" {
				refs = append(refs, branch)
			}
			continue
		}
		if strings.HasPrefix(part, "tag: ") {
			tagName := strings.TrimSpace(strings.TrimPrefix(part, "tag: "))
			if tagName != "" {
				refs = append(refs, tagName)
			}
			continue
		}
		refs = append(refs, part)
	}

	return refs
}

func pickProjectGitPrimaryRef(refs []string, currentBranch string) string {
	trimmedCurrent := strings.TrimSpace(currentBranch)
	if trimmedCurrent != "" {
		for _, ref := range refs {
			if strings.TrimSpace(ref) == trimmedCurrent {
				return trimmedCurrent
			}
		}
	}
	for _, ref := range refs {
		trimmedRef := strings.TrimSpace(ref)
		if trimmedRef == "" {
			continue
		}
		if strings.HasPrefix(trimmedRef, "origin/") || strings.Contains(trimmedRef, "/") {
			return trimmedRef
		}
		return trimmedRef
	}
	return ""
}

func parseProjectGitCommitFileStatuses(output string) map[string]string {
	statuses := make(map[string]string)
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			continue
		}
		parts := strings.Split(trimmedLine, "\t")
		if len(parts) < 2 {
			continue
		}
		status := strings.TrimSpace(parts[0])
		if status == "" {
			continue
		}
		path := strings.TrimSpace(parts[len(parts)-1])
		if path == "" {
			continue
		}
		path = decodeGitPath(path)
		if path == "" {
			continue
		}
		statuses[path] = status
	}
	return statuses
}

func mergeProjectGitCommitFiles(statuses map[string]string, statsOutput string) []ProjectGitCommitFile {
	merged := make(map[string]*ProjectGitCommitFile, len(statuses))

	for path, status := range statuses {
		merged[path] = &ProjectGitCommitFile{
			Path:   path,
			Status: status,
		}
	}

	lines := strings.Split(statsOutput, "\n")
	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			continue
		}
		parts := strings.Split(trimmedLine, "\t")
		if len(parts) < 3 {
			continue
		}

		path := strings.TrimSpace(parts[2])
		if path == "" {
			continue
		}
		path = normalizeGitNumstatPath(path)
		if path == "" {
			continue
		}

		file := merged[path]
		if file == nil {
			file = &ProjectGitCommitFile{
				Path:   path,
				Status: "M",
			}
			merged[path] = file
		}

		additionsRaw := strings.TrimSpace(parts[0])
		deletionsRaw := strings.TrimSpace(parts[1])
		if additionsRaw == "-" || deletionsRaw == "-" {
			file.Binary = true
			file.Additions = 0
			file.Deletions = 0
			continue
		}
		if additions, err := strconv.Atoi(additionsRaw); err == nil {
			file.Additions = additions
		}
		if deletions, err := strconv.Atoi(deletionsRaw); err == nil {
			file.Deletions = deletions
		}
	}

	files := make([]ProjectGitCommitFile, 0, len(merged))
	for _, file := range merged {
		files = append(files, *file)
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files
}

func normalizeGitNumstatPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "=>") {
		parts := strings.Split(trimmed, "=>")
		if len(parts) > 1 {
			candidate := strings.TrimSpace(parts[len(parts)-1])
			candidate = strings.TrimPrefix(candidate, "{")
			candidate = strings.TrimSuffix(candidate, "}")
			candidate = strings.TrimSpace(candidate)
			if candidate != "" {
				return decodeGitPath(candidate)
			}
		}
	}
	return decodeGitPath(trimmed)
}

type rankedFileNameMatch struct {
	ProjectFileNameMatch
	score int
}

type rankedContentMatch struct {
	ProjectContentMatch
	score int
}

func scoreProjectFileNameMatch(relPath string, queryLower string) int {
	baseName := strings.ToLower(filepath.Base(relPath))
	pathLower := strings.ToLower(relPath)

	score := 0
	switch {
	case baseName == queryLower:
		score = 120
	case strings.HasPrefix(baseName, queryLower):
		score = 100
	case strings.Contains(baseName, queryLower):
		score = 80
	case strings.HasPrefix(pathLower, queryLower):
		score = 70
	case strings.Contains(pathLower, queryLower):
		score = 50
	default:
		return 0
	}

	if pathLower == queryLower {
		score += 30
	}
	if len(relPath) <= 40 {
		score += 10
	}
	if len(relPath) <= 16 {
		score += 6
	}

	return score
}

func findProjectContentMatch(content []byte, queryLower string) (int, string, int) {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lowerLine := strings.ToLower(line)
		matchIndex := strings.Index(lowerLine, queryLower)
		if matchIndex < 0 {
			continue
		}
		lineScore := 70 - index
		if lineScore < 12 {
			lineScore = 12
		}
		colScore := 20 - matchIndex
		if colScore < 0 {
			colScore = 0
		}
		return index + 1, line, lineScore + colScore
	}
	return 0, "", 0
}

func sanitizeGeneratedCommitMessage(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	trimmed = strings.Trim(trimmed, "\"'`")
	trimmed = strings.ReplaceAll(trimmed, "\r\n", "\n")
	lines := strings.Split(trimmed, "\n")
	filteredLines := make([]string, 0, len(lines))
	for _, line := range lines {
		candidate := strings.TrimSpace(strings.Trim(line, "\"'`"))
		if candidate == "" {
			if len(filteredLines) > 0 && filteredLines[len(filteredLines)-1] == "" {
				continue
			}
			filteredLines = append(filteredLines, "")
			continue
		}
		if candidate == "```" {
			continue
		}
		filteredLines = append(filteredLines, candidate)
	}
	for len(filteredLines) > 0 && filteredLines[0] == "" {
		filteredLines = filteredLines[1:]
	}
	for len(filteredLines) > 0 && filteredLines[len(filteredLines)-1] == "" {
		filteredLines = filteredLines[:len(filteredLines)-1]
	}
	if len(filteredLines) == 0 {
		return ""
	}
	message := strings.Join(filteredLines, "\n")

	lowered := strings.ToLower(strings.TrimSpace(message))
	if strings.HasPrefix(lowered, "commit message:") {
		message = strings.TrimSpace(message[len("commit message:"):])
		lowered = strings.ToLower(strings.TrimSpace(message))
	}
	if strings.HasPrefix(lowered, "message:") {
		message = strings.TrimSpace(message[len("message:"):])
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	return truncateText(message, 480)
}

func buildGitCommitPrompt(template string, files string, diffs string) string {
	prompt := template
	prompt = strings.ReplaceAll(prompt, "{{files}}", strings.TrimSpace(files))
	prompt = strings.ReplaceAll(prompt, "{{diffs}}", strings.TrimSpace(diffs))
	return strings.TrimSpace(prompt)
}

func buildFallbackCommitMessage(files []ProjectGitChangedFile) string {
	if len(files) == 0 {
		return ""
	}
	if len(files) == 1 {
		return fmt.Sprintf("Update %s", files[0].Path)
	}

	paths := make([]string, 0, len(files))
	for _, file := range files {
		if strings.TrimSpace(file.Path) == "" {
			continue
		}
		paths = append(paths, file.Path)
	}
	if len(paths) == 0 {
		return fmt.Sprintf("Update %d files", len(files))
	}
	if len(paths) == 2 {
		return fmt.Sprintf("Update %s and %s", paths[0], paths[1])
	}
	return fmt.Sprintf("Update %d files (%s, %s, ...)", len(paths), paths[0], paths[1])
}

func truncateText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}

func splitNonEmptyLines(raw string) []string {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func parseGitPorcelain(output string) []ProjectGitChangedFile {
	lines := strings.Split(output, "\n")
	files := make([]ProjectGitChangedFile, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || len(line) < 3 {
			continue
		}

		statusCode := line[:2]
		pathStart := 3
		// Be tolerant if a caller accidentally trimmed leading whitespace in a " M path" line.
		// In that case git line may look like "M path", where path starts at index 2.
		if len(line) > 2 && statusCode[1] == ' ' && line[2] != ' ' {
			pathStart = 2
		}
		if len(line) <= pathStart {
			continue
		}
		pathPart := strings.TrimSpace(line[pathStart:])
		if strings.Contains(pathPart, " -> ") {
			parts := strings.SplitN(pathPart, " -> ", 2)
			pathPart = strings.TrimSpace(parts[1])
		}
		if pathPart == "" {
			continue
		}
		pathPart = decodeGitPath(pathPart)

		indexStatus := string(statusCode[0])
		worktreeStatus := string(statusCode[1])
		if pathStart == 2 && statusCode[1] == ' ' {
			// Reconstruct original unstaged-only status from a trimmed " M path" line.
			indexStatus = " "
			worktreeStatus = string(statusCode[0])
		}
		untracked := statusCode == "??"
		staged := !untracked && indexStatus != " "

		files = append(files, ProjectGitChangedFile{
			Path:           pathPart,
			Status:         strings.TrimSpace(statusCode),
			IndexStatus:    indexStatus,
			WorktreeStatus: worktreeStatus,
			Staged:         staged,
			Untracked:      untracked,
		})
	}
	return files
}

func decodeGitPath(pathPart string) string {
	trimmed := strings.TrimSpace(pathPart)
	if trimmed == "" {
		return trimmed
	}

	// Git may return paths in C-style quoted format with octal escapes,
	// e.g. "00-\320\230...". Decode those so UI gets readable UTF-8 names.
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") {
		if decoded, err := strconv.Unquote(trimmed); err == nil {
			return decoded
		}
	}

	// Fallback for edge cases where git produced escapes without outer quotes.
	if strings.Contains(trimmed, "\\") {
		quoted := "\"" + strings.ReplaceAll(trimmed, "\"", "\\\"") + "\""
		if decoded, err := strconv.Unquote(quoted); err == nil {
			return decoded
		}
	}

	return trimmed
}
