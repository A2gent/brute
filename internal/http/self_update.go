package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/A2gent/brute/internal/logging"
)

const (
	selfUpdateGitTimeout     = 15 * time.Second
	selfUpdateFetchTimeout   = 30 * time.Second
	selfUpdatePullTimeout    = 2 * time.Minute
	selfUpdateBuildTimeout   = 10 * time.Minute
	selfUpdateTotalTimeout   = 20 * time.Minute
	selfUpdateFetchInterval  = 2 * time.Minute
	selfUpdateIdlePoll       = 2 * time.Second
	selfUpdateCommandWaitMax = 5 * time.Second
)

type selfUpdateState struct {
	mu             sync.Mutex
	updating       atomic.Bool
	restartPending atomic.Bool
	builtCommit    string
	restartOnce    sync.Once
	restartCh      chan struct{}
	fetchMu        sync.Mutex
	lastFetch      map[string]time.Time
}

var processStartedAt = time.Now()

var startCommit struct {
	once     sync.Once
	commit   string
	modified bool
}

func buildVCSRevision() (commit string, modified bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			commit = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	return commit, modified
}

func captureRunningCommit() (string, bool) {
	startCommit.once.Do(func() {
		startCommit.commit, startCommit.modified = buildVCSRevision()
		if startCommit.commit != "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), selfUpdateGitTimeout)
		defer cancel()
		if dir := bruteSourceDir(ctx); dir != "" {
			startCommit.commit, _ = runInDir(ctx, dir, "git", "rev-parse", "HEAD")
		}
	})
	return startCommit.commit, startCommit.modified
}

var runningCommit = func() string {
	commit, _ := captureRunningCommit()
	return commit
}

func sameCommit(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

func bruteNeedsBuild(running, oldHead, newHead string) bool {
	if running == "" {
		return oldHead != newHead
	}
	return !sameCommit(running, newHead)
}

func (st *selfUpdateState) shouldFetch(dir string, now time.Time) bool {
	if dir == "" || st.updating.Load() {
		return false
	}
	st.fetchMu.Lock()
	defer st.fetchMu.Unlock()
	if last, ok := st.lastFetch[dir]; ok && now.Sub(last) < selfUpdateFetchInterval {
		return false
	}
	if st.lastFetch == nil {
		st.lastFetch = map[string]time.Time{}
	}
	st.lastFetch[dir] = now
	return true
}

type gitRepoStatus struct {
	Present     bool   `json:"present"`
	Commit      string `json:"commit,omitempty"`
	CommitDate  string `json:"commit_date,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Detached    bool   `json:"detached"`
	HasUpstream bool   `json:"has_upstream"`
	Dirty       bool   `json:"dirty"`
	Behind      int    `json:"behind"`
	SourceDir   string `json:"source_dir,omitempty"`
	Error       string `json:"error,omitempty"`
}

type systemVersionResponse struct {
	gitRepoStatus
	RunningCommit   string        `json:"running_commit,omitempty"`
	RunningModified bool          `json:"running_modified,omitempty"`
	StartedAt       string        `json:"started_at"`
	PID             int           `json:"pid"`
	RestartPending  bool          `json:"restart_pending"`
	Updating        bool          `json:"updating"`
	DevReload       bool          `json:"dev_reload"`
	Caesar          gitRepoStatus `json:"caesar"`
}

type selfUpdateRequest struct {
	Component string `json:"component"`
	Restart   *bool  `json:"restart"`
}

type selfUpdateResponse struct {
	Component      string `json:"component"`
	Status         string `json:"status"`
	OldCommit      string `json:"old_commit"`
	NewCommit      string `json:"new_commit"`
	Output         string `json:"output,omitempty"`
	NpmInstall     bool   `json:"npm_install,omitempty"`
	RestartAdvised bool   `json:"restart_advised,omitempty"`
}

func selfUpdateCommandEnv() []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	if strings.TrimSpace(os.Getenv("GIT_SSH_COMMAND")) == "" {
		env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	return env
}

var runInDir = func(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = selfUpdateCommandEnv()
	cmd.WaitDelay = selfUpdateCommandWaitMax
	output, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(output))
	if err != nil {
		if trimmed == "" {
			return "", err
		}
		return trimmed, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, trimmed)
	}
	return trimmed, nil
}

var selfUpdateExecutable = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func (s *Server) restartChannel() chan struct{} {
	s.selfUpdate.restartOnce.Do(func() { s.selfUpdate.restartCh = make(chan struct{}, 1) })
	return s.selfUpdate.restartCh
}

func (s *Server) RestartRequests() <-chan struct{} {
	return s.restartChannel()
}

func devReloadActive() bool {
	return strings.TrimSpace(os.Getenv("AAGENT_DEV_RELOAD_STATUS")) != ""
}

func gitTopLevel(ctx context.Context, dir string) string {
	if dir == "" {
		return ""
	}
	out, err := runInDir(ctx, dir, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func bruteSourceDir(ctx context.Context) string {
	if dir := strings.TrimSpace(os.Getenv("A2GENT_BRUTE_SOURCE_DIR")); dir != "" {
		return dir
	}
	var candidates []string
	if exe, err := selfUpdateExecutable(); err == nil {
		candidates = append(candidates, filepath.Dir(exe))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	for _, candidate := range candidates {
		top := gitTopLevel(ctx, candidate)
		if top != "" && dirExists(filepath.Join(top, "cmd", "aagent")) {
			return top
		}
	}
	return ""
}

func caesarSourceDir(bruteDir string) string {
	if dir := strings.TrimSpace(os.Getenv("A2GENT_CAESAR_SOURCE_DIR")); dir != "" {
		return dir
	}
	if bruteDir == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(bruteDir), "caesar")
}

func parseBehindCount(output string) int {
	n, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func parseDirty(porcelain string) bool {
	return strings.TrimSpace(porcelain) != ""
}

func readGitStatus(ctx context.Context, dir string, fetch bool) gitRepoStatus {
	status := gitRepoStatus{SourceDir: dir}
	if dir == "" || !dirExists(dir) {
		return status
	}
	ctx, cancel := context.WithTimeout(ctx, selfUpdateGitTimeout)
	defer cancel()
	commit, err := runInDir(ctx, dir, "git", "rev-parse", "--short", "HEAD")
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Present = true
	status.Commit = commit
	status.CommitDate, _ = runInDir(ctx, dir, "git", "log", "-1", "--format=%cI")
	status.Branch, _ = runInDir(ctx, dir, "git", "rev-parse", "--abbrev-ref", "HEAD")
	status.Detached = status.Branch == "HEAD"
	porcelain, _ := runInDir(ctx, dir, "git", "status", "--porcelain", "--untracked-files=no")
	status.Dirty = parseDirty(porcelain)
	if _, err := runInDir(ctx, dir, "git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err != nil {
		return status
	}
	status.HasUpstream = true
	if fetch {
		fetchCtx, cancelFetch := context.WithTimeout(context.WithoutCancel(ctx), selfUpdateFetchTimeout)
		if _, err := runInDir(fetchCtx, dir, "git", "fetch", "--quiet"); err != nil {
			status.Error = err.Error()
		}
		cancelFetch()
	}
	if behind, err := runInDir(ctx, dir, "git", "rev-list", "--count", "HEAD..@{u}"); err == nil {
		status.Behind = parseBehindCount(behind)
	}
	return status
}

func (s *Server) handleSystemVersion(w http.ResponseWriter, r *http.Request) {
	fetch := r.URL.Query().Get("fetch") != "0"
	bruteDir := bruteSourceDir(r.Context())
	caesarDir := caesarSourceDir(bruteDir)
	now := time.Now()
	fetchBrute := fetch && s.selfUpdate.shouldFetch(bruteDir, now)
	fetchCaesar := fetch && s.selfUpdate.shouldFetch(caesarDir, now)
	var wg sync.WaitGroup
	var brute, caesar gitRepoStatus
	wg.Add(2)
	go func() { defer wg.Done(); brute = readGitStatus(r.Context(), bruteDir, fetchBrute) }()
	go func() { defer wg.Done(); caesar = readGitStatus(r.Context(), caesarDir, fetchCaesar) }()
	wg.Wait()
	_, modified := captureRunningCommit()
	s.jsonResponse(w, http.StatusOK, systemVersionResponse{
		gitRepoStatus:   brute,
		RunningCommit:   shortCommit(runningCommit()),
		RunningModified: modified,
		StartedAt:       processStartedAt.UTC().Format(time.RFC3339Nano),
		PID:             os.Getpid(),
		RestartPending:  s.selfUpdate.restartPending.Load(),
		Updating:        s.selfUpdate.updating.Load(),
		DevReload:       devReloadActive(),
		Caesar:          caesar,
	})
}

func isJSONContentType(header string) bool {
	mediaType, _, err := mime.ParseMediaType(header)
	return err == nil && mediaType == "application/json"
}

func (s *Server) handleSystemUpdate(w http.ResponseWriter, r *http.Request) {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		s.errorResponse(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var req selfUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if req.Component != "brute" && req.Component != "caesar" {
		s.errorResponse(w, http.StatusBadRequest, `component must be "brute" or "caesar"`)
		return
	}
	if !s.selfUpdate.mu.TryLock() {
		s.errorResponse(w, http.StatusConflict, "Another update is already running")
		return
	}
	defer s.selfUpdate.mu.Unlock()
	s.selfUpdate.updating.Store(true)
	defer s.selfUpdate.updating.Store(false)

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), selfUpdateTotalTimeout)
	defer cancel()
	bruteDir := bruteSourceDir(ctx)
	var (
		result selfUpdateResponse
		err    error
	)
	if req.Component == "brute" {
		result, err = s.updateBrute(ctx, bruteDir, req.Restart == nil || *req.Restart)
	} else {
		result, err = updateCaesar(ctx, caesarSourceDir(bruteDir))
	}
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Update failed:\n"+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, result)
}

func gitPull(ctx context.Context, dir string) (oldCommit, newCommit, output string, err error) {
	if top := gitTopLevel(ctx, dir); top == "" {
		return "", "", "", fmt.Errorf("source directory %q is not a git repository", dir)
	}
	if oldCommit, err = runInDir(ctx, dir, "git", "rev-parse", "HEAD"); err != nil {
		return "", "", "", err
	}
	pullCtx, cancel := context.WithTimeout(ctx, selfUpdatePullTimeout)
	defer cancel()
	if output, err = runInDir(pullCtx, dir, "git", "pull", "--ff-only"); err != nil {
		return oldCommit, oldCommit, output, err
	}
	newCommit, err = runInDir(ctx, dir, "git", "rev-parse", "HEAD")
	return oldCommit, newCommit, output, err
}

func ensureCleanTree(ctx context.Context, component, dir string) error {
	porcelain, err := runInDir(ctx, dir, "git", "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return err
	}
	if parseDirty(porcelain) {
		return fmt.Errorf("%s working tree has uncommitted changes in %s:\n%s", component, dir, porcelain)
	}
	return nil
}

func (s *Server) updateBrute(ctx context.Context, dir string, restart bool) (selfUpdateResponse, error) {
	result := selfUpdateResponse{Component: "brute"}
	if dir == "" {
		return result, fmt.Errorf("brute source directory not found; set A2GENT_BRUTE_SOURCE_DIR")
	}
	if err := ensureCleanTree(ctx, "brute", dir); err != nil {
		return result, err
	}
	running := runningCommit()
	oldCommit, newCommit, output, err := gitPull(ctx, dir)
	result.OldCommit, result.NewCommit, result.Output = shortCommit(oldCommit), shortCommit(newCommit), output
	if running != "" {
		result.OldCommit = shortCommit(running)
	}
	if err != nil {
		return result, err
	}
	switch {
	case !bruteNeedsBuild(running, oldCommit, newCommit):
		result.Status = "up_to_date"
		return result, nil
	case devReloadActive():
		result.Status = "pulled"
		return result, nil
	case s.selfUpdate.restartPending.Load() && sameCommit(s.selfUpdate.builtCommit, newCommit):
		result.Status = "restart_scheduled"
		return result, nil
	}
	buildOutput, err := buildBruteBinary(ctx, dir)
	result.Output = strings.TrimSpace(output + "\n" + buildOutput)
	if err != nil {
		return result, err
	}
	s.selfUpdate.builtCommit = newCommit
	if !restart {
		result.Status = "built"
		return result, nil
	}
	result.Status = "restart_scheduled"
	if s.selfUpdate.restartPending.CompareAndSwap(false, true) {
		go s.restartWhenIdle()
	}
	return result, nil
}

func buildBruteBinary(ctx context.Context, dir string) (string, error) {
	exe, err := selfUpdateExecutable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	tmp := exe + ".update"
	defer os.Remove(tmp)
	buildCtx, cancel := context.WithTimeout(ctx, selfUpdateBuildTimeout)
	defer cancel()
	output, err := runInDir(buildCtx, dir, "go", "build", "-o", tmp, "./cmd/aagent")
	if err != nil {
		return output, err
	}
	if err := os.Rename(tmp, exe); err != nil {
		return output, fmt.Errorf("replace %s: %w", exe, err)
	}
	return output, nil
}

func (s *Server) activeWork() (runs, workers int) {
	s.serialQueueMu.Lock()
	for _, n := range s.serialQueueWorkers {
		workers += n
	}
	s.serialQueueMu.Unlock()
	if s.sessionManager != nil {
		runs = s.sessionManager.ActiveRunCount()
	}
	return runs, workers
}

func (s *Server) restartWhenIdle() {
	for attempt := 0; ; attempt++ {
		runs, workers := s.activeWork()
		if runs == 0 && workers == 0 {
			break
		}
		if attempt%30 == 0 {
			logging.Info("Self-update restart waiting for idle: %d runs, %d queue workers", runs, workers)
		}
		time.Sleep(selfUpdateIdlePoll)
	}
	time.Sleep(500 * time.Millisecond)
	logging.Info("Self-update restart requested")
	select {
	case s.restartChannel() <- struct{}{}:
	default:
	}
}

func changedFilesNeedNpmInstall(files []string) bool {
	for _, f := range files {
		if f == "package.json" || f == "package-lock.json" {
			return true
		}
	}
	return false
}

func changedFilesNeedAppRestart(files []string) bool {
	for _, f := range files {
		if strings.HasPrefix(f, "src-tauri/") {
			return true
		}
	}
	return false
}

func updateCaesar(ctx context.Context, dir string) (selfUpdateResponse, error) {
	result := selfUpdateResponse{Component: "caesar"}
	if dir == "" || !dirExists(dir) {
		return result, fmt.Errorf("caesar source directory not found; set A2GENT_CAESAR_SOURCE_DIR")
	}
	if err := ensureCleanTree(ctx, "caesar", dir); err != nil {
		return result, err
	}
	oldCommit, newCommit, output, err := gitPull(ctx, dir)
	result.OldCommit, result.NewCommit, result.Output = shortCommit(oldCommit), shortCommit(newCommit), output
	if err != nil {
		return result, err
	}
	if oldCommit == newCommit {
		result.Status = "up_to_date"
		return result, nil
	}
	result.Status = "updated"
	diff, err := runInDir(ctx, dir, "git", "diff", "--name-only", oldCommit, newCommit)
	if err != nil {
		return result, err
	}
	files := strings.Split(diff, "\n")
	result.RestartAdvised = changedFilesNeedAppRestart(files)
	if changedFilesNeedNpmInstall(files) {
		result.NpmInstall = true
		commandLine := strings.TrimSpace(os.Getenv("A2GENT_CAESAR_NPM_INSTALL_COMMAND"))
		if commandLine == "" {
			commandLine = "npm ci"
		}
		command, args, err := splitCommandLine(commandLine)
		if err != nil {
			return result, err
		}
		installCtx, cancel := context.WithTimeout(ctx, selfUpdateBuildTimeout)
		defer cancel()
		npmOutput, err := runInDir(installCtx, dir, command, args...)
		result.Output = strings.TrimSpace(output + "\n" + npmOutput)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func shortCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
