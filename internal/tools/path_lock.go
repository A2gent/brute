package tools

import (
	"path/filepath"
	"sync"
)

type pathLockEntry struct {
	mu   sync.Mutex
	refs int
}

var toolPathLocks = struct {
	sync.Mutex
	byPath map[string]*pathLockEntry
}{byPath: make(map[string]*pathLockEntry)}

// lockToolPath serializes a complete read-modify-write operation for a file,
// including calls made through different editing tool instances.
func lockToolPath(path string) func() {
	absPath, err := filepath.Abs(path)
	if err == nil {
		path = filepath.Clean(absPath)
	} else {
		path = filepath.Clean(path)
	}

	toolPathLocks.Lock()
	entry := toolPathLocks.byPath[path]
	if entry == nil {
		entry = &pathLockEntry{}
		toolPathLocks.byPath[path] = entry
	}
	entry.refs++
	toolPathLocks.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		toolPathLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(toolPathLocks.byPath, path)
		}
		toolPathLocks.Unlock()
	}
}
