package http

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/storage"
)

func TestAdmissionBlockReason(t *testing.T) {
	cfg := concurrencyConfig{
		MaxSessions:   3,
		ProjectLimits: map[string]int{"a": 2},
		Queues:        []sessionQueueConfig{{Name: "browser", Limit: 1}},
	}
	usage := concurrencyUsage{Running: 2, Projects: map[string]int{"a": 2}, Queues: map[string]int{"browser": 1}}
	cases := []struct {
		name     string
		info     sessionSlotInfo
		usage    concurrencyUsage
		resource string
		blocked  bool
	}{
		{"free project", sessionSlotInfo{projectID: "b"}, usage, "", false},
		{"project full", sessionSlotInfo{projectID: "a"}, usage, "", true},
		{"queue full", sessionSlotInfo{projectID: "b", queueName: "browser"}, usage, "", true},
		{"global full", sessionSlotInfo{projectID: "b"}, concurrencyUsage{Running: 3}, "", true},
		{"resources busy", sessionSlotInfo{projectID: "b"}, usage, "CPU busy", true},
		{"child bypasses everything", sessionSlotInfo{child: true, projectID: "a", queueName: "browser"}, concurrencyUsage{Running: 9}, "CPU busy", false},
	}
	for _, tc := range cases {
		if got := admissionBlockReason(tc.info, cfg, tc.usage, tc.resource) != ""; got != tc.blocked {
			t.Errorf("%s: blocked = %v, want %v", tc.name, got, tc.blocked)
		}
	}
}

func TestReserveSessionSlotEnforcesLimitsAndSkipsChildren(t *testing.T) {
	server, manager, projectID := newNamedQueueProjectServer(t, nil)
	if err := server.saveConcurrencyConfig(concurrencyConfig{MaxSessions: 1}); err != nil {
		t.Fatalf("save concurrency: %v", err)
	}
	first := newProjectSession(t, manager, projectID, "")
	second := newProjectSession(t, manager, projectID, "")
	child := newProjectSession(t, manager, projectID, first.ID)

	if !server.reserveSessionSlot(first) {
		t.Fatal("first session was not admitted")
	}
	if server.reserveSessionSlot(second) {
		t.Fatal("second session admitted past the global limit")
	}
	if !server.reserveSessionSlot(child) {
		t.Fatal("child session was blocked by the global limit")
	}
	if got := server.currentConcurrencyUsage().Running; got != 1 {
		t.Fatalf("running = %d, want 1 (children are not counted)", got)
	}
	server.releaseSessionSlot(first.ID)
	if !server.reserveSessionSlot(second) {
		t.Fatal("second session was not admitted after the slot freed")
	}
}

func TestProjectLimitAppliesAcrossProjectSessions(t *testing.T) {
	server, manager, projectID := newNamedQueueProjectServer(t, nil)
	if err := server.saveConcurrencyConfig(concurrencyConfig{ProjectLimits: map[string]int{projectID: 1}}); err != nil {
		t.Fatalf("save concurrency: %v", err)
	}
	first := newProjectSession(t, manager, projectID, "")
	second := newProjectSession(t, manager, projectID, "")
	other := newProjectSession(t, manager, "", "")
	if !server.reserveSessionSlot(first) || server.reserveSessionSlot(second) {
		t.Fatal("project limit 1 did not admit exactly one project session")
	}
	if !server.reserveSessionSlot(other) {
		t.Fatal("session outside the project was blocked by its limit")
	}
}

func TestNamedQueueLimitIsSharedAcrossProjects(t *testing.T) {
	server, manager, projectID := newNamedQueueProjectServer(t, nil)
	if err := server.saveConcurrencyConfig(concurrencyConfig{Queues: []sessionQueueConfig{{Name: "browser", Limit: 1}}}); err != nil {
		t.Fatalf("save concurrency: %v", err)
	}
	first := newProjectSession(t, manager, projectID, "")
	setSessionQueueName(first, "browser")
	second := newProjectSession(t, manager, "other-project", "")
	setSessionQueueName(second, "browser")
	if !server.reserveSessionSlot(first) || server.reserveSessionSlot(second) {
		t.Fatal("queue browser:1 did not admit exactly one session across projects")
	}
}

func TestResourceThresholdHoldsQueuedSessionUntilLoadDrops(t *testing.T) {
	server, manager, projectID := newNamedQueueProjectServer(t, nil)
	st := server.concurrencyState()
	busy := true
	st.sampleMu.Lock()
	st.sampler = func() resourceReading {
		if busy {
			return resourceReading{CPUPercent: 95}
		}
		return resourceReading{CPUPercent: 10}
	}
	st.sampleMu.Unlock()
	server.refreshResourceReading()
	if err := server.saveConcurrencyConfig(concurrencyConfig{MaxCPUPercent: 80}); err != nil {
		t.Fatalf("save concurrency: %v", err)
	}

	sess := newProjectSession(t, manager, projectID, "")
	sess.AddUserMessage("work")
	if server.reserveSessionSlot(sess) {
		t.Fatal("session admitted while CPU is above the threshold")
	}
	if err := server.queueSessionForAdmission(sess); err != nil {
		t.Fatalf("queue session: %v", err)
	}
	if got := waitSessionStatus(t, manager, sess.ID, session.StatusCompleted, 300*time.Millisecond); got != session.StatusQueued {
		t.Fatalf("status under load = %s, want queued", got)
	}

	st.sampleMu.Lock()
	busy = false
	st.sampleMu.Unlock()
	server.refreshResourceReading()
	server.scheduleSessionQueueResume()
	if got := waitSessionStatus(t, manager, sess.ID, session.StatusCompleted, 3*time.Second); got != session.StatusCompleted {
		t.Fatalf("status after load dropped = %s, want completed", got)
	}
	reloaded, err := manager.Get(sess.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if sessionAwaitsAdmission(reloaded) || sessionIsSerialQueuedAutoRun(reloaded) {
		t.Fatal("started session kept its admission queue markers")
	}
}

func TestChatStreamQueuesWhenGlobalLimitIsFull(t *testing.T) {
	server, manager, projectID := newNamedQueueProjectServer(t, nil)
	if err := server.saveConcurrencyConfig(concurrencyConfig{MaxSessions: 1}); err != nil {
		t.Fatalf("save concurrency: %v", err)
	}
	busy := newProjectSession(t, manager, projectID, "")
	if !server.reserveSessionSlot(busy) {
		t.Fatal("blocking session was not admitted")
	}
	chat := newProjectSession(t, manager, projectID, "")
	chat.SetStatus(session.StatusCompleted)
	if err := manager.Save(chat); err != nil {
		t.Fatalf("save chat: %v", err)
	}

	rec := httptest.NewRecorder()
	server.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/sessions/"+chat.ID+"/chat/stream", strings.NewReader(`{"message":"hello"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("chat stream status = %d: %s", rec.Code, rec.Body.String())
	}
	var event ChatStreamEvent
	scanner := bufio.NewScanner(rec.Body)
	if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &event) != nil {
		t.Fatalf("missing stream event: %s", rec.Body.String())
	}
	if event.Type != "done" || event.Status != string(session.StatusQueued) {
		t.Fatalf("stream event = %s/%s, want done/queued", event.Type, event.Status)
	}
	if got := waitSessionStatus(t, manager, chat.ID, session.StatusCompleted, 300*time.Millisecond); got != session.StatusQueued {
		t.Fatalf("chat status while limit is full = %s, want queued", got)
	}

	server.releaseSessionSlot(busy.ID)
	if got := waitSessionStatus(t, manager, chat.ID, session.StatusCompleted, 3*time.Second); got != session.StatusCompleted {
		t.Fatalf("chat status after slot freed = %s, want completed", got)
	}
}

func TestMigrateProjectConcurrencySettingsMergesIntoGlobal(t *testing.T) {
	server, store := newUnifiedAgentsTestServer(t)
	for _, project := range []*storage.Project{
		{ID: "a", Name: "A", Settings: map[string]string{
			projectMaxParallelSessionsKey: "3",
			projectSessionQueuesKey:       `[{"name":"godot","limit":1}]`,
		}},
		{ID: "b", Name: "B", Settings: map[string]string{
			projectSessionQueuesKey: `[{"name":"godot","limit":2},{"name":"images","limit":1}]`,
			"other":                 "kept",
		}},
	} {
		project.CreatedAt, project.UpdatedAt = time.Now(), time.Now()
		if err := store.SaveProject(project); err != nil {
			t.Fatalf("save project: %v", err)
		}
	}

	server.migrateProjectConcurrencySettings()

	cfg := server.loadConcurrencyConfig()
	if cfg.ProjectLimits["a"] != 3 || len(cfg.ProjectLimits) != 1 {
		t.Fatalf("project limits = %v, want a:3", cfg.ProjectLimits)
	}
	if limit, _ := cfg.queueLimit("godot"); limit != 2 {
		t.Fatalf("godot limit = %d, want the larger 2", limit)
	}
	if limit, ok := cfg.queueLimit("images"); !ok || limit != 1 {
		t.Fatalf("images limit = %d/%v, want 1", limit, ok)
	}
	b, err := store.GetProject("b")
	if err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if hasLegacyProjectConcurrency(b) || b.Settings["other"] != "kept" {
		t.Fatalf("project settings after migration = %v", b.Settings)
	}
	server.invalidateConcurrencyConfig()
	if again := server.loadConcurrencyConfig(); len(again.Queues) != 2 {
		t.Fatalf("persisted queues = %v, want 2", again.Queues)
	}
}

func TestParseIoregGPUUtilization(t *testing.T) {
	out := `+-o AGXAcceleratorG14X
    "PerformanceStatistics" = {"Device Utilization %"=37,"Renderer Utilization %"=30}
+-o Other
    "PerformanceStatistics" = {"Device Utilization %"=12}`
	if got, ok := parseIoregGPUUtilization(out); !ok || got != 37 {
		t.Fatalf("gpu utilization = %v/%v, want 37", got, ok)
	}
	if _, ok := parseIoregGPUUtilization("nothing"); ok {
		t.Fatal("parsed utilization from output without accelerators")
	}
}

func newProjectSession(t *testing.T, manager *session.Manager, projectID, parentID string) *session.Session {
	t.Helper()
	var sess *session.Session
	var err error
	if parentID != "" {
		sess, err = manager.CreateWithParent("build", parentID)
	} else {
		sess, err = manager.Create("build")
	}
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if projectID != "" {
		id := projectID
		sess.ProjectID = &id
	}
	if err := manager.Save(sess); err != nil {
		t.Fatalf("save session: %v", err)
	}
	return sess
}

type settingsHookStore struct {
	storage.Store
	onGet func()
}

func (s *settingsHookStore) GetSettings() (map[string]string, error) {
	settings, err := s.Store.GetSettings()
	if s.onGet != nil {
		hook := s.onGet
		s.onGet = nil
		hook()
	}
	return settings, err
}

func TestStaleConcurrencyLoadDoesNotOverwriteNewerConfig(t *testing.T) {
	server, _, _ := newNamedQueueProjectServer(t, nil)
	sqlite := server.store.(*storage.SQLiteStore)
	hooked := &settingsHookStore{Store: sqlite}
	hooked.onGet = func() {
		if err := sqlite.SetSetting(concurrencySettingKey, `{"max_sessions":7}`); err != nil {
			t.Errorf("set setting: %v", err)
		}
		server.invalidateConcurrencyConfig()
	}
	server.store = hooked
	server.invalidateConcurrencyConfig()
	if got := server.loadConcurrencyConfig().MaxSessions; got != 0 {
		t.Fatalf("racing load = %d, want the stale 0", got)
	}
	if hooked.onGet != nil {
		t.Fatal("load did not read settings from the store")
	}
	if got := server.loadConcurrencyConfig().MaxSessions; got != 7 {
		t.Fatalf("load after invalidation = %d, want 7", got)
	}
}

func TestSaveConcurrencyConfigKeepsOtherSettings(t *testing.T) {
	server, _, _ := newNamedQueueProjectServer(t, nil)
	const otherKey = "A2GENT_TOOL_RESULT_COMPRESSION_ENABLED"
	if err := server.store.SaveSettings(map[string]string{otherKey: "true"}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	if err := server.saveConcurrencyConfig(concurrencyConfig{MaxSessions: 2}); err != nil {
		t.Fatalf("save concurrency: %v", err)
	}
	settings, err := server.store.GetSettings()
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if settings[otherKey] != "true" || !strings.Contains(settings[concurrencySettingKey], `"max_sessions":2`) {
		t.Fatalf("settings = %v", settings)
	}
}

func TestStopCancelsSlotWaitWithoutTakingSlot(t *testing.T) {
	server, manager, projectID := newNamedQueueProjectServer(t, nil)
	if err := server.saveConcurrencyConfig(concurrencyConfig{MaxSessions: 1}); err != nil {
		t.Fatalf("save concurrency: %v", err)
	}
	first := newProjectSession(t, manager, projectID, "")
	waiting := newProjectSession(t, manager, projectID, "")
	waiting.SetStatus(session.StatusRunning)
	if !server.reserveSessionSlot(first) {
		t.Fatal("first session was not admitted")
	}
	result := make(chan error, 1)
	go func() {
		release, err := server.waitForSessionSlot(context.Background(), waiting)
		if release != nil {
			release()
		}
		result <- err
	}()
	if got := waitSessionStatus(t, manager, waiting.ID, session.StatusQueued, 2*time.Second); got != session.StatusQueued {
		t.Fatalf("status while waiting = %s, want queued", got)
	}
	if cancelled := server.cancelActiveSessionRuns(waiting.ID); cancelled != 1 {
		t.Fatalf("cancelled = %d, want 1", cancelled)
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wait error = %v, want canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slot wait was not cancelled")
	}
	reloaded, err := manager.Get(waiting.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Status != session.StatusPaused {
		t.Fatalf("status after stop = %s, want paused", reloaded.Status)
	}
	if usage := server.currentConcurrencyUsage(); usage.Running != 1 {
		t.Fatalf("running = %d, want only the first session", usage.Running)
	}
}
