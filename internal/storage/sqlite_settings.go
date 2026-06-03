package storage

import (
	"fmt"
	"time"
)

// GetSettings returns all app settings as key/value pairs.
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

	return settings, nil
}

// SaveSettings replaces all app settings with the provided map.
func (s *SQLiteStore) SaveSettings(settings map[string]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

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

	return tx.Commit()
}
