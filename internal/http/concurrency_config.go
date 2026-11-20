package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/storage"
)

// App setting holding the global concurrency limits as JSON (concurrencyConfig).
const concurrencySettingKey = storage.ConcurrencySettingKey

const concurrencyLimitMax = 100

type concurrencyConfig struct {
	MaxSessions   int                  `json:"max_sessions"`
	ProjectLimits map[string]int       `json:"project_limits"`
	Queues        []sessionQueueConfig `json:"queues"`
	MaxCPUPercent int                  `json:"max_cpu_percent"`
	MaxRAMPercent int                  `json:"max_ram_percent"`
	MaxGPUPercent int                  `json:"max_gpu_percent"`
}

// parseConcurrencyConfig degrades a broken value to "no limits" instead of stalling every run.
func parseConcurrencyConfig(raw string) concurrencyConfig {
	var cfg concurrencyConfig
	if strings.TrimSpace(raw) == "" {
		return normalizedEmptyConcurrencyConfig()
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		logging.Warn("Ignoring invalid concurrency settings: %v", err)
		return normalizedEmptyConcurrencyConfig()
	}
	normalized, err := normalizeConcurrencyConfig(cfg)
	if err != nil {
		logging.Warn("Ignoring invalid concurrency settings: %v", err)
		normalized.Queues = nil
	}
	return normalized
}

func normalizedEmptyConcurrencyConfig() concurrencyConfig {
	return concurrencyConfig{ProjectLimits: map[string]int{}, Queues: []sessionQueueConfig{}}
}

func normalizeConcurrencyConfig(cfg concurrencyConfig) (concurrencyConfig, error) {
	out := normalizedEmptyConcurrencyConfig()
	out.MaxSessions = clampNonNegative(cfg.MaxSessions, concurrencyLimitMax)
	out.MaxCPUPercent = clampNonNegative(cfg.MaxCPUPercent, 100)
	out.MaxRAMPercent = clampNonNegative(cfg.MaxRAMPercent, 100)
	out.MaxGPUPercent = clampNonNegative(cfg.MaxGPUPercent, 100)
	for projectID, limit := range cfg.ProjectLimits {
		projectID = strings.TrimSpace(projectID)
		if limit = clampNonNegative(limit, concurrencyLimitMax); projectID != "" && limit > 0 {
			out.ProjectLimits[projectID] = limit
		}
	}
	seen := make(map[string]struct{}, len(cfg.Queues))
	for _, queue := range cfg.Queues {
		name, err := validateSessionQueueName(queue.Name)
		if err != nil {
			return out, err
		}
		if _, duplicate := seen[name]; duplicate {
			return out, errors.New("duplicate session queue name: " + name)
		}
		seen[name] = struct{}{}
		out.Queues = append(out.Queues, sessionQueueConfig{Name: name, Limit: clampSessionQueueLimit(queue.Limit)})
	}
	return out, nil
}

func clampNonNegative(n, max int) int {
	if n < 0 {
		return 0
	}
	if n > max {
		return max
	}
	return n
}

func (c concurrencyConfig) queueLimit(name string) (int, bool) {
	name = strings.TrimSpace(name)
	for _, queue := range c.Queues {
		if queue.Name == name {
			return queue.Limit, true
		}
	}
	return 0, false
}

func (c concurrencyConfig) queueNames() []string {
	names := make([]string, 0, len(c.Queues))
	for _, queue := range c.Queues {
		names = append(names, queue.Name)
	}
	return names
}

func (c concurrencyConfig) resourceLimitsEnabled() bool {
	return c.MaxCPUPercent > 0 || c.MaxRAMPercent > 0 || c.MaxGPUPercent > 0
}

func (c concurrencyConfig) anyLimitEnabled() bool {
	return c.MaxSessions > 0 || len(c.ProjectLimits) > 0 || c.resourceLimitsEnabled()
}

func encodeConcurrencyConfig(cfg concurrencyConfig) (string, error) {
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (s *Server) loadConcurrencyConfig() concurrencyConfig {
	st := s.concurrencyState()
	st.mu.Lock()
	cached := st.cfg
	generation := st.cfgGeneration
	st.mu.Unlock()
	if cached != nil {
		return cached.clone()
	}
	cfg := normalizedEmptyConcurrencyConfig()
	if s.store != nil {
		settings, err := s.store.GetSettings()
		if err != nil {
			logging.Warn("Failed to load concurrency settings: %v", err)
			return cfg
		}
		cfg = parseConcurrencyConfig(settings[concurrencySettingKey])
	}
	cachedCopy := cfg.clone()
	st.mu.Lock()
	if st.cfgGeneration == generation {
		st.cfg = &cachedCopy
	}
	st.mu.Unlock()
	return cfg
}

func (c concurrencyConfig) clone() concurrencyConfig {
	out := c
	out.ProjectLimits = make(map[string]int, len(c.ProjectLimits))
	for id, limit := range c.ProjectLimits {
		out.ProjectLimits[id] = limit
	}
	out.Queues = append([]sessionQueueConfig{}, c.Queues...)
	return out
}

func (s *Server) invalidateConcurrencyConfig() {
	st := s.concurrencyState()
	st.mu.Lock()
	st.cfg = nil
	st.cfgGeneration++
	st.mu.Unlock()
}

func (s *Server) saveConcurrencyConfig(cfg concurrencyConfig) error {
	if s.store == nil {
		return errors.New("settings store is unavailable")
	}
	encoded, err := encodeConcurrencyConfig(cfg)
	if err != nil {
		return err
	}
	if err := s.saveSingleSetting(concurrencySettingKey, encoded); err != nil {
		return err
	}
	s.invalidateConcurrencyConfig()
	s.detachLoopsFromRemovedQueues()
	s.scheduleSessionQueueResume()
	return nil
}

// migrateProjectConcurrencySettings moves legacy per-project max_parallel_sessions and
// session_queues into the global config: queues merge by name keeping the larger limit.
func (s *Server) migrateProjectConcurrencySettings() {
	s.migrateLegacyProjectConcurrency(false)
}

// applyLegacyProjectConcurrency takes values an older client just saved on a project as-is.
func (s *Server) applyLegacyProjectConcurrency() {
	s.migrateLegacyProjectConcurrency(true)
}

func (s *Server) migrateLegacyProjectConcurrency(overwrite bool) {
	if s.store == nil {
		return
	}
	projects, err := s.store.ListProjects()
	if err != nil {
		logging.Warn("Failed to list projects for concurrency migration: %v", err)
		return
	}
	cfg := s.loadConcurrencyConfig()
	migrated := make([]*storage.Project, 0)
	for _, project := range projects {
		if mergeLegacyProjectConcurrency(&cfg, project, overwrite) {
			migrated = append(migrated, project)
		}
	}
	if len(migrated) == 0 {
		return
	}
	if err := s.saveConcurrencyConfig(cfg); err != nil {
		logging.Warn("Failed to save migrated concurrency settings: %v", err)
		return
	}
	for _, project := range migrated {
		delete(project.Settings, projectMaxParallelSessionsKey)
		delete(project.Settings, projectSessionQueuesKey)
		if err := s.store.SaveProject(project); err != nil {
			logging.Warn("Failed to drop migrated concurrency settings of project %s: %v", project.ID, err)
		}
	}
	logging.Info("Migrated concurrency settings of %d project(s) to global settings", len(migrated))
}

func hasLegacyProjectConcurrency(project *storage.Project) bool {
	if project == nil || project.Settings == nil {
		return false
	}
	_, hasLimit := project.Settings[projectMaxParallelSessionsKey]
	_, hasQueues := project.Settings[projectSessionQueuesKey]
	return hasLimit || hasQueues
}

func mergeLegacyProjectConcurrency(cfg *concurrencyConfig, project *storage.Project, overwrite bool) bool {
	if !hasLegacyProjectConcurrency(project) {
		return false
	}
	rawLimit, hasLimit := project.Settings[projectMaxParallelSessionsKey]
	rawQueues := project.Settings[projectSessionQueuesKey]
	if hasLimit && strings.TrimSpace(rawLimit) != "" {
		if limit := parseMaxParallelSessions(rawLimit); overwrite || limit > cfg.ProjectLimits[project.ID] {
			cfg.ProjectLimits[project.ID] = limit
		}
	}
	queues, err := parseSessionQueues(rawQueues)
	if err != nil {
		logging.Warn("Skipped invalid session queues of project %s during migration: %v", project.ID, err)
	}
	for _, queue := range queues {
		merged := false
		for i := range cfg.Queues {
			if cfg.Queues[i].Name == queue.Name {
				cfg.Queues[i].Limit = max(cfg.Queues[i].Limit, queue.Limit)
				if overwrite {
					cfg.Queues[i].Limit = queue.Limit
				}
				merged = true
			}
		}
		if !merged {
			cfg.Queues = append(cfg.Queues, queue)
		}
	}
	return true
}

type singleSettingStore interface {
	SetSetting(key, value string) error
}

func (s *Server) saveSingleSetting(key, value string) error {
	if store, ok := s.store.(singleSettingStore); ok {
		return store.SetSetting(key, value)
	}
	settings, err := s.store.GetSettings()
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}
	settings[key] = value
	return s.store.SaveSettings(settings)
}
