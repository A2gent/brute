package contextcompress

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/tools"
)

type RetrieveTool struct {
	compressor *Compressor
}

type RetrieveParams struct {
	Hash  string `json:"hash"`
	Query string `json:"query,omitempty"`
}

func NewRetrieveTool(compressor *Compressor) *RetrieveTool {
	return &RetrieveTool{compressor: compressor}
}

func (t *RetrieveTool) Name() string {
	return RetrievalToolName
}

func (t *RetrieveTool) Description() string {
	return "Retrieve original content for a compressed tool result by hash. Use query to return only matching lines when possible."
}

func (t *RetrieveTool) Schema() map[string]interface{} {
	return RetrievalToolDefinition().InputSchema
}

func (t *RetrieveTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	var p RetrieveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	hash := strings.TrimSpace(p.Hash)
	if hash == "" {
		return &tools.Result{Success: false, Error: "hash is required"}, nil
	}
	sessionID, _ := ctx.Value("session_id").(string)
	if strings.TrimSpace(sessionID) == "" {
		return &tools.Result{Success: false, Error: "session_id not found in context"}, nil
	}
	content, ok := t.compressor.Retrieve(sessionID, hash, p.Query)
	if !ok {
		return &tools.Result{Success: false, Error: "compressed content not found for this session"}, nil
	}
	return &tools.Result{Success: true, Output: content}, nil
}
