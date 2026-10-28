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

	previous := sessionFallbackStartIndex(sess, providerRef)
	// A chain shortened after this session advanced leaves the index past the end,
	// and the client reports "exhausted" before any skip callback can repair it.
	// Clamp so the session can run on the remaining nodes again.
	if previous >= len(chain) {
		setSessionFallbackStartIndex(sess, providerRef, 0)
		s.persistPreflightStart(ctx, sess)
		return
	}

	advanced := previous
	for advanced < len(chain) {
		// Keep a partial advance: nodes already known to be exhausted stay skipped
		// even if a later usage probe runs out of the preflight budget.
		if ctx.Err() != nil {
			break
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
	if advanced == previous || advanced >= len(chain) {
		return
	}
	setSessionFallbackStartIndex(sess, providerRef, advanced)
	s.persistPreflightStart(ctx, sess)
}

// persistPreflightStart writes the advanced start only for the run that owns the
// session. Persisting writes the whole row, so an auxiliary caller working on a
// detached snapshot (session summary, manual compaction, job prep) would delete
// messages a live run appended after the copy. The chain client reads the index
// from the in-memory session, so those callers still start on the right node.
func (s *Server) persistPreflightStart(ctx context.Context, sess *session.Session) {
	if s.sessionManager == nil || !s.sessionManager.HoldsRunLease(ctx, sess.ID) {
		return
	}
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Failed to persist fallback preflight start: %v", err)
	}
}
