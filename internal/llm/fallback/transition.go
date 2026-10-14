package fallback

import (
	"context"
	"fmt"
)

type boundaryKey struct{}

// WithSessionBoundary disables in-request provider switching. The caller must
// durably commit context and policy before rebuilding the next request.
func WithSessionBoundary(ctx context.Context) context.Context {
	return context.WithValue(ctx, boundaryKey{}, true)
}

func boundaryEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(boundaryKey{}).(bool)
	return enabled
}

type TransitionRequired struct {
	Source        Node
	Target        Node
	Index         int
	Reason        string
	SourceOutcome string
	RetryCount    int
}

func (e *TransitionRequired) Error() string {
	return fmt.Sprintf("provider transition required: %s -> %s: %s", e.Source.Name, e.Target.Name, e.Reason)
}
func (c *Client) boundary(ctx context.Context, index int, reason, outcome string, attempts int) error {
	if enabled, _ := ctx.Value(boundaryKey{}).(bool); !enabled || index+1 >= len(c.nodes) {
		return nil
	}
	return &TransitionRequired{Source: c.nodes[index], Target: c.nodes[index+1], Index: index + 1, Reason: reason, SourceOutcome: outcome, RetryCount: max(0, attempts-1)}
}
func (c *Client) CommitTransition(index int) { c.setCurrentIndex(index) }
func (c *Client) NodeAt(index int) (Node, bool) {
	if index < 0 || index >= len(c.nodes) {
		return Node{}, false
	}
	return c.nodes[index], true
}
