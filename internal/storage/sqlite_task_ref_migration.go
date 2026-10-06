package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// migrateBrokenTaskRefs repairs refs created from UTF-8 bytes instead of runes.
func (s *SQLiteStore) migrateBrokenTaskRefs() error {
	rows, err := s.db.Query(`SELECT t.id, t.project_id, t.seq, t.ref, p.name, p.settings FROM tasks t JOIN projects p ON p.id = t.project_id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type brokenRef struct {
		id, projectID, ref, name, settings string
		seq                                int
	}
	var tasks []brokenRef
	for rows.Next() {
		var task brokenRef
		if err := rows.Scan(&task.id, &task.projectID, &task.seq, &task.ref, &task.name, &task.settings); err != nil {
			return err
		}
		var settingMap map[string]string
		if err := json.Unmarshal([]byte(task.settings), &settingMap); err != nil {
			return fmt.Errorf("decode project settings for task %s: %w", task.id, err)
		}
		prefix := taskRefPrefix(&Project{Name: task.name, Settings: settingMap})
		expected := prefix + "-" + strconv.Itoa(task.seq)
		if task.ref == expected {
			continue
		}
		// The byte-slicing bug left a mangled prefix: either literal U+FFFD runes
		// or raw invalid UTF-8 bytes in the DB. Only rewrite refs of the form
		// "<mangled>-<seq>", never valid custom refs.
		suffix := "-" + strconv.Itoa(task.seq)
		if !strings.HasSuffix(task.ref, suffix) {
			continue
		}
		mangled := task.ref[:len(task.ref)-len(suffix)]
		if utf8.ValidString(mangled) && !strings.ContainsRune(mangled, '\uFFFD') {
			continue
		}
		task.settings = expected
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, task := range tasks {
		var collision string
		err := tx.QueryRow(`SELECT id FROM tasks WHERE project_id = ? AND ref = ?`, task.projectID, task.settings).Scan(&collision)
		if err == nil && collision != task.id {
			return fmt.Errorf("cannot rename task %s to %s: ref already belongs to task %s", task.id, task.settings, collision)
		}
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if _, err := tx.Exec(`UPDATE tasks SET ref = ? WHERE id = ?`, task.settings, task.id); err != nil {
			return fmt.Errorf("rename task %s: %w", task.id, err)
		}
	}
	return tx.Commit()
}

func isValidASCIIRef(ref string) bool {
	if ref == "" {
		return false
	}
	for _, r := range ref {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
