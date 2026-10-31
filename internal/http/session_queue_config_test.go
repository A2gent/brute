package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/speechcache"
	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tools"
)

func TestParseSessionQueues(t *testing.T) {
	queues, err := parseSessionQueues(`[{"name":" godot ","limit":0},{"name":"images","limit":99}]`)
	if err != nil {
		t.Fatalf("parseSessionQueues: %v", err)
	}
	if len(queues) != 2 {
		t.Fatalf("queues = %#v, want 2 entries", queues)
	}
	if queues[0].Name != "godot" || queues[0].Limit != 1 {
		t.Fatalf("queue[0] = %#v, want godot/1", queues[0])
	}
	if queues[1].Limit != maxParallelSessionsLimit {
		t.Fatalf("queue[1].Limit = %d, want %d", queues[1].Limit, maxParallelSessionsLimit)
	}

	for _, raw := range []string{"", "  "} {
		if got, err := parseSessionQueues(raw); err != nil || got != nil {
			t.Fatalf("parseSessionQueues(%q) = %#v, %v; want nil, nil", raw, got, err)
		}
	}
}

func TestParseSessionQueuesRejectsInvalidNames(t *testing.T) {
	cases := map[string]string{
		"not json":      `[{"name":"godot"`,
		"empty name":    `[{"name":"  ","limit":1}]`,
		"slash name":    `[{"name":"godot/ci","limit":1}]`,
		"duplicate":     `[{"name":"godot","limit":1},{"name":"godot","limit":2}]`,
		"too long name": `[{"name":"` + strings.Repeat("a", sessionQueueNameMaxLength+1) + `","limit":1}]`,
	}
	for name, raw := range cases {
		if _, err := parseSessionQueues(raw); err == nil {
			t.Errorf("%s: parseSessionQueues succeeded, want error", name)
		}
	}
}

func TestSerialQueueLimitForNamedQueue(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	project := &storage.Project{
		ID:        "project-named-queues",
		Name:      "Named queues",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Settings: map[string]string{
			projectMaxParallelSessionsKey: "3",
			projectSessionQueuesKey:       `[{"name":"godot","limit":1},{"name":"images","limit":2}]`,
		},
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("save project: %v", err)
	}

	cases := map[string]int{
		project.ID:                              3,
		serialQueueScope(project.ID, ""):        3,
		serialQueueScope(project.ID, "godot"):   1,
		serialQueueScope(project.ID, "images"):  2,
		serialQueueScope(project.ID, "unknown"): 3,
		serialQueueGlobalScope:                  1,
	}
	for scope, want := range cases {
		if got := server.serialQueueLimitForScope(scope); got != want {
			t.Errorf("serialQueueLimitForScope(%q) = %d, want %d", scope, got, want)
		}
	}
}

func TestSerialQueueScopeUsesSessionQueueName(t *testing.T) {
	projectID := "project-scope"
	sess := &session.Session{ProjectID: &projectID}

	if got := serialQueueScopeForSession(sess); got != projectID {
		t.Fatalf("default scope = %q, want %q", got, projectID)
	}

	sess.Metadata = map[string]interface{}{sessionQueueNameKey: "godot"}
	if got := serialQueueScopeForSession(sess); got != projectID+"/godot" {
		t.Fatalf("named scope = %q, want %q", got, projectID+"/godot")
	}

	setSessionQueueName(sess, "")
	if _, ok := sess.Metadata[sessionQueueNameKey]; ok {
		t.Fatal("setSessionQueueName with empty name did not clear metadata")
	}
}

func TestCreateJobRejectsUnknownQueue(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	project := &storage.Project{
		ID:        "project-queue-validation",
		Name:      "Queue validation",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Settings:  map[string]string{projectSessionQueuesKey: `[{"name":"godot","limit":1}]`},
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("save project: %v", err)
	}

	body := `{"name":"Loop","schedule_text":"every hour","task_prompt":"do it","enabled":true,
		"project_id":"project-queue-validation","queue_name":"nope"}`
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create with unknown queue status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Unknown queue") {
		t.Fatalf("create error = %s, want unknown queue message", rec.Body.String())
	}
}

func TestQueueLoopSessionsWaitInNamedQueue(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)

	project := &storage.Project{
		ID:        "project-named-queue-run",
		Name:      "Named queue run",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Settings:  map[string]string{projectSessionQueuesKey: `[{"name":"godot","limit":1}]`},
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("save project: %v", err)
	}

	projectID := project.ID
	queued := make([]*session.Session, 0, 2)
	other := make([]*session.Session, 0, 1)
	for i := 0; i < 2; i++ {
		sess, err := server.sessionManager.CreateQueued("build")
		if err != nil {
			t.Fatalf("create queued session: %v", err)
		}
		sess.ProjectID = &projectID
		sess.Metadata = map[string]interface{}{
			sessionQueueModeMetadataKey: sessionQueueModeSerial,
			sessionQueueAutoStartKey:    true,
		}
		setSessionQueueName(sess, "godot")
		sess.AddUserMessage("godot work")
		if err := server.sessionManager.Save(sess); err != nil {
			t.Fatalf("save queued session: %v", err)
		}
		queued = append(queued, sess)
	}

	defaultQueue, err := server.sessionManager.CreateQueued("build")
	if err != nil {
		t.Fatalf("create default queue session: %v", err)
	}
	defaultQueue.ProjectID = &projectID
	defaultQueue.Metadata = map[string]interface{}{
		sessionQueueModeMetadataKey: sessionQueueModeSerial,
		sessionQueueAutoStartKey:    true,
	}
	defaultQueue.AddUserMessage("default work")
	if err := server.sessionManager.Save(defaultQueue); err != nil {
		t.Fatalf("save default queue session: %v", err)
	}
	other = append(other, defaultQueue)

	named, err := server.serialQueueCandidates(serialQueueScope(projectID, "godot"))
	if err != nil {
		t.Fatalf("named candidates: %v", err)
	}
	if len(named) != 2 {
		t.Fatalf("named queue candidates = %d, want 2", len(named))
	}
	unscoped, err := server.serialQueueCandidates(projectID)
	if err != nil {
		t.Fatalf("default candidates: %v", err)
	}
	if len(unscoped) != 1 || unscoped[0].ID != other[0].ID {
		t.Fatalf("default queue candidates = %#v, want only the unbound session", unscoped)
	}
}

func TestSessionQueueNameInheritedFromParent(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	project := &storage.Project{
		ID:        "project-inherit-queue",
		Name:      "Inherit queue",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Settings:  map[string]string{projectSessionQueuesKey: `[{"name":"godot","limit":1}]`},
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("save project: %v", err)
	}

	projectID := project.ID
	parent, err := server.sessionManager.Create("build")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	parent.ProjectID = &projectID
	parent.Metadata = map[string]interface{}{sessionQueueNameKey: "godot"}
	parent.SetStatus(session.StatusCompleted)
	if err := server.sessionManager.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"agent_id":  "build",
		"parent_id": parent.ID,
		"task":      "follow up",
	})
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(string(body))))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create child status = %d, want %d: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var created CreateSessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode child: %v", err)
	}
	child, err := server.sessionManager.Get(created.ID)
	if err != nil {
		t.Fatalf("load child: %v", err)
	}
	if got := sessionQueueName(child); got != "godot" {
		t.Fatalf("child queue name = %q, want godot", got)
	}
}

func TestRemovedQueueDetachesLoopsFromProject(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	project := &storage.Project{
		ID:        "project-queue-removal",
		Name:      "Queue removal",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Settings:  map[string]string{projectSessionQueuesKey: `[{"name":"godot","limit":1}]`},
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("save project: %v", err)
	}

	projectID := project.ID
	job := &storage.RecurringJob{
		ID:            "loop-queue-removal",
		ProjectID:     &projectID,
		Name:          "Godot loop",
		ScheduleHuman: "every hour",
		ScheduleCron:  "0 * * * *",
		TaskPrompt:    "work",
		QueueSerial:   true,
		QueueName:     "godot",
		Enabled:       true,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save job: %v", err)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"settings": map[string]string{projectSessionQueuesKey: ""},
	})
	req := httptest.NewRequest(http.MethodPut, "/projects/"+projectID, strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update project status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	stored, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if stored.QueueName != "" {
		t.Fatalf("loop queue name = %q, want cleared", stored.QueueName)
	}
}

func TestProjectSettingsRejectInvalidSessionQueues(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	project := &storage.Project{
		ID:        "project-invalid-queues",
		Name:      "Invalid queues",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("save project: %v", err)
	}

	body := `{"settings":{"session_queues":"[{\"name\":\"a/b\",\"limit\":1}]"}}`
	req := httptest.NewRequest(http.MethodPut, "/projects/"+project.ID, strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("update status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// Two loops sharing one named queue must not exceed that queue's limit, while the
// default queue keeps its own, higher limit.
func TestNamedQueueLimitsConcurrentLoopRuns(t *testing.T) {
	var mu sync.Mutex
	active, maxActive, total := 0, 0, 0
	release := make(chan struct{})

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		active++
		total++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			active--
			mu.Unlock()
		}()
		<-release
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":3,"output_tokens":2}}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer provider.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	project := &storage.Project{
		ID:        "project-named-queue-limit",
		Name:      "Named queue limit",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Settings: map[string]string{
			projectMaxParallelSessionsKey: "2",
			projectSessionQueuesKey:       `[{"name":"godot","limit":1}]`,
		},
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
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{APIKey: "test-key", BaseURL: provider.URL + "/v1", Model: "test-model"}

	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)

	projectID := project.ID
	saveQueued := func(task, queueName string) {
		t.Helper()
		sess, err := sessionManager.CreateQueued("build")
		if err != nil {
			t.Fatalf("create queued session: %v", err)
		}
		sess.ProjectID = &projectID
		sess.Metadata = map[string]interface{}{
			sessionQueueModeMetadataKey: sessionQueueModeSerial,
			sessionQueueAutoStartKey:    true,
		}
		setSessionQueueName(sess, queueName)
		sess.AddUserMessage(task)
		if err := sessionManager.Save(sess); err != nil {
			t.Fatalf("save queued session: %v", err)
		}
	}

	saveQueued("godot developer", "godot")
	saveQueued("godot tester", "godot")
	saveQueued("default work", "")

	server.triggerProjectSessionQueues(project)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		started := total
		mu.Unlock()
		if started >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// One godot run plus one default-queue run may be active; a second godot run may not.
	mu.Lock()
	if total != 2 {
		mu.Unlock()
		t.Fatalf("concurrent provider requests = %d, want 2 (one named, one default)", total)
	}
	mu.Unlock()

	close(release)
	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if maxActive > 2 {
		t.Fatalf("max concurrent provider requests = %d, want at most 2", maxActive)
	}
	if total != 3 {
		t.Fatalf("total provider requests = %d, want 3", total)
	}
}

// Sessions left in a scope whose queue was deleted must still start on the fallback
// limit instead of waiting forever in a scope nobody triggers.
func TestQueuedSessionsStartAfterTheirQueueIsDeleted(t *testing.T) {
	server, sessionManager, projectID := newNamedQueueProjectServer(t, map[string]string{
		projectSessionQueuesKey: `[{"name":"godot","limit":1}]`,
	})

	queued := saveNamedQueueSession(t, sessionManager, projectID, "godot")

	body, _ := json.Marshal(map[string]interface{}{
		"settings": map[string]string{projectSessionQueuesKey: ""},
	})
	req := httptest.NewRequest(http.MethodPut, "/projects/"+projectID, strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update project status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	if got := waitSessionStatus(t, sessionManager, queued.ID, session.StatusCompleted, 3*time.Second); got != session.StatusCompleted {
		t.Fatalf("session in removed queue status = %s, want %s", got, session.StatusCompleted)
	}
}

// Changing an unrelated setting resends the queue list, so loop bindings must survive it.
func TestUnrelatedSettingsSaveKeepsLoopQueueBinding(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	queues := `[{"name":"godot","limit":1}]`
	project := &storage.Project{
		ID:        "project-settings-keep-queue",
		Name:      "Keep queue",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Settings:  map[string]string{projectSessionQueuesKey: queues},
	}
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("save project: %v", err)
	}
	projectID := project.ID
	job := &storage.RecurringJob{
		ID: "loop-keep-queue", ProjectID: &projectID, Name: "Godot", ScheduleHuman: "hourly", ScheduleCron: "0 * * * *",
		TaskPrompt: "work", QueueSerial: true, QueueName: "godot", Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("save job: %v", err)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"settings": map[string]string{projectSessionQueuesKey: queues, "A2GENT_PROJECT_WORKTREES_ENABLED": "true"},
	})
	req := httptest.NewRequest(http.MethodPut, "/projects/"+projectID, strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update project status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	stored, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if stored.QueueName != "godot" {
		t.Fatalf("loop queue name = %q, want godot", stored.QueueName)
	}
}

func newNamedQueueServer(t *testing.T, settings map[string]string) (*Server, *session.Manager) {
	t.Helper()
	server, manager, _ := newNamedQueueProjectServer(t, settings)
	return server, manager
}

func newNamedQueueProjectServer(t *testing.T, settings map[string]string) (*Server, *session.Manager, string) {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":3,"output_tokens":2}}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(provider.Close)

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	project := &storage.Project{
		ID:        "project-" + t.Name(),
		Name:      "Named queue project",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Settings:  settings,
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
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{APIKey: "test-key", BaseURL: provider.URL + "/v1", Model: "test-model"}

	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)
	return server, sessionManager, project.ID
}

func saveNamedQueueSession(t *testing.T, manager *session.Manager, projectID, queueName string) *session.Session {
	t.Helper()
	sess, err := manager.CreateQueued("build")
	if err != nil {
		t.Fatalf("create queued session: %v", err)
	}
	id := projectID
	sess.ProjectID = &id
	sess.Metadata = map[string]interface{}{
		sessionQueueModeMetadataKey: sessionQueueModeSerial,
		sessionQueueAutoStartKey:    true,
	}
	setSessionQueueName(sess, queueName)
	sess.AddUserMessage("queued work")
	if err := manager.Save(sess); err != nil {
		t.Fatalf("save queued session: %v", err)
	}
	return sess
}

func waitSessionStatus(t *testing.T, manager *session.Manager, sessionID string, want session.Status, within time.Duration) session.Status {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		sess, err := manager.Get(sessionID)
		if err != nil {
			t.Fatalf("load session: %v", err)
		}
		if sess.Status == want || time.Now().After(deadline) {
			return sess.Status
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// Lowering a queue limit must take effect while the queue still has work: surplus workers
// retire before claiming the next session instead of running out the old limit.
func TestLoweringNamedQueueLimitRetiresSurplusWorkers(t *testing.T) {
	var mu sync.Mutex
	active, maxAfterLowering := 0, 0
	lowered := false
	release := make(chan struct{})
	startedTwo := make(chan struct{})
	var startedOnce sync.Once

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		active++
		if lowered && active > maxAfterLowering {
			maxAfterLowering = active
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			active--
			mu.Unlock()
		}()
		<-release
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":3,"output_tokens":2}}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer provider.Close()

	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	project := &storage.Project{
		ID:        "project-queue-limit-lowering",
		Name:      "Lowering",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Settings: map[string]string{
			projectMaxParallelSessionsKey: "1",
			projectSessionQueuesKey:       `[{"name":"godot","limit":2}]`,
		},
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
	cfg.Providers[string(config.ProviderOpenAI)] = config.Provider{APIKey: "test-key", BaseURL: provider.URL + "/v1", Model: "test-model"}

	sessionManager := session.NewManager(store)
	server := NewServer(cfg, nil, tools.NewManager(cfg.WorkDir), sessionManager, store, speechcache.New(0), 0)
	projectID := project.ID
	for i := 0; i < 4; i++ {
		saveNamedQueueSession(t, sessionManager, projectID, "godot")
	}

	server.triggerProjectSessionQueues(project)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		now := active
		mu.Unlock()
		if now >= 2 {
			startedOnce.Do(func() { close(startedTwo) })
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-startedTwo:
	case <-time.After(time.Second):
		t.Fatal("both workers did not start before the limit was lowered")
	}

	project.Settings[projectSessionQueuesKey] = `[{"name":"godot","limit":1}]`
	if err := store.SaveProject(project); err != nil {
		t.Fatalf("save project: %v", err)
	}
	mu.Lock()
	lowered = true
	mu.Unlock()
	server.triggerProjectSessionQueues(project)

	close(release)
	time.Sleep(700 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if maxAfterLowering > 1 {
		t.Fatalf("concurrent requests after lowering = %d, want at most 1", maxAfterLowering)
	}
}

// A restart must restore queued sessions into their named queue, not the default one.
func TestResumeRestoresNamedQueueSessions(t *testing.T) {
	server, sessionManager, projectID := newNamedQueueProjectServer(t, map[string]string{
		projectMaxParallelSessionsKey: "3",
		projectSessionQueuesKey:       `[{"name":"godot","limit":1}]`,
	})

	first := saveNamedQueueSession(t, sessionManager, projectID, "godot")
	second := saveNamedQueueSession(t, sessionManager, projectID, "godot")
	defaultQueue := saveNamedQueueSession(t, sessionManager, projectID, "")

	server.resumeSerialSessionQueues()

	for _, sess := range []*session.Session{first, second, defaultQueue} {
		if got := waitSessionStatus(t, sessionManager, sess.ID, session.StatusCompleted, 3*time.Second); got != session.StatusCompleted {
			t.Fatalf("resumed session %s status = %s, want %s", sess.ID, got, session.StatusCompleted)
		}
	}
	// The named queue limit is 1, so its two sessions could not overlap.
	reloaded, err := sessionManager.Get(second.ID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if got := sessionQueueName(reloaded); got != "godot" {
		t.Fatalf("resumed session queue name = %q, want godot", got)
	}
}
