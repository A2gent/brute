package session

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/A2gent/brute/internal/storage"
)

func TestSessionWriteLocksRetireWithoutSplittingWaiters(t *testing.T) {
	manager := NewManager(nil)
	var active atomic.Int32
	var overlap atomic.Bool
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				unlock := manager.lockSession("same")
				if active.Add(1) != 1 {
					overlap.Store(true)
				}
				runtime.Gosched()
				active.Add(-1)
				unlock()
			}
		}()
	}
	wg.Wait()
	if overlap.Load() {
		t.Fatal("session writes used different locks concurrently")
	}
	manager.writeLocks.Range(func(key, value interface{}) bool { t.Errorf("idle lock retained: %v", key); return true })
}

func TestManagerDeleteRetiresSessionTreeWriteLocks(t *testing.T) {
	store, err := storage.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := NewManager(store)
	parent, err := manager.Create("build")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateWithParent("build", parent.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Delete(parent.ID); err != nil {
		t.Fatal(err)
	}
	manager.writeLocks.Range(func(key, value interface{}) bool { t.Errorf("deleted lock retained: %v", key); return true })
}
