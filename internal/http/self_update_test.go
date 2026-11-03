package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseBehindAndDirty(t *testing.T) {
	if got := parseBehindCount("3\n"); got != 3 {
		t.Fatalf("behind = %d", got)
	}
	if got := parseBehindCount("fatal: no upstream"); got != 0 {
		t.Fatalf("behind on error = %d", got)
	}
	if parseDirty("\n") || !parseDirty(" M main.go\n") {
		t.Fatal("unexpected dirty parsing")
	}
}

func TestChangedFilesDetection(t *testing.T) {
	if !changedFilesNeedNpmInstall([]string{"src/a.ts", "package-lock.json"}) {
		t.Fatal("lockfile change should require npm install")
	}
	if changedFilesNeedNpmInstall([]string{"src/package.json"}) {
		t.Fatal("nested package.json should not trigger install")
	}
	if !changedFilesNeedAppRestart([]string{"src-tauri/src/main.rs"}) || changedFilesNeedAppRestart([]string{"src/App.tsx"}) {
		t.Fatal("unexpected restart detection")
	}
}

type fakeGit struct {
	heads []string
	calls []string
}

func (f *fakeGit) run(ctx context.Context, dir, name string, args ...string) (string, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if name == "go" && len(args) > 2 && args[0] == "build" {
		return "", os.WriteFile(args[2], []byte("new"), 0o755)
	}
	switch call {
	case "git rev-parse --show-toplevel":
		return dir, nil
	case "git rev-parse HEAD":
		head := f.heads[0]
		if len(f.heads) > 1 {
			f.heads = f.heads[1:]
		}
		return head, nil
	case "git diff --name-only aaaaaaaaaa bbbbbbbbbb":
		return "package.json\nsrc-tauri/tauri.conf.json", nil
	}
	return "", nil
}

func stubRunInDir(t *testing.T, fake *fakeGit) {
	old := runInDir
	runInDir = fake.run
	t.Cleanup(func() { runInDir = old })
}

func stubRunningCommit(t *testing.T, commit string) {
	old := runningCommit
	runningCommit = func() string { return commit }
	t.Cleanup(func() { runningCommit = old })
}

func stubExecutable(t *testing.T) string {
	exe := filepath.Join(t.TempDir(), "aagent")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := selfUpdateExecutable
	selfUpdateExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { selfUpdateExecutable = old })
	return exe
}

func hasGoBuild(calls []string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, "go build") {
			return true
		}
	}
	return false
}

func TestBruteNeedsBuild(t *testing.T) {
	cases := []struct {
		running, oldHead, newHead string
		want                      bool
	}{
		{"aaaaaaaaaa", "bbbbbbbbbb", "bbbbbbbbbb", true},
		{"bbbbbbbbbb", "bbbbbbbbbb", "bbbbbbbbbb", false},
		{"bbbbbbb", "aaaaaaaaaa", "bbbbbbbbbb", false},
		{"", "aaaaaaaaaa", "aaaaaaaaaa", false},
		{"", "aaaaaaaaaa", "bbbbbbbbbb", true},
	}
	for _, tc := range cases {
		if got := bruteNeedsBuild(tc.running, tc.oldHead, tc.newHead); got != tc.want {
			t.Errorf("bruteNeedsBuild(%q, %q, %q) = %v, want %v", tc.running, tc.oldHead, tc.newHead, got, tc.want)
		}
	}
}

func TestUpdateBruteRebuildsWhenRunningBinaryIsBehindHead(t *testing.T) {
	fake := &fakeGit{heads: []string{"bbbbbbbbbb"}}
	stubRunInDir(t, fake)
	stubRunningCommit(t, "aaaaaaaaaa")
	t.Setenv("AAGENT_DEV_RELOAD_STATUS", "")
	exe := stubExecutable(t)

	result, err := (&Server{}).updateBrute(context.Background(), t.TempDir(), false)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if result.Status != "built" || result.OldCommit != "aaaaaaa" || result.NewCommit != "bbbbbbb" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !hasGoBuild(fake.calls) {
		t.Fatalf("expected a build after a previously failed one, calls: %v", fake.calls)
	}
	if data, _ := os.ReadFile(exe); string(data) != "new" {
		t.Fatalf("binary not replaced, got %q", data)
	}
}

func TestUpdateBruteUpToDateWhenRunningMatchesHead(t *testing.T) {
	fake := &fakeGit{heads: []string{"aaaaaaaaaa", "bbbbbbbbbb"}}
	stubRunInDir(t, fake)
	stubRunningCommit(t, "bbbbbbbbbb")

	result, err := (&Server{}).updateBrute(context.Background(), t.TempDir(), true)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if result.Status != "up_to_date" || hasGoBuild(fake.calls) {
		t.Fatalf("unexpected result %+v, calls %v", result, fake.calls)
	}
}

func TestShouldFetchIsCachedAndSkippedDuringUpdate(t *testing.T) {
	var st selfUpdateState
	now := time.Now()
	if !st.shouldFetch("/repo", now) {
		t.Fatal("first fetch should run")
	}
	if st.shouldFetch("/repo", now.Add(time.Minute)) {
		t.Fatal("fetch within the interval should be cached")
	}
	if !st.shouldFetch("/repo", now.Add(selfUpdateFetchInterval+time.Second)) {
		t.Fatal("fetch after the interval should run")
	}
	st.updating.Store(true)
	if st.shouldFetch("/other", now) {
		t.Fatal("fetch must be skipped while an update runs")
	}
}

func TestUpdateBruteDevReloadStopsAfterPull(t *testing.T) {
	fake := &fakeGit{heads: []string{"aaaaaaaaaa", "bbbbbbbbbb"}}
	stubRunInDir(t, fake)
	stubRunningCommit(t, "aaaaaaaaaa")
	t.Setenv("AAGENT_DEV_RELOAD_STATUS", "/tmp/status")

	result, err := (&Server{}).updateBrute(context.Background(), t.TempDir(), true)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if result.Status != "pulled" || result.OldCommit != "aaaaaaa" || result.NewCommit != "bbbbbbb" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if hasGoBuild(fake.calls) {
		t.Fatalf("dev reload mode must not build: %v", fake.calls)
	}
}

func TestUpdateBruteRejectsDirtyTree(t *testing.T) {
	old := runInDir
	runInDir = func(ctx context.Context, dir, name string, args ...string) (string, error) {
		if strings.Join(args, " ") == "status --porcelain --untracked-files=no" {
			return " M main.go", nil
		}
		t.Fatalf("unexpected command after dirty check: %s %v", name, args)
		return "", nil
	}
	t.Cleanup(func() { runInDir = old })

	if _, err := (&Server{}).updateBrute(context.Background(), t.TempDir(), true); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("expected dirty error, got %v", err)
	}
}

func TestUpdateCaesarRunsNpmCiOnManifestChange(t *testing.T) {
	fake := &fakeGit{heads: []string{"aaaaaaaaaa", "bbbbbbbbbb"}}
	stubRunInDir(t, fake)

	result, err := updateCaesar(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if !result.NpmInstall || !result.RestartAdvised || result.Status != "updated" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if fake.calls[len(fake.calls)-1] != "npm ci" {
		t.Fatalf("expected npm ci last, got %v", fake.calls)
	}
}

func TestSystemUpdateRejectsConcurrentUpdates(t *testing.T) {
	server := &Server{}
	server.selfUpdate.mu.Lock()
	defer server.selfUpdate.mu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/system/update", strings.NewReader(`{"component":"brute"}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()
	server.handleSystemUpdate(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
}

func TestSystemUpdateRejectsNonJSONContentType(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		req := httptest.NewRequest(http.MethodPost, "/system/update", strings.NewReader(`{"component":"brute"}`))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()
		(&Server{}).handleSystemUpdate(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("Content-Type %q: expected 415, got %d", contentType, rec.Code)
		}
	}
}
