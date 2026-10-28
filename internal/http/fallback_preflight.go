package http

import (
	"context"
	"time"

	"github.com/A2gent/brute/internal/config"
	"github.com/A2gent/brute/internal/llm/fallback"
	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

// Some usage sources (Codex, Cursor, OpenRouter) are HTTP calls. The run start must
// not wait on a slow usage endpoint: the in-request skipper remains the backstop,
// so an unknown result here only means the chain starts where it used to.
const fallbackPreflightTimeout = 5 * time.Second

// preflightFallbackStart moves the chain start past nodes whose usage is already exhausted.
// WHY: the chain used to discover an exhausted head node only inside the first request. The
// run then failed and a provider transition (including context compaction) ran before the
// session produced any answer. Checking usage first starts the run on a usable node.
// WHAT: advances and persists fallback_progress; the chain client, the context window and
// the UI then all agree on the node the run starts on.
func (s *Server) preflightFallbackStart(ctx context.Context, providerRef config.ProviderType, sess *session.Session) {
	if s == nil || sess == nil || s.config == nil {
		return
	}
	ref := config.NormalizeProviderRef(string(providerRef))
	if !config.IsFallbackAggregateRef(ref) && ref != string(config.ProviderFallback) {
		return
	}
	chain, err := s.fallbackNodesForProvider(providerRef)
	if err != nil || len(chain) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, fallbackPreflightTimeout)
	defer cancel()

	start := sessionFallbackStartIndex(sess, providerRef)
	advanced := start
	for advanced < len(chain) {
		if ctx.Err() != nil {
			return
		}
		node := fallback.Node{Name: chain[advanced].Provider, Model: chain[advanced].Model}
		skip, reason := s.fallbackNodeUsageSkip(ctx, node)
		if !skip {
			break
		}
		logging.Warn("Fallback chain preflight: node %d/%d %s/%s unusable: %s", advanced+1, len(chain), node.Name, node.Model, reason)
		advanced++
	}
	// Every node is exhausted: leave the index alone so the chain reports its own
	// aggregate error instead of hiding that nothing can serve the session.
	if advanced == start || advanced >= len(chain) {
		return
	}

	setSessionFallbackStartIndex(sess, providerRef, advanced)
	if s.sessionManager == nil {
		return
	}
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist fallback preflight start: %v", err)
	}
}
