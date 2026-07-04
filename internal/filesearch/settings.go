package filesearch

import (
	"os"
	"strings"
	"sync/atomic"
)

const IndexingEnabledSettingKey = "A2GENT_FILE_INDEXING_ENABLED"

var indexingEnabled atomic.Bool

func init() {
	indexingEnabled.Store(parseEnabled(os.Getenv(IndexingEnabledSettingKey)))
}

func SetIndexingEnabled(enabled bool) {
	indexingEnabled.Store(enabled)
	if !enabled {
		DefaultManager().Clear()
	}
}

func SetIndexingEnabledFromSettings(settings map[string]string) {
	SetIndexingEnabled(parseEnabled(settings[IndexingEnabledSettingKey]))
}

func IndexingEnabled() bool {
	return indexingEnabled.Load()
}

func parseEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}
