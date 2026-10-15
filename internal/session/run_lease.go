package session

import (
	"context"
	"fmt"
)

type runLeaseKey struct{}
type runLease struct {
	manager *Manager
	id      string
}

// AcquireRun serializes tools and transition decisions, including non-HTTP runs.
// Only the owning context can reenter (HTTP admission followed by agent execution).
func (m *Manager) AcquireRun(ctx context.Context, id string) (context.Context, func(), error) {
	if lease, ok := ctx.Value(runLeaseKey{}).(runLease); ok && lease.manager == m && lease.id == id {
		return ctx, func() {}, nil
	}
	leases := &m.runLeases
	if _, loaded := leases.LoadOrStore(id, true); loaded {
		return ctx, nil, fmt.Errorf("session already has an active run")
	}
	return context.WithValue(ctx, runLeaseKey{}, runLease{m, id}), func() { leases.Delete(id) }, nil
}

// ActiveRunCount reports sessions currently holding a run lease (any run path).
func (m *Manager) ActiveRunCount() int {
	count := 0
	m.runLeases.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}
