package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInsertLinesSerializesConcurrentAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.txt")
	if err := os.WriteFile(path, []byte("start\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const calls = 64
	tool := NewInsertLinesTool("")
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			params, _ := json.Marshal(InsertLinesParams{Path: path, AfterLine: -1, Content: fmt.Sprintf("entry-%d", i)})
			result, err := tool.Execute(context.Background(), params)
			if err != nil {
				errs <- err
			} else if !result.Success {
				errs <- fmt.Errorf("insert failed: %s", result.Error)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) != calls+1 {
		t.Fatalf("expected %d lines without lost updates, got %d: %q", calls+1, len(lines), content)
	}
	seen := make(map[string]bool, calls)
	for _, line := range lines[1:] {
		seen[line] = true
	}
	if len(seen) != calls {
		t.Fatalf("expected %d unique appended lines, got %d", calls, len(seen))
	}
}

func TestLockToolPathNormalizesEquivalentPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.txt")
	unlock := lockToolPath(path)

	started := make(chan struct{})
	acquired := make(chan struct{})
	go func() {
		close(started)
		unlockAlias := lockToolPath(filepath.Join(filepath.Dir(path), ".", filepath.Base(path)))
		close(acquired)
		unlockAlias()
	}()
	<-started
	for {
		toolPathLocks.Lock()
		refs := toolPathLocks.byPath[filepath.Clean(path)].refs
		toolPathLocks.Unlock()
		if refs == 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-acquired:
		t.Fatal("equivalent paths should share a lock")
	default:
	}
	unlock()
	<-acquired
}
