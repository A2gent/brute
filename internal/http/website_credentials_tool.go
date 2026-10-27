package http

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/A2gent/brute/internal/tools"
	"github.com/A2gent/brute/internal/websitecredentials"
)

type websiteCredentialsTool struct{ store *websitecredentials.Store }

func (t *websiteCredentialsTool) Name() string { return "website_credentials" }
func (t *websiteCredentialsTool) Description() string {
	return "Read saved website credentials. operation=list returns metadata only; operation=get requires id and returns the raw password. The user explicitly consents to the raw secret entering model context and session history. Use credentials only for the stored site. Never echo passwords into responses or transmit them anywhere else. No write operations are available."
}
func (t *websiteCredentialsTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation": map[string]interface{}{"type": "string", "enum": []string{"list", "get"}},
			"id":        map[string]interface{}{"type": "string", "description": "Saved credential ID; required for get."},
		},
		"required":             []string{"operation"},
		"additionalProperties": false,
	}
}
func (t *websiteCredentialsTool) Execute(ctx context.Context, params json.RawMessage) (*tools.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var p struct {
		Operation string `json:"operation"`
		ID        string `json:"id"`
	}
	fail := func(message string) (*tools.Result, error) { return &tools.Result{Error: message}, nil }
	if json.Unmarshal(params, &p) != nil {
		return fail("Invalid website credential parameters")
	}
	var output any
	var err error
	switch p.Operation {
	case "list":
		output, err = t.store.List()
	case "get":
		if strings.TrimSpace(p.ID) == "" {
			return fail("id is required for get")
		}
		var record websitecredentials.Credential
		record, err = t.store.Get(p.ID)
		output = struct {
			ID       string `json:"id"`
			URL      string `json:"url"`
			Login    string `json:"login"`
			Password string `json:"password"`
		}{record.ID, record.URL, record.Login, record.Password}
	default:
		return fail("operation must be list or get")
	}
	if errors.Is(err, websitecredentials.ErrNotFound) {
		return fail("Website credential not found")
	}
	if err != nil {
		return fail("Website credential storage unavailable")
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return fail("Website credential result unavailable")
	}
	return &tools.Result{Success: true, Output: string(encoded)}, nil
}
