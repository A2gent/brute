package http

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/A2gent/brute/internal/dbtool"
	"github.com/A2gent/brute/internal/storage"
)

func (s *Server) handleListProjectDatabases(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")
	dbs, err := s.store.ListProjectDatabases(projectID)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to list project databases: "+err.Error())
		return
	}

	resp := make([]ProjectDatabaseResponse, len(dbs))
	for i, db := range dbs {
		resp[i] = ProjectDatabaseResponse{
			ID:          db.ID,
			ProjectID:   db.ProjectID,
			Name:        db.Name,
			Engine:      db.Engine,
			DSN:         db.DSN,
			Environment: db.Environment,
			IsReadOnly:  db.IsReadOnly,
			CreatedAt:   db.CreatedAt,
			UpdatedAt:   db.UpdatedAt,
		}
	}
	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleCreateProjectDatabase(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectID")

	var req CreateProjectDatabaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	if req.Name == "" || req.Engine == "" || req.DSN == "" || req.Environment == "" {
		s.errorResponse(w, http.StatusBadRequest, "Name, engine, dsn, and environment are required")
		return
	}

	now := time.Now()
	db := &storage.ProjectDatabase{
		ID:          uuid.New().String(),
		ProjectID:   projectID,
		Name:        req.Name,
		Engine:      req.Engine,
		DSN:         req.DSN,
		Environment: req.Environment,
		IsReadOnly:  req.IsReadOnly,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.store.SaveProjectDatabase(db); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to create project database: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusCreated, ProjectDatabaseResponse{
		ID:          db.ID,
		ProjectID:   db.ProjectID,
		Name:        db.Name,
		Engine:      db.Engine,
		DSN:         db.DSN,
		Environment: db.Environment,
		IsReadOnly:  db.IsReadOnly,
		CreatedAt:   db.CreatedAt,
		UpdatedAt:   db.UpdatedAt,
	})
}

func (s *Server) handleUpdateProjectDatabase(w http.ResponseWriter, r *http.Request) {
	dbID := chi.URLParam(r, "dbID")

	var req UpdateProjectDatabaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	db, err := s.store.GetProjectDatabase(dbID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project database not found")
		return
	}

	if req.Name != nil {
		db.Name = *req.Name
	}
	if req.Engine != nil {
		db.Engine = *req.Engine
	}
	if req.DSN != nil {
		db.DSN = *req.DSN
	}
	if req.Environment != nil {
		db.Environment = *req.Environment
	}
	if req.IsReadOnly != nil {
		db.IsReadOnly = *req.IsReadOnly
	}
	db.UpdatedAt = time.Now()

	if err := s.store.SaveProjectDatabase(db); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to update project database: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectDatabaseResponse{
		ID:          db.ID,
		ProjectID:   db.ProjectID,
		Name:        db.Name,
		Engine:      db.Engine,
		DSN:         db.DSN,
		Environment: db.Environment,
		IsReadOnly:  db.IsReadOnly,
		CreatedAt:   db.CreatedAt,
		UpdatedAt:   db.UpdatedAt,
	})
}

func (s *Server) handleDeleteProjectDatabase(w http.ResponseWriter, r *http.Request) {
	dbID := chi.URLParam(r, "dbID")
	if err := s.store.DeleteProjectDatabase(dbID); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to delete project database: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleProjectDatabaseListTables(w http.ResponseWriter, r *http.Request) {
	dbID := chi.URLParam(r, "dbID")

	dbRecord, err := s.store.GetProjectDatabase(dbID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project database not found")
		return
	}

	cfg := dbtool.Config{
		Engine:     dbRecord.Engine,
		DSN:        dbRecord.DSN,
		IsReadOnly: dbRecord.IsReadOnly,
	}

	tables, err := dbtool.GetTables(r.Context(), cfg)
	if err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to fetch tables: "+err.Error())
		return
	}

	resp := make([]ProjectDatabaseTableResponse, len(tables))
	for i, t := range tables {
		resp[i] = ProjectDatabaseTableResponse{Name: t}
	}

	s.jsonResponse(w, http.StatusOK, resp)
}

func (s *Server) handleProjectDatabaseTableSchema(w http.ResponseWriter, r *http.Request) {
	dbID := chi.URLParam(r, "dbID")
	tableName := chi.URLParam(r, "tableName")
	if tableName == "" {
		s.errorResponse(w, http.StatusBadRequest, "Table is required")
		return
	}

	dbRecord, err := s.store.GetProjectDatabase(dbID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project database not found")
		return
	}
	if dbRecord.Engine == "redis" {
		s.errorResponse(w, http.StatusBadRequest, "Table schema is not available for Redis connections")
		return
	}

	columns, err := dbtool.GetTableColumns(r.Context(), dbtool.Config{
		Engine:     dbRecord.Engine,
		DSN:        dbRecord.DSN,
		IsReadOnly: dbRecord.IsReadOnly,
	}, tableName)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to load table schema: "+err.Error())
		return
	}

	response := make([]ProjectDatabaseTableColumnResponse, len(columns))
	for index, column := range columns {
		foreignKeys := make([]ProjectDatabaseColumnAnalyticsForeignKey, len(column.ForeignKeys))
		for foreignKeyIndex, foreignKey := range column.ForeignKeys {
			foreignKeys[foreignKeyIndex] = ProjectDatabaseColumnAnalyticsForeignKey{
				ConstraintName:   foreignKey.ConstraintName,
				ReferencedTable:  foreignKey.ReferencedTable,
				ReferencedColumn: foreignKey.ReferencedColumn,
			}
		}
		response[index] = ProjectDatabaseTableColumnResponse{
			Name:         column.Name,
			DataType:     column.DataType,
			IsPrimaryKey: column.IsPrimaryKey,
			IsNullable:   column.IsNullable,
			ForeignKeys:  foreignKeys,
		}
	}
	s.jsonResponse(w, http.StatusOK, response)
}

func (s *Server) handleProjectDatabaseUpdateCell(w http.ResponseWriter, r *http.Request) {
	dbID := chi.URLParam(r, "dbID")
	tableName := chi.URLParam(r, "tableName")
	if tableName == "" {
		s.errorResponse(w, http.StatusBadRequest, "Table is required")
		return
	}

	dbRecord, err := s.store.GetProjectDatabase(dbID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project database not found")
		return
	}

	var req ProjectDatabaseUpdateCellRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request payload")
		return
	}
	if req.Column == "" {
		s.errorResponse(w, http.StatusBadRequest, "Column is required")
		return
	}
	if len(req.PrimaryKey) == 0 {
		s.errorResponse(w, http.StatusBadRequest, "Primary key values are required")
		return
	}

	result, err := dbtool.UpdateTableCell(r.Context(), dbtool.Config{
		Engine:     dbRecord.Engine,
		DSN:        dbRecord.DSN,
		IsReadOnly: dbRecord.IsReadOnly,
	}, tableName, req.Column, req.Value, req.PrimaryKey)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to update cell: "+err.Error())
		return
	}

	s.jsonResponse(w, http.StatusOK, ProjectDatabaseUpdateCellResponse{
		Query:        result.Query,
		RowsAffected: result.RowsAffected,
	})
}

func (s *Server) handleProjectDatabaseColumnAnalytics(w http.ResponseWriter, r *http.Request) {
	dbID := chi.URLParam(r, "dbID")
	tableName := chi.URLParam(r, "tableName")
	columnName := chi.URLParam(r, "columnName")
	if tableName == "" || columnName == "" {
		s.errorResponse(w, http.StatusBadRequest, "Table and column are required")
		return
	}

	dbRecord, err := s.store.GetProjectDatabase(dbID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project database not found")
		return
	}
	if dbRecord.Engine == "redis" {
		s.errorResponse(w, http.StatusBadRequest, "Column analytics is not available for Redis connections")
		return
	}

	analytics, err := dbtool.GetColumnAnalytics(r.Context(), dbtool.Config{
		Engine:     dbRecord.Engine,
		DSN:        dbRecord.DSN,
		IsReadOnly: dbRecord.IsReadOnly,
	}, tableName, columnName, 20)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Failed to analyze column: "+err.Error())
		return
	}

	response := ProjectDatabaseColumnAnalyticsResponse{
		Table:              analytics.Table,
		Column:             analytics.Column,
		TotalRowCount:      analytics.TotalRowCount,
		DistinctCount:      analytics.DistinctCount,
		NullCount:          analytics.NullCount,
		TopValuesTruncated: analytics.TopValuesTruncated,
		TopValues:          make([]ProjectDatabaseColumnAnalyticsValue, len(analytics.TopValues)),
		ForeignKeys:        make([]ProjectDatabaseColumnAnalyticsForeignKey, len(analytics.ForeignKeys)),
	}
	for index, value := range analytics.TopValues {
		response.TopValues[index] = ProjectDatabaseColumnAnalyticsValue{Value: value.Value, Count: value.Count}
	}
	for index, foreignKey := range analytics.ForeignKeys {
		response.ForeignKeys[index] = ProjectDatabaseColumnAnalyticsForeignKey{
			ConstraintName:   foreignKey.ConstraintName,
			ReferencedTable:  foreignKey.ReferencedTable,
			ReferencedColumn: foreignKey.ReferencedColumn,
		}
	}
	s.jsonResponse(w, http.StatusOK, response)
}

func (s *Server) handleProjectDatabaseQuery(w http.ResponseWriter, r *http.Request) {
	dbID := chi.URLParam(r, "dbID")

	dbRecord, err := s.store.GetProjectDatabase(dbID)
	if err != nil {
		s.errorResponse(w, http.StatusNotFound, "Project database not found")
		return
	}

	var req ProjectDatabaseDataRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	cfg := dbtool.Config{
		Engine:     dbRecord.Engine,
		DSN:        dbRecord.DSN,
		IsReadOnly: dbRecord.IsReadOnly, // Enforced in ExecuteQuery
	}

	result, err := dbtool.ExecuteQuery(r.Context(), cfg, req.Query, req.Limit, req.Offset, "json")
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Query failed: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(result))
}
