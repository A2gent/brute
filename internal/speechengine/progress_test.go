package speechengine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyWhisperKitLinePhases(t *testing.T) {
	startProgress("wk-test")
	for _, tc := range []struct {
		line  string
		phase string
	}{
		{"Initializing models...", PhaseLoading},
		{"Model initialization complete:", PhaseTranscribing},
	} {
		applyWhisperKitLine("wk-test", tc.line)
		if p, _ := CurrentProgress("wk-test"); p.Phase != tc.phase {
			t.Fatalf("after %q phase = %q, want %q", tc.line, p.Phase, tc.phase)
		}
	}
	applyWhisperKitLine("wk-test", "[=========                                         ] 18% | Elapsed Time: 1.20 s | Remaining: 5 s")
	if p, _ := CurrentProgress("wk-test"); p.Percent != 18 || !p.Active {
		t.Fatalf("progress = %+v", p)
	}
	finishProgress("wk-test")
	if p, _ := CurrentProgress("wk-test"); p.Active {
		t.Fatal("expected inactive after finish")
	}
}

func TestLineWriterSplitsCarriageReturns(t *testing.T) {
	var got []string
	w := &lineWriter{onLine: func(s string) { got = append(got, s) }}
	_, _ = w.Write([]byte("10%\r20%\rdone\n"))
	if len(got) != 3 || got[1] != "20%" {
		t.Fatalf("got %v", got)
	}
}

func TestCacheSizeCountsOnlyLargestIncompleteAttempt(t *testing.T) {
	root := t.TempDir()
	blobs := filepath.Join(root, "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, size int) {
		if err := os.WriteFile(filepath.Join(blobs, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("done", 100)
	write("hashA.1111.incomplete", 40)
	write("hashA.2222.incomplete", 70) // newer retry of the same blob
	write("hashB.3333.incomplete", 5)
	if got := cacheSize(root); got != 100+70+5 {
		t.Fatalf("cacheSize = %d, want 175", got)
	}
}

func TestSumRepoEntriesPrefersLFSSize(t *testing.T) {
	type entry = struct {
		Type string `json:"type"`
		Size int64  `json:"size"`
		LFS  *struct {
			Size int64 `json:"size"`
		} `json:"lfs"`
	}
	got := sumRepoEntries([]entry{
		{Type: "file", Size: 134, LFS: &struct {
			Size int64 `json:"size"`
		}{Size: 1000}},
		{Type: "file", Size: 20},
		{Type: "directory", Size: 999},
	})
	if got != 1020 {
		t.Fatalf("got %d, want 1020", got)
	}
}

func TestDeleteModelCacheRemovesOnlyModelFolders(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("HF_HUB_CACHE", cache)
	dir := filepath.Join(cache, "models--org--name")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := DeleteModelCache(EngineKokoro, "org/name"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("expected model folder removed")
	}
	if _, err := DeleteModelCache("piper_tts", "x"); err == nil {
		t.Fatal("engines without a downloadable model must be rejected")
	}
	startProgress(EngineKokoro)
	defer finishProgress(EngineKokoro)
	if _, err := DeleteModelCache(EngineKokoro, "org/name"); err != ErrModelCacheBusy {
		t.Fatalf("want ErrModelCacheBusy, got %v", err)
	}
}
