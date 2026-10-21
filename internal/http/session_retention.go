package http

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

// Settings for automatic cleanup of JSONL session logs. Zero/blank disables a limit.
const (
	sessionsRetentionMaxMBSettingKey    = "AAGENT_SESSIONS_RETENTION_MAX_MB"
	sessionsRetentionMaxDaysSettingKey  = "AAGENT_SESSIONS_RETENTION_MAX_DAYS"
	sessionsRetentionMaxFilesSettingKey = "AAGENT_SESSIONS_RETENTION_MAX_FILES"

	// defaultSessionsRetentionMaxMB is applied when the size setting was never saved.
	defaultSessionsRetentionMaxMB = 1024
	sessionsRetentionInterval     = time.Hour
)

func retentionPolicyFromSettings(settings map[string]string) session.RetentionPolicy {
	num := func(key string) (int64, bool) {
		raw := strings.TrimSpace(settings[key])
		if raw == "" {
			return 0, false
		}
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 0 {
			return 0, false
		}
		return v, true
	}
	mb, ok := num(sessionsRetentionMaxMBSettingKey)
	if !ok {
		mb = defaultSessionsRetentionMaxMB
	}
	days, _ := num(sessionsRetentionMaxDaysSettingKey)
	files, _ := num(sessionsRetentionMaxFilesSettingKey)
	return session.RetentionPolicy{
		MaxBytes: mb << 20,
		MaxAge:   time.Duration(days) * 24 * time.Hour,
		MaxFiles: int(files),
	}
}

// pruneSessionLogs applies the configured retention policy once.
func (s *Server) pruneSessionLogs() {
	settings, err := s.store.GetSettings()
	if err != nil {
		return
	}
	n, freed := session.PruneJSONLLogs(s.sessionManager.JSONLFolder(), retentionPolicyFromSettings(settings), time.Now())
	if n > 0 {
		logging.Info("Session log retention removed %d files (%d MB)", n, freed>>20)
	}
}

// runSessionLogRetention prunes at startup and then periodically, so logs cannot grow unbounded.
func (s *Server) runSessionLogRetention(ctx context.Context) {
	ticker := time.NewTicker(sessionsRetentionInterval)
	defer ticker.Stop()
	for {
		s.pruneSessionLogs()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
