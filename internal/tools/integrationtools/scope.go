package integrationtools

import (
	"context"
	"fmt"
	"strings"

	"github.com/A2gent/brute/internal/storage"
)

// Scope limits which integrations an agent run can see. ProjectID is the
// project the run belongs to ("" = none); ProjectOnly hides global
// integrations so the agent only uses its project's credentials.
type Scope struct {
	ProjectID   string
	ProjectOnly bool
}

type scopeContextKey struct{}

// WithScope attaches an integration scope to ctx for integration tools.
func WithScope(ctx context.Context, scope Scope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	scope.ProjectID = strings.TrimSpace(scope.ProjectID)
	return context.WithValue(ctx, scopeContextKey{}, scope)
}

// ScopeFromContext returns the integration scope attached to ctx. ok=false
// means no scope was set (TUI, jobs, legacy callers): tools see everything.
func ScopeFromContext(ctx context.Context) (Scope, bool) {
	if ctx == nil {
		return Scope{}, false
	}
	scope, ok := ctx.Value(scopeContextKey{}).(Scope)
	return scope, ok
}

// FilterIntegrations returns the integrations visible within scope:
//   - integrations of other projects are never visible;
//   - ProjectOnly keeps only the scope project's integrations (none without a project);
//   - otherwise global + project integrations, where a project integration of a
//     provider hides global integrations of the same provider, so a project Jira
//     wins over the global Jira without "multiple integrations" ambiguity.
func FilterIntegrations(all []*storage.Integration, scope Scope) []*storage.Integration {
	projectID := strings.TrimSpace(scope.ProjectID)
	shadowed := map[string]bool{}
	if projectID != "" {
		for _, item := range all {
			if item != nil && strings.TrimSpace(item.ProjectID) == projectID {
				shadowed[item.Provider] = true
			}
		}
	}
	out := make([]*storage.Integration, 0, len(all))
	for _, item := range all {
		if item == nil {
			continue
		}
		owner := strings.TrimSpace(item.ProjectID)
		switch {
		case owner != "":
			if projectID == "" || owner != projectID {
				continue
			}
		case scope.ProjectOnly || shadowed[item.Provider]:
			continue
		}
		out = append(out, item)
	}
	return out
}

// listIntegrations is the single integration source for tools: it applies the
// scope from ctx, or returns everything when the run has no scope.
func listIntegrations(ctx context.Context, store storage.Store) ([]*storage.Integration, error) {
	all, err := store.ListIntegrations()
	if err != nil {
		return nil, err
	}
	scope, ok := ScopeFromContext(ctx)
	if !ok {
		return all, nil
	}
	return FilterIntegrations(all, scope), nil
}

// scopedStore exposes only in-scope integrations to code that takes a whole
// storage.Store (codehostreview), so explicit IDs outside the scope fail as
// "not found" there too.
type scopedStore struct {
	storage.Store
	ctx context.Context
}

func withScopedStore(ctx context.Context, store storage.Store) storage.Store {
	if store == nil {
		return nil
	}
	if _, ok := ScopeFromContext(ctx); !ok {
		return store
	}
	return scopedStore{Store: store, ctx: ctx}
}

func (s scopedStore) ListIntegrations() ([]*storage.Integration, error) {
	return listIntegrations(s.ctx, s.Store)
}

func (s scopedStore) GetIntegration(id string) (*storage.Integration, error) {
	items, err := listIntegrations(s.ctx, s.Store)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return nil, fmt.Errorf("integration not found: %s", id)
}
