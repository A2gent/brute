package speechcache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const DefaultTTL = 15 * time.Minute

type clip struct {
	contentType string
	data        []byte
	createdAt   time.Time
}

type persistedClipMetadata struct {
	ContentType string    `json:"content_type"`
	CreatedAt   time.Time `json:"created_at"`
}

// Store keeps generated speech clips in memory for fast playback and, when
// configured with a disk directory, persists them so session history can keep
// playing Telegram/TTS replies after the in-memory TTL or a server restart.
type Store struct {
	mu      sync.Mutex
	ttl     time.Duration
	clips   map[string]clip
	diskDir string
}

func New(ttl time.Duration) *Store {
	return newStore(ttl, "")
}

// NewPersistent creates a speech clip store that mirrors every saved clip to
// disk. The memory cache still uses ttl, but Load can fall back to disk when a
// session references an older clip ID.
func NewPersistent(ttl time.Duration, diskDir string) *Store {
	return newStore(ttl, diskDir)
}

func newStore(ttl time.Duration, diskDir string) *Store {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Store{
		ttl:     ttl,
		clips:   make(map[string]clip, 32),
		diskDir: strings.TrimSpace(diskDir),
	}
}

func (s *Store) Save(contentType string, data []byte) string {
	if s == nil {
		return ""
	}

	ct := strings.TrimSpace(contentType)
	if ct == "" {
		ct = "audio/mpeg"
	}
	payload := make([]byte, len(data))
	copy(payload, data)

	id := uuid.New().String()
	now := time.Now()
	item := clip{
		contentType: ct,
		data:        payload,
		createdAt:   now,
	}

	s.mu.Lock()
	s.cleanupExpiredLocked(now)
	s.clips[id] = item
	diskDir := s.diskDir
	s.mu.Unlock()

	// Persist after updating memory so transient disk errors never break a live
	// TTS response; they only affect later history playback.
	if diskDir != "" {
		_ = persistClipToDisk(diskDir, id, item)
	}
	return id
}

func (s *Store) Load(id string) (string, []byte, bool) {
	if s == nil {
		return "", nil, false
	}

	clipID := strings.TrimSpace(id)
	if !isSafeClipID(clipID) {
		return "", nil, false
	}

	now := time.Now()
	s.mu.Lock()
	s.cleanupExpiredLocked(now)

	item, ok := s.clips[clipID]
	if ok {
		s.mu.Unlock()
		payload := make([]byte, len(item.data))
		copy(payload, item.data)
		return item.contentType, payload, true
	}
	diskDir := s.diskDir
	s.mu.Unlock()

	if diskDir == "" {
		return "", nil, false
	}

	item, ok = loadClipFromDisk(diskDir, clipID)
	if !ok {
		return "", nil, false
	}

	// Rehydrate memory cache so repeated playback does not keep hitting disk.
	cached := item
	cached.createdAt = now
	s.mu.Lock()
	s.cleanupExpiredLocked(now)
	s.clips[clipID] = cached
	s.mu.Unlock()

	payload := make([]byte, len(item.data))
	copy(payload, item.data)
	return item.contentType, payload, true
}

func (s *Store) cleanupExpiredLocked(now time.Time) {
	cutoff := now.Add(-s.ttl)
	for id, item := range s.clips {
		if item.createdAt.Before(cutoff) {
			delete(s.clips, id)
		}
	}
}

func isSafeClipID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func clipDataPath(dir, id string) string {
	return filepath.Join(dir, id+".audio")
}

func clipMetadataPath(dir, id string) string {
	return filepath.Join(dir, id+".json")
}

func persistClipToDisk(dir, id string, item clip) error {
	if !isSafeClipID(id) {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	if err := writeFileAtomic(clipDataPath(dir, id), item.data, 0o644); err != nil {
		return err
	}
	meta := persistedClipMetadata{
		ContentType: item.contentType,
		CreatedAt:   item.createdAt,
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return writeFileAtomic(clipMetadataPath(dir, id), metaBytes, 0o644)
}

func loadClipFromDisk(dir, id string) (clip, bool) {
	if !isSafeClipID(id) {
		return clip{}, false
	}
	data, err := os.ReadFile(clipDataPath(dir, id))
	if err != nil || len(data) == 0 {
		return clip{}, false
	}

	meta := persistedClipMetadata{}
	if metaBytes, err := os.ReadFile(clipMetadataPath(dir, id)); err == nil {
		_ = json.Unmarshal(metaBytes, &meta)
	}
	ct := strings.TrimSpace(meta.ContentType)
	if ct == "" {
		ct = "audio/mpeg"
	}
	createdAt := meta.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	payload := make([]byte, len(data))
	copy(payload, data)
	return clip{
		contentType: ct,
		data:        payload,
		createdAt:   createdAt,
	}, true
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-clip-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
