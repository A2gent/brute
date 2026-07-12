package storage

import (
	"fmt"
	"strings"
	"time"
)

// CustomEnvStore is implemented by stores that keep user-defined environment
// variables separately from managed app settings.
type CustomEnvStore interface {
	GetCustomEnv() (map[string]string, error)
	SaveCustomEnv(env map[string]string) error
	SaveSettingsAndCustomEnv(settings map[string]string, customEnv map[string]string) error
}

// GetSettings returns managed app settings as key/value pairs.
func (s *SQLiteStore) GetSettings() (map[string]string, error) {
	rows, err := s.db.Query(`
		SELECT key, value
		FROM app_settings
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		settings[key] = value
	}

	return settings, rows.Err()
}

func normalizeManagedSettings(settings map[string]string) map[string]string {
	out := make(map[string]string)
	for key, value := range settings {
		k := strings.TrimSpace(key)
		if k == "" || !isManagedAppSettingKey(k) {
			continue
		}
		out[k] = value
	}
	return out
}

func normalizeCustomEnv(env map[string]string) map[string]string {
	out := make(map[string]string)
	for key, value := range env {
		k := strings.TrimSpace(key)
		if k == "" || isManagedAppSettingKey(k) {
			continue
		}
		out[k] = value
	}
	return out
}

func splitSettingsAndCustomEnv(settings map[string]string) (map[string]string, map[string]string) {
	managed := make(map[string]string)
	custom := make(map[string]string)
	for key, value := range settings {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		if isManagedAppSettingKey(k) {
			managed[k] = value
		} else {
			custom[k] = value
		}
	}
	return managed, custom
}

// SaveSettings replaces managed app settings with the provided map. Legacy
// callers may still include arbitrary env-looking keys; those keys are ignored
// here so app_settings cannot become a process-env source again.
func (s *SQLiteStore) SaveSettings(settings map[string]string) error {
	return s.saveSettingsAndCustomEnv(normalizeManagedSettings(settings), nil, false)
}

func (s *SQLiteStore) GetCustomEnv() (map[string]string, error) {
	rows, err := s.db.Query(`
		SELECT key, value
		FROM custom_env
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	env := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		env[key] = value
	}
	return env, rows.Err()
}

func (s *SQLiteStore) SaveCustomEnv(env map[string]string) error {
	return s.saveSettingsAndCustomEnv(nil, normalizeCustomEnv(env), true)
}

func (s *SQLiteStore) SaveSettingsAndCustomEnv(settings map[string]string, customEnv map[string]string) error {
	managed, legacyCustom := splitSettingsAndCustomEnv(settings)
	normalizedCustom := normalizeCustomEnv(customEnv)
	for key, value := range legacyCustom {
		if _, ok := normalizedCustom[key]; !ok {
			normalizedCustom[key] = value
		}
	}
	return s.saveSettingsAndCustomEnv(managed, normalizedCustom, true)
}

func (s *SQLiteStore) saveSettingsAndCustomEnv(settings map[string]string, customEnv map[string]string, replaceCustomEnv bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if settings != nil {
		if _, err := tx.Exec(`DELETE FROM app_settings`); err != nil {
			return fmt.Errorf("failed to clear settings: %w", err)
		}
		now := time.Now()
		for key, value := range settings {
			if key == "" {
				continue
			}
			if _, err := tx.Exec(`
				INSERT INTO app_settings (key, value, updated_at)
				VALUES (?, ?, ?)
			`, key, value, now); err != nil {
				return fmt.Errorf("failed to save setting %q: %w", key, err)
			}
		}
	}

	if replaceCustomEnv {
		if _, err := tx.Exec(`DELETE FROM custom_env`); err != nil {
			return fmt.Errorf("failed to clear custom env: %w", err)
		}
		now := time.Now()
		for key, value := range customEnv {
			if key == "" {
				continue
			}
			if _, err := tx.Exec(`
				INSERT INTO custom_env (key, value, updated_at)
				VALUES (?, ?, ?)
			`, key, value, now); err != nil {
				return fmt.Errorf("failed to save custom env %q: %w", key, err)
			}
		}
	}

	return tx.Commit()
}
