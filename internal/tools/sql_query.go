package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/dbtool"
	"github.com/A2gent/brute/internal/storage"
)

type SQLQueryTool struct {
	store storage.Store
}

func NewSQLQueryTool(store storage.Store) *SQLQueryTool {
	return &SQLQueryTool{store: store}
}

func (t *SQLQueryTool) Name() string {
	return "sql_query"
}

func (t *SQLQueryTool) Description() string {
	return `Execute a SQL query against a configured project database.
Use connection_name to reference a pre-configured database from the project context, OR provide a raw dsn.
You must use limit and offset parameters to paginate results safely to avoid context overflow.
Output can be in csv or json format (default csv).`
}

func (t *SQLQueryTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"connection_name": map[string]interface{}{
				"type":        "string",
				"description": "Name of the configured project database (e.g., 'production', 'test database'). Required if dsn is not provided.",
			},
			"dsn": map[string]interface{}{
				"type":        "string",
				"description": "Raw DSN to connect to. Required if connection_name is not provided.",
			},
			"engine": map[string]interface{}{
				"type":        "string",
				"description": "Database engine (postgres, mysql, sqlite). Required if using raw dsn.",
				"enum":        []string{"postgres", "mysql", "sqlite"},
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "The SQL query to execute. Do not include multiple statements separated by semicolon.",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of rows to return (default 50, max 1000).",
				"minimum":     1,
				"maximum":     1000,
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "Offset for pagination (default 0).",
				"minimum":     0,
			},
			"format": map[string]interface{}{
				"type":        "string",
				"description": "Output format: csv or json (default csv).",
				"enum":        []string{"csv", "json"},
			},
		},
		"required": []string{"query"},
	}
}

func (t *SQLQueryTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	var args map[string]interface{}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	query, ok := args["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return &Result{Success: false, Error: "query is required"}, nil
	}

	limit := 50
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}
	// Require bounded pagination before opening a DB connection so accidental
	// model-generated calls cannot request unbounded result sets.
	if limit <= 0 || limit > 1000 {
		return &Result{Success: false, Error: "limit must be between 1 and 1000"}, nil
	}

	offset := 0
	if o, ok := args["offset"].(float64); ok {
		offset = int(o)
	}
	if offset < 0 {
		return &Result{Success: false, Error: "offset must be greater than or equal to 0"}, nil
	}

	format := "csv"
	if f, ok := args["format"].(string); ok {
		format = strings.ToLower(strings.TrimSpace(f))
		if format == "" {
			format = "csv"
		}
	}
	if format != "csv" && format != "json" {
		return &Result{Success: false, Error: "format must be one of: csv, json"}, nil
	}

	var cfg dbtool.Config

	connectionName, hasConnName := args["connection_name"].(string)
	dsn, hasDsn := args["dsn"].(string)
	connectionName = strings.TrimSpace(connectionName)
	dsn = strings.TrimSpace(dsn)

	if hasConnName && connectionName != "" {
		projectID, _ := ctx.Value("projectID").(string)
		projectID = strings.TrimSpace(projectID)
		if projectID == "" {
			return &Result{Success: false, Error: "connection_name requires an active project context (project ID not found in context)"}, nil
		}
		if t.store == nil {
			return &Result{Success: false, Error: "connection_name requires project database storage to be configured"}, nil
		}

		dbs, err := t.store.ListProjectDatabases(projectID)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("failed to fetch project databases: %v", err)}, nil
		}

		var matchedDB *storage.ProjectDatabase
		available := make([]string, 0, len(dbs))
		for _, db := range dbs {
			available = append(available, db.Name)
			if db.Name == connectionName {
				matchedDB = db
				break
			}
		}

		if matchedDB == nil {
			return &Result{Success: false, Error: fmt.Sprintf("database connection %q not found in project. Available: %s", connectionName, strings.Join(available, ", "))}, nil
		}

		cfg = dbtool.Config{
			Engine:     strings.TrimSpace(matchedDB.Engine),
			DSN:        strings.TrimSpace(matchedDB.DSN),
			IsReadOnly: matchedDB.IsReadOnly,
		}
	} else if hasDsn && dsn != "" {
		engine, _ := args["engine"].(string)
		engine = strings.ToLower(strings.TrimSpace(engine))
		if engine == "" {
			return &Result{Success: false, Error: "engine is required when using raw dsn"}, nil
		}
		if engine != "postgres" && engine != "mysql" && engine != "sqlite" {
			return &Result{Success: false, Error: "engine must be one of: postgres, mysql, sqlite"}, nil
		}
		cfg = dbtool.Config{
			Engine:     engine,
			DSN:        dsn,
			IsReadOnly: false, // Raw DSN permissions are controlled by the provided database credentials.
		}
	} else {
		return &Result{Success: false, Error: "either connection_name or dsn must be provided"}, nil
	}

	result, err := dbtool.ExecuteQuery(ctx, cfg, query, limit, offset, format)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("query failed: %v", err)}, nil
	}

	return &Result{Success: true, Output: result}, nil
}
