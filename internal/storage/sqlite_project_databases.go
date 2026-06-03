package storage

// SaveProjectDatabase creates or updates a project database.
func (s *SQLiteStore) SaveProjectDatabase(db *ProjectDatabase) error {
	_, err := s.db.Exec(`
		INSERT INTO project_databases (id, project_id, name, engine, dsn, environment, is_read_only, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			project_id = excluded.project_id,
			name = excluded.name,
			engine = excluded.engine,
			dsn = excluded.dsn,
			environment = excluded.environment,
			is_read_only = excluded.is_read_only,
			updated_at = excluded.updated_at
	`, db.ID, db.ProjectID, db.Name, db.Engine, db.DSN, db.Environment, db.IsReadOnly, db.CreatedAt, db.UpdatedAt)
	return err
}

// GetProjectDatabase returns a project database by ID.
func (s *SQLiteStore) GetProjectDatabase(id string) (*ProjectDatabase, error) {
	row := s.db.QueryRow(`
		SELECT id, project_id, name, engine, dsn, environment, is_read_only, created_at, updated_at
		FROM project_databases WHERE id = ?
	`, id)

	var db ProjectDatabase
	err := row.Scan(&db.ID, &db.ProjectID, &db.Name, &db.Engine, &db.DSN, &db.Environment, &db.IsReadOnly, &db.CreatedAt, &db.UpdatedAt)
	if err != nil {
		return nil, err
	}

	return &db, nil
}

// ListProjectDatabases returns all databases for a project.
func (s *SQLiteStore) ListProjectDatabases(projectID string) ([]*ProjectDatabase, error) {
	rows, err := s.db.Query(`
		SELECT id, project_id, name, engine, dsn, environment, is_read_only, created_at, updated_at
		FROM project_databases
		WHERE project_id = ?
		ORDER BY created_at DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dbs []*ProjectDatabase
	for rows.Next() {
		var db ProjectDatabase
		err := rows.Scan(&db.ID, &db.ProjectID, &db.Name, &db.Engine, &db.DSN, &db.Environment, &db.IsReadOnly, &db.CreatedAt, &db.UpdatedAt)
		if err != nil {
			return nil, err
		}
		dbs = append(dbs, &db)
	}

	return dbs, nil
}

// DeleteProjectDatabase deletes a project database by ID.
func (s *SQLiteStore) DeleteProjectDatabase(id string) error {
	_, err := s.db.Exec(`DELETE FROM project_databases WHERE id = ?`, id)
	return err
}
