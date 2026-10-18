package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestSerialQueuedProjectSessionsRunOneAtATime(t *testing.T) {
	if got := runQueuedProjectSessions(t, nil, 2); got != 1 {
		t.Fatalf("max concurrent provider requests = %d, want 1", got)
	}
}

func TestSerialQueuedProjectSessionsRunInParallelWhenProjectAllows(t *testing.T) {
	got := runQueuedProjectSessions(t, map[string]string{projectMaxParallelSessionsKey: "2"}, 4)
	if got != 2 {
		t.Fatalf("max concurrent provider requests = %d, want 2", got)
	}
}

func TestParseMaxParallelSessions(t *testing.T) {
	for raw, want := range map[string]int{"": 1, "abc": 1, "0": 1, "-3": 1, "3": 3, " 4 ": 4, "99": maxParallelSessionsLimit} {
		if got := parseMaxParallelSessions(raw); got != want {
			t.Errorf("parseMaxParallelSessions(%q) = %d, want %d", raw, got, want)
		}
	}
}

// runQueuedProjectSessions queues taskCount serial sessions in a project with the given settings,
// waits for them to complete and returns the peak number of concurrent provider requests.
func runQueuedProjectSessions(t *testing.T, settings map[string]string, taskCount int) int {
	t.Helper()
	var mu sync.Mutex
	activeRequests := 0
	maxActiveRequests := 0
	totalRequests := 0
	// Creating a session over HTTP is slower than a fake provider reply, so responses are held
	// until every session is queued; otherwise runs never overlap and the peak is always 1.
	release := make(chan struct{})

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/v1/responses"; got != want {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}

		mu.Lock()
		activeRequests++
		totalRequests++
		callNumber := totalRequests
		if activeRequests > maxActiveRequests {
			maxActiveRequests = activeRequests
		}
		mu.Unlock()

		defer func() {
			mu.Lock()
			activeRequests--
			mu.Unlock()
		}()

		<-release
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done %d\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n", callNumber)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer provider.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	project := &storage.Project{
		ID:        "project-serial-queue",
		Name:      "Serial Queue",
		Settings:  settings,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()
	cfg.ActiveProvider = string(config.ProviderOpenAI)
	cfg.DefaultModel = "test-model"
	cfg.LLMRetries = 1
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{
		APIKey:  "test-key",
		BaseURL: provider.URL + "/v1",
		Model:   "test-model",
	}

	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)
	api := httptest.NewServer(server.router)
	defer api.Close()

	createQueued := func(task string) string {
		t.Helper()
		payload := map[string]interface{}{
			"agent_id":   "build",
			"task":       task,
			"project_id": project.ID,
			"queued":     true,
			"queue_mode": "serial",
		}
		body, _ := json.Marshal(payload)
		resp, err := api.Client().Post(api.URL+"/sessions", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("create queued session: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create queued session status = %d, want 201", resp.StatusCode)
		}
		var created CreateSessionResponse
		if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
			t.Fatalf("decode create response: %v", err)
		}
		return created.ID
	}

	ids := make([]string, 0, taskCount)
	for i := 0; i < taskCount; i++ {
		ids = append(ids, createQueued(fmt.Sprintf("queued task %d", i)))
	}
	time.Sleep(100 * time.Millisecond) // let workers pick up the last queued session
	close(release)

	allCompleted := func() bool {
		for _, id := range ids {
			sess, err := sessionManager.Get(id)
			if err != nil || sess.Status != session.StatusCompleted {
				return false
			}
		}
		return true
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && !allCompleted() {
		time.Sleep(25 * time.Millisecond)
	}
	if !allCompleted() {
		t.Fatalf("queued sessions did not complete")
	}

	mu.Lock()
	defer mu.Unlock()
	if totalRequests != taskCount {
		t.Fatalf("provider requests = %d, want %d", totalRequests, taskCount)
	}
	return maxActiveRequests
}

func TestStartSessionManuallyOverridesSerialQueueAutoStart(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	project := &storage.Project{
		ID:        "project-manual-start",
		Name:      "Manual Start",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()

	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)

	sess, err := sessionManager.CreateQueued("build")
	if err != nil {
		t.Fatalf("create queued session: %v", err)
	}
	projectID := project.ID
	sess.ProjectID = &projectID
	sess.Metadata = map[string]interface{}{
		sessionQueueModeMetadataKey: sessionQueueModeSerial,
		sessionQueueAutoStartKey:    true,
	}
	sess.AddUserMessage("Start me manually")
	if err := sessionManager.Save(sess); err != nil {
		t.Fatalf("save queued session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/start", nil)
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	fresh, err := sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if fresh.Status != session.StatusRunning {
		t.Fatalf("session status = %s, want %s after one manual start", fresh.Status, session.StatusRunning)
	}
	if sessionIsSerialQueuedAutoRun(fresh) {
		t.Fatalf("manual start should clear serial queue auto-start metadata")
	}
	if sessionQueueMode(fresh) != "" {
		t.Fatalf("manual start should clear serial queue mode, got %q", sessionQueueMode(fresh))
	}
}

func TestStartSerialQueuedSessionCreatesWorktreeBeforeRunning(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}
	server, projectID, repoDir := newProjectFileTestServer(t)
	initMindGitTestRepo(t, repoDir)

	project, err := server.store.GetProject(projectID)
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	worktreesDir := t.TempDir()
	project.Settings[projectWorktreesEnabledSettingKey] = "true"
	project.Settings[projectWorktreesDirectorySettingKey] = worktreesDir
	project.Settings[projectWorktreesBaseRefSettingKey] = "master"
	project.Settings[projectWorktreesMaxSlotsSettingKey] = "1"
	if err := server.store.SaveProject(project); err != nil {
		t.Fatalf("save project settings: %v", err)
	}

	sess, err := server.sessionManager.CreateQueued("build")
	if err != nil {
		t.Fatalf("create queued session: %v", err)
	}
	sess.ProjectID = &projectID
	sess.Metadata = map[string]interface{}{
		sessionQueueModeMetadataKey: sessionQueueModeSerial,
		sessionQueueAutoStartKey:    true,
	}
	sess.AddUserMessage("Start me in an isolated worktree")
	if err := server.sessionManager.Save(sess); err != nil {
		t.Fatalf("save queued session: %v", err)
	}

	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions/"+sess.ID+"/start", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	fresh, err := server.sessionManager.Get(sess.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if fresh.Status != session.StatusRunning {
		t.Fatalf("session status = %s, want %s", fresh.Status, session.StatusRunning)
	}
	worktree, err := server.store.GetSessionWorktree(sess.ID)
	if err != nil {
		t.Fatalf("load worktree: %v", err)
	}
	if worktree == nil || worktree.Path == "" {
		t.Fatal("manual start did not create a worktree before running")
	}
}

func TestSerialQueueSkipsPausedQueuedSessions(t *testing.T) {
	var mu sync.Mutex
	totalRequests := 0

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		totalRequests++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer provider.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	project := &storage.Project{
		ID:        "project-paused-queue",
		Name:      "Paused Queue",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.DataPath = t.TempDir()
	cfg.WorkDir = t.TempDir()
	cfg.ActiveProvider = string(config.ProviderOpenAI)
	cfg.DefaultModel = "test-model"
	cfg.LLMRetries = 1
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{
		APIKey:  "test-key",
		BaseURL: provider.URL + "/v1",
		Model:   "test-model",
	}

	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)

	saveQueued := func(task string, paused bool) string {
		t.Helper()
		sess, err := sessionManager.CreateQueued("build")
		if err != nil {
			t.Fatalf("create queued session: %v", err)
		}
		projectID := project.ID
		sess.ProjectID = &projectID
		sess.Metadata = map[string]interface{}{
			sessionQueueModeMetadataKey: sessionQueueModeSerial,
			sessionQueueAutoStartKey:    true,
		}
		if paused {
			setSessionQueuePaused(sess, true)
		}
		sess.AddUserMessage(task)
		if err := sessionManager.Save(sess); err != nil {
			t.Fatalf("save queued session: %v", err)
		}
		return sess.ID
	}

	pausedID := saveQueued("paused queued task", true)
	activeID := saveQueued("active queued task", false)
	server.triggerSerialSessionQueue(project.ID)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		paused, pausedErr := sessionManager.Get(pausedID)
		active, activeErr := sessionManager.Get(activeID)
		if pausedErr == nil && activeErr == nil && paused.Status == session.StatusQueued && active.Status == session.StatusCompleted {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	paused, err := sessionManager.Get(pausedID)
	if err != nil {
		t.Fatalf("load paused session: %v", err)
	}
	active, err := sessionManager.Get(activeID)
	if err != nil {
		t.Fatalf("load active session: %v", err)
	}
	if paused.Status != session.StatusQueued {
		t.Fatalf("paused session status = %s, want %s", paused.Status, session.StatusQueued)
	}
	if active.Status != session.StatusCompleted {
		t.Fatalf("active session status = %s, want %s", active.Status, session.StatusCompleted)
	}

	mu.Lock()
	defer mu.Unlock()
	if totalRequests != 1 {
		t.Fatalf("provider requests = %d, want 1", totalRequests)
	}
}

func TestSerialQueueCanAdvanceAfterInactiveStatuses(t *testing.T) {
	advanceStatuses := []session.Status{
		session.StatusCompleted,
		session.StatusFailed,
		session.StatusPaused,
		session.StatusInputRequired,
		session.StatusWaitingExternal,
	}
	for _, status := range advanceStatuses {
		t.Run(string(status), func(t *testing.T) {
			if !serialQueueCanAdvanceAfterStatus(status) {
				t.Fatalf("serial queue should advance after %s", status)
			}
		})
	}

	blockingStatuses := []session.Status{
		session.StatusQueued,
		session.StatusRunning,
	}
	for _, status := range blockingStatuses {
		t.Run(string(status), func(t *testing.T) {
			if serialQueueCanAdvanceAfterStatus(status) {
				t.Fatalf("serial queue should not advance after %s", status)
			}
		})
	}
}
