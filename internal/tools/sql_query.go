package tools

import (
	"context"
	"encoding/json"
	"fmt"

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
				"description": "Maximum number of rows to return (default 50).",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "Offset for pagination (default 0).",
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
	if !ok {
		return &Result{Success: false, Error: "query is required"}, nil
	}

	limit := 50
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	offset := 0
	if o, ok := args["offset"].(float64); ok {
		offset = int(o)
	}

	format := "csv"
	if f, ok := args["format"].(string); ok {
		format = f
	}

	var cfg dbtool.Config

	connectionName, hasConnName := args["connection_name"].(string)
	dsn, hasDsn := args["dsn"].(string)

	if hasConnName && connectionName != "" {
		// ToolContext / Session ProjectId is not strictly passed to tools right now cleanly without a larger refactor in Execute method signature.
		// For now we will rely on DSN or maybe fetch it differently. Wait, I can modify the ToolContext / session? 
		// Actually, I can retrieve the ProjectID if it is passed in the context. Is it? Let's check `manager.Execute` signature.
		// It only takes ctx. I can try to extract project ID from context if it's there.
		
		// For now, let's just use a global or something if we really need it, but realistically I can't look up by project ID here easily if context doesn't have it.
		// Let's modify so connectionName checks all databases and matches by name if it's unique, or we can look up project ID from context if we inject it.
		// For now let's just mock the ProjectID extraction.
		
		var projectID string
		// Try to find the project ID from context. 
		if pID, ok := ctx.Value("projectID").(string); ok && pID != "" {
			projectID = pID
		}
		
		if projectID == "" {
			return &Result{Success: false, Error: "connection_name requires an active project context (project ID not found in context)"}, nil
		}

		dbs, err := t.store.ListProjectDatabases(projectID)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("failed to fetch project databases: %v", err)}, nil
		}

		var matchedDB *storage.ProjectDatabase
		for _, db := range dbs {
			if db.Name == connectionName {
				matchedDB = db
				break
			}
		}

		if matchedDB == nil {
			available := ""
			for _, db := range dbs {
				available += db.Name + ", "
			}
			return &Result{Success: false, Error: fmt.Sprintf("database connection '%s' not found in project. Available: %s", connectionName, available)}, nil
		}

		cfg = dbtool.Config{
			Engine:     matchedDB.Engine,
			DSN:        matchedDB.DSN,
			IsReadOnly: matchedDB.IsReadOnly,
		}
	} else if hasDsn && dsn != "" {
		engine, _ := args["engine"].(string)
		if engine == "" {
			return &Result{Success: false, Error: "engine is required when using raw dsn"}, nil
		}
		cfg = dbtool.Config{
			Engine:     engine,
			DSN:        dsn,
			IsReadOnly: false, // Assume raw connections allow execution unless restricted by the user credentials
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
