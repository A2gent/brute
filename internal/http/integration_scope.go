package http

import (
	"context"
	"strings"

	"github.com/A2gent/brute/internal/agentdef"
	"github.com/A2gent/brute/internal/session"
	"github.com/A2gent/brute/internal/tools/integrationtools"
)

// parentAgentToolsIntegrationScopeKey carries the delegated child's integration
// scope in an MCP bridge token. WHY: delegated tokens are bound to the parent
// session, whose project/flag may differ from the child agent's.
type parentAgentToolsIntegrationScopeKey struct{}

// integrationScopeForSession resolves which integrations an agent session may
// use. Agent sessions use the agent's bound project (else the session project)
// and the agent's project_integrations_only flag; plain sessions see global +
// their project's integrations.
func (s *Server) integrationScopeForSession(sess *session.Session) integrationtools.Scope {
	if sess == nil {
		return integrationtools.Scope{}
	}
	scope := integrationtools.Scope{}
	if sess.ProjectID != nil {
		scope.ProjectID = strings.TrimSpace(*sess.ProjectID)
	}
	if sess.Metadata == nil {
		return scope
	}
	if saID := strings.TrimSpace(metadataString(sess.Metadata["sub_agent_id"])); saID != "" && s.store != nil {
		if sa, err := s.store.GetSubAgent(saID); err == nil && sa != nil {
			if sa.ProjectID != nil && strings.TrimSpace(*sa.ProjectID) != "" {
				scope.ProjectID = strings.TrimSpace(*sa.ProjectID)
			}
			scope.ProjectOnly = sa.ProjectIntegrationsOnly
			return scope
		}
	}
	if strings.TrimSpace(metadataString(sess.Metadata["unified_agent_id"])) != "" {
		if def, err := s.unifiedAgentDefinitionForSession(sess); err == nil && def != nil {
			if bound := strings.TrimSpace(def.Local.ProjectBindings[agentdef.WorkspaceScopeConfiguredProject]); bound != "" {
				scope.ProjectID = bound
			}
			scope.ProjectOnly = def.Tools.ProjectIntegrationsOnly
		}
	}
	return scope
}

// withIntegrationScope attaches the integration scope for tool calls of sess.
// A scope minted into a delegated bridge token wins over the session scope.
func (s *Server) withIntegrationScope(ctx context.Context, sess *session.Session) context.Context {
	if scope, ok := ctx.Value(parentAgentToolsIntegrationScopeKey{}).(integrationtools.Scope); ok {
		return integrationtools.WithScope(ctx, scope)
	}
	return integrationtools.WithScope(ctx, s.integrationScopeForSession(sess))
}

// delegatedIntegrationScope is the scope of a delegate_to_agent child: its
// resolved workspace project (else the parent's project) and its own flag.
func delegatedIntegrationScope(parent *session.Session, workspace *dockerWorkspaceBinding) integrationtools.Scope {
	scope := integrationtools.Scope{}
	if parent != nil && parent.ProjectID != nil {
		scope.ProjectID = strings.TrimSpace(*parent.ProjectID)
	}
	if workspace != nil {
		if projectID := strings.TrimSpace(workspace.ProjectID); projectID != "" {
			scope.ProjectID = projectID
		}
		scope.ProjectOnly = workspace.ProjectIntegrationsOnly
	}
	return scope
}
