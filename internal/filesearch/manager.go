package filesearch

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ManagerOptions configures the shared per-project search cache.
type ManagerOptions struct {
	IndexOptions   Options
	MaxMemoryBytes int64
	StaleAfter     time.Duration
}

// Manager keeps immutable project indexes in memory and rebuilds stale indexes
// in the background. This keeps UI search fast after the first build while
// avoiding file-system watchers and their per-project file descriptor cost.
type Manager struct {
	mu          sync.Mutex
	entries     map[string]*managedIndex
	options     ManagerOptions
	cachedBytes int64
}

var ErrIndexingDisabled = errors.New("file indexing is disabled")

type managedIndex struct {
	root       string
	idx        *Index
	lastAccess time.Time
	building   bool
	wait       chan struct{}
	buildErr   error
}

var defaultManager = NewManager(ManagerOptions{})

// DefaultManager returns the process-wide cache used by Brute HTTP and tools.
func DefaultManager() *Manager {
	return defaultManager
}

// NewManager creates an independent project-index cache.
func NewManager(opts ManagerOptions) *Manager {
	if opts.MaxMemoryBytes <= 0 {
		opts.MaxMemoryBytes = DefaultMaxMemoryBytes
	}
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = DefaultStaleAfter
	}
	opts.IndexOptions = normalizeOptions(opts.IndexOptions)
	if opts.IndexOptions.MaxContentBytes > opts.MaxMemoryBytes/4 {
		opts.IndexOptions.MaxContentBytes = opts.MaxMemoryBytes / 4
	}
	if opts.IndexOptions.MaxIndexBytes <= 0 || opts.IndexOptions.MaxIndexBytes > opts.MaxMemoryBytes {
		opts.IndexOptions.MaxIndexBytes = opts.MaxMemoryBytes
	}
	return &Manager{entries: make(map[string]*managedIndex), options: opts}
}

// Search returns a fast result from the cache. It only blocks for a rebuild when
// a project has no usable index yet or was explicitly invalidated.
func (m *Manager) Search(ctx context.Context, root string, req SearchRequest) (SearchResult, error) {
	if !IndexingEnabled() {
		return SearchResult{}, ErrIndexingDisabled
	}
	if m == nil {
		m = DefaultManager()
	}
	resolvedRoot, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return SearchResult{}, err
	}

	idx, err := m.indexFor(ctx, resolvedRoot)
	if err != nil {
		return SearchResult{}, err
	}
	return idx.Search(req), nil
}

// Invalidate drops a cached project index so the next search observes internal
// Brute file writes/moves/deletes immediately.
func (m *Manager) Invalidate(root string) {
	if m == nil {
		return
	}
	resolvedRoot, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry := m.entries[resolvedRoot]; entry != nil && entry.idx != nil {
		m.cachedBytes -= entry.idx.stats.ApproxBytes
	}
	delete(m.entries, resolvedRoot)
}

// Warm starts a non-blocking initial build for a project root. It intentionally
// avoids replacing a usable index synchronously so opening a project does not
// steal CPU from the UI thread.
func (m *Manager) Warm(root string) {
	if !IndexingEnabled() {
		return
	}
	if m == nil {
		return
	}
	resolvedRoot, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return
	}
	m.mu.Lock()
	entry := m.entries[resolvedRoot]
	if entry != nil && (entry.idx != nil || entry.building) {
		m.mu.Unlock()
		return
	}
	entry = &managedIndex{root: resolvedRoot, building: true, wait: make(chan struct{}), lastAccess: time.Now()}
	m.entries[resolvedRoot] = entry
	m.mu.Unlock()
	go m.rebuild(resolvedRoot)
}

// Clear drops every cached project index. It is used when indexing is disabled
// so the process can release the large text/trigram cache promptly.
func (m *Manager) Clear() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = make(map[string]*managedIndex)
	m.cachedBytes = 0
}

func (m *Manager) indexFor(ctx context.Context, root string) (*Index, error) {
	for {
		m.mu.Lock()
		entry := m.entries[root]
		if entry == nil {
			entry = &managedIndex{root: root, building: true, wait: make(chan struct{}), lastAccess: time.Now()}
			m.entries[root] = entry
			m.mu.Unlock()
			idx, err := Build(ctx, root, m.options.IndexOptions)
			m.finishBuild(root, idx, err)
			return idx, err
		}

		if entry.idx != nil {
			entry.lastAccess = time.Now()
			idx := entry.idx
			if time.Since(idx.builtAt) > m.options.StaleAfter && !entry.building {
				entry.building = true
				entry.wait = make(chan struct{})
				go m.rebuild(root)
			}
			m.mu.Unlock()
			return idx, nil
		}

		if entry.building {
			wait := entry.wait
			m.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		entry.building = true
		entry.wait = make(chan struct{})
		m.mu.Unlock()
		idx, err := Build(ctx, root, m.options.IndexOptions)
		m.finishBuild(root, idx, err)
		return idx, err
	}
}

func (m *Manager) rebuild(root string) {
	idx, err := Build(context.Background(), root, m.options.IndexOptions)
	m.finishBuild(root, idx, err)
}

func (m *Manager) finishBuild(root string, idx *Index, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[root]
	if entry == nil {
		entry = &managedIndex{root: root}
		m.entries[root] = entry
	}
	if entry.idx != nil {
		m.cachedBytes -= entry.idx.stats.ApproxBytes
	}
	if err == nil && idx != nil && IndexingEnabled() {
		entry.idx = idx
		entry.lastAccess = time.Now()
		m.cachedBytes += idx.stats.ApproxBytes
		m.pruneLocked(root)
	}
	entry.buildErr = err
	entry.building = false
	if entry.wait != nil {
		close(entry.wait)
		entry.wait = nil
	}
}

func (m *Manager) pruneLocked(protectedRoot string) {
	if m.options.MaxMemoryBytes <= 0 {
		return
	}
	for m.cachedBytes > m.options.MaxMemoryBytes {
		var oldestRoot string
		var oldest time.Time
		for root, entry := range m.entries {
			if root == protectedRoot || entry.idx == nil || entry.building {
				continue
			}
			if oldestRoot == "" || entry.lastAccess.Before(oldest) {
				oldestRoot = root
				oldest = entry.lastAccess
			}
		}
		if oldestRoot == "" {
			return
		}
		m.cachedBytes -= m.entries[oldestRoot].idx.stats.ApproxBytes
		delete(m.entries, oldestRoot)
	}
}
