package integrationtools

import (
	"context"
	"strings"
)

// Capture native boundaries before formatting, rather than rediscovering them
// from untrusted snippets that can contain forged headings and URL lines.
type webCaptureKey struct{}
type webResultCapture struct {
	captured bool
	prefix   string
	items    []webRelevanceItem
}

func webCapturePrefix(ctx context.Context, prefix string) {
	if capture, ok := ctx.Value(webCaptureKey{}).(*webResultCapture); ok {
		capture.captured = true
		first, rest, found := strings.Cut(prefix, "\n")
		capture.prefix = prefix
		if found && strings.TrimSpace(rest) != "" {
			capture.prefix = first + "\n"
			capture.items = append(capture.items, webRelevanceItem{title: "Search summary", content: rest})
		}
	}
}
func webCaptureItem(ctx context.Context, title, url, content string) {
	if capture, ok := ctx.Value(webCaptureKey{}).(*webResultCapture); ok {
		capture.items = append(capture.items, webRelevanceItem{title: strings.Join(strings.Fields(title), " "), url: url, content: content})
	}
}
