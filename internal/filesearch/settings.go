package filesearch

import (
	"os"
	"strings"
	"sync/atomic"
)

const IndexingEnabledSettingKey = "A2GENT_FILE_INDEXING_ENABLED"

// ProjectIndexingEnabledSettingKey stores per-project indexing preference in projects.settings.
const ProjectIndexingEnabledSettingKey = "A2GENT_PROJECT_FILE_INDEXING_ENABLED"

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

// IsIndexingEnabledForProject resolves indexing for one project. Explicit project
// settings win; otherwise legacy global Tools/env settings are used.
func IsIndexingEnabledForProject(projectSettings map[string]string, globalSettings map[string]string) bool {
	if projectSettings != nil {
		if value, ok := projectSettings[ProjectIndexingEnabledSettingKey]; ok && strings.TrimSpace(value) != "" {
			return parseEnabled(value)
		}
	}
	if globalSettings != nil {
		if value, ok := globalSettings[IndexingEnabledSettingKey]; ok && strings.TrimSpace(value) != "" {
			return parseEnabled(value)
		}
	}
	return IndexingEnabled()
}

func parseEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}
