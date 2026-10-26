package claudecli

import "context"

type mcpOnlyContextKey struct{}

// WithMCPOnly confines a proxied child invocation to its reverse MCP bridge.
// The context follows fallback nodes without changing shared provider clients.
func WithMCPOnly(ctx context.Context, workDir string) context.Context {
	return context.WithValue(ctx, mcpOnlyContextKey{}, workDir)
}

func (c *Client) forInvocation(ctx context.Context) *Client {
	if workDir, ok := ctx.Value(mcpOnlyContextKey{}).(string); ok {
		opts := c.options
		opts.MCPOnly = true
		opts.WorkDir = workDir
		return NewClientWithOptions(c.model, opts)
	}
	return c
}
