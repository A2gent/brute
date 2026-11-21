package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	showHTMLMaxBytes       = 512 * 1024
	showHTMLDefaultHeight  = 400
	showHTMLMinHeight      = 80
	showHTMLMaxHeight      = 1600
	showHTMLMetadataKind   = "html_widget"
	showHTMLMetadataFormat = "html"
)

type ShowHTMLParams struct {
	Title  string `json:"title,omitempty"`
	HTML   string `json:"html"`
	Height int    `json:"height,omitempty"`
}

type ShowHTMLTool struct{}

func NewShowHTMLTool() *ShowHTMLTool {
	return &ShowHTMLTool{}
}

func (t *ShowHTMLTool) Name() string {
	return "show_html"
}

func (t *ShowHTMLTool) Description() string {
	return `Show an inline HTML block to the user in the Caesar session view. Use it for UI prototypes, diagrams, tables, small interactive demos and other visual explanations that are clearer rendered than described.

Important usage:
- Pass a complete, self-contained HTML document or fragment. Inline all CSS and JavaScript.
- The block runs in a sandboxed iframe with no network access: external scripts, stylesheets, fonts, images and fetch calls are blocked. Use inline SVG, CSS and data: URIs.
- Caesar renders the block directly from this call. Do not repeat the HTML in your reply; describe it briefly instead.
- Prefer a neutral light or dark friendly design with explicit colors, since the page background is not inherited.
- Terminal and non-Caesar clients only see a short text confirmation.`
}

func (t *ShowHTMLTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Optional short caption shown above the block.",
			},
			"html": map[string]interface{}{
				"type":        "string",
				"description": "Self-contained HTML (document or fragment) with inline CSS and JS. No external resources.",
			},
			"height": map[string]interface{}{
				"type":        "integer",
				"description": fmt.Sprintf("Frame height in pixels (%d-%d, default %d).", showHTMLMinHeight, showHTMLMaxHeight, showHTMLDefaultHeight),
			},
		},
		"required": []string{"html"},
	}
}

func (t *ShowHTMLTool) Execute(ctx context.Context, params json.RawMessage) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var p ShowHTMLParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	normalized, errMessage := normalizeShowHTMLParams(p)
	if errMessage != "" {
		return &Result{Success: false, Error: errMessage}, nil
	}

	return &Result{
		Success: true,
		Output:  fmt.Sprintf("HTML block rendered for the user (%d bytes, height %dpx).", len(normalized.HTML), normalized.Height),
		Metadata: map[string]interface{}{
			"kind":   showHTMLMetadataKind,
			"format": showHTMLMetadataFormat,
			"title":  normalized.Title,
			"height": normalized.Height,
		},
	}, nil
}

func normalizeShowHTMLParams(p ShowHTMLParams) (ShowHTMLParams, string) {
	normalized := ShowHTMLParams{
		Title:  strings.TrimSpace(p.Title),
		HTML:   strings.TrimSpace(p.HTML),
		Height: p.Height,
	}

	if normalized.HTML == "" {
		return ShowHTMLParams{}, "html is required"
	}
	if len(normalized.HTML) > showHTMLMaxBytes {
		return ShowHTMLParams{}, fmt.Sprintf("html is too large (%d bytes, max %d)", len(normalized.HTML), showHTMLMaxBytes)
	}

	switch {
	case normalized.Height <= 0:
		normalized.Height = showHTMLDefaultHeight
	case normalized.Height < showHTMLMinHeight:
		normalized.Height = showHTMLMinHeight
	case normalized.Height > showHTMLMaxHeight:
		normalized.Height = showHTMLMaxHeight
	}

	return normalized, ""
}

var _ Tool = (*ShowHTMLTool)(nil)
