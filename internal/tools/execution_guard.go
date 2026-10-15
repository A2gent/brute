package tools

import "context"

type executionGuardKey struct{}

// WithExecutionGuard follows nested parallel/pipeline calls through their context.
// Filtering definitions alone cannot prevent a model from calling a hidden tool.
func WithExecutionGuard(ctx context.Context, guard func(string) error) context.Context {
	return context.WithValue(ctx, executionGuardKey{}, guard)
}
