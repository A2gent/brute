package http

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	nethttp "net/http"
	"net/http/httputil"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/logging"
	"github.com/A2gent/brute/internal/session"
)

const (
	dockerSessionProxyLookupTimeout  = 5 * time.Second
	dockerSessionProxyRequestTimeout = 2 * time.Second
)

type dockerSessionCandidate struct {
	APIURL          string
	AgentName       string
	Runtime         string // "host" for host-runtime agents; empty means docker
	ParentSessionID string
}

func (s *Server) recordDockerDelegationChildSession(parentSessionID string, agent *LocalDockerAgent, childSessionID string, workspace *dockerWorkspaceBinding) {
	parentSessionID = strings.TrimSpace(parentSessionID)
	childSessionID = strings.TrimSpace(childSessionID)
	if parentSessionID == "" || childSessionID == "" || agent == nil {
		return
	}

	// The parent session metadata can be overwritten by the running parent's own
	// save, so keep the owner in memory too.
	s.dockerChildOwners.Store(childSessionID, dockerSessionCandidate{
		APIURL:          agent.APIURL,
		AgentName:       agent.Name,
		Runtime:         agentRuntimeName(agent),
		ParentSessionID: parentSessionID,
	})

	sess, err := s.sessionManager.Get(parentSessionID)
	if err != nil {
		logging.Warn("Docker delegation: failed to load parent session %s for child mapping: %v", parentSessionID, err)
		return
	}
	addDockerChildSessionEntry(sess, agent, childSessionID, workspace)
	if err := s.sessionManager.Save(sess); err != nil {
		logging.Warn("Docker delegation: failed to save parent session %s child mapping: %v", parentSessionID, err)
	}
}

// addDockerChildSessionEntry records the child mapping on an in-memory session.
func addDockerChildSessionEntry(sess *session.Session, agent *LocalDockerAgent, childSessionID string, workspace *dockerWorkspaceBinding) {
	parentSessionID := sess.ID
	if sess.Metadata == nil {
		sess.Metadata = map[string]interface{}{}
	}

	entry := map[string]interface{}{
		"child_session_id":  childSessionID,
		"parent_session_id": parentSessionID,
		"agent_id":          agent.ID,
		"agent_name":        agent.Name,
		"agent_api_url":     strings.TrimRight(strings.TrimSpace(agent.APIURL), "/"),
		"agent_runtime":     agentRuntimeName(agent),
		"created_at":        time.Now().UTC().Format(time.RFC3339Nano),
	}
	if workspace != nil {
		entry["docker_workspace"] = dockerWorkspaceMetadata(*workspace)
	}

	entries := dockerChildSessionEntries(sess.Metadata["docker_child_sessions"])
	next := make([]map[string]interface{}, 0, len(entries)+1)
	for _, existing := range entries {
		if metadataString(existing["child_session_id"]) == childSessionID {
			continue
		}
		next = append(next, existing)
	}
	next = append(next, entry)
	sess.Metadata["docker_child_sessions"] = next
}

func (s *Server) getDockerDelegatedSession(ctx context.Context, sessionID string, includeMessages bool, includeMetadata bool) (SessionResponse, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionResponse{}, false, nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, dockerSessionProxyLookupTimeout)
	defer cancel()

	client := &nethttp.Client{Timeout: dockerSessionProxyRequestTimeout}
	failures := make([]string, 0)
	for candidate := range s.dockerSessionCandidateStages(lookupCtx, sessionID) {
		resp, err := fetchDockerSession(lookupCtx, client, candidate, sessionID, includeMessages)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		s.rememberDockerChildOwner(sessionID, candidate)

		decorateDockerSessionResponse(&resp, candidate)
		if !includeMessages {
			resp.Messages = nil
			resp.SystemPromptSnapshot = nil
		}
		if !includeMetadata {
			resp.Metadata = nil
		}
		return resp, true, nil
	}

	if len(failures) > 0 {
		return SessionResponse{}, false, fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return SessionResponse{}, false, nil
}

// findDockerSessionOwner returns the agent that hosts a delegated child session.
func (s *Server) findDockerSessionOwner(ctx context.Context, sessionID string) (dockerSessionCandidate, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return dockerSessionCandidate{}, false
	}
	lookupCtx, cancel := context.WithTimeout(ctx, dockerSessionProxyLookupTimeout)
	defer cancel()

	client := &nethttp.Client{Timeout: dockerSessionProxyRequestTimeout}
	for candidate := range s.dockerSessionCandidateStages(lookupCtx, sessionID) {
		if _, err := fetchDockerSession(lookupCtx, client, candidate, sessionID, false); err != nil {
			continue
		}
		s.rememberDockerChildOwner(sessionID, candidate)
		return candidate, true
	}
	return dockerSessionCandidate{}, false
}

// proxyDockerDelegatedGET forwards a read-only session sub-resource (events, queue, approval)
// to the agent that owns the child session. It reports false when no agent owns the session.
func (s *Server) proxyDockerDelegatedGET(w nethttp.ResponseWriter, r *nethttp.Request, sessionID, suffix string) bool {
	owner, ok := s.findDockerSessionOwner(r.Context(), sessionID)
	if !ok {
		return false
	}
	target, err := neturl.Parse(owner.APIURL)
	if err != nil {
		return false
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = strings.TrimRight(target.Path, "/") + "/sessions/" + sessionID + suffix
			pr.Out.URL.RawPath = ""
			pr.Out.Host = target.Host
		},
		// The upstream agent adds its own CORS headers; duplicates would make browsers reject the response.
		ModifyResponse: func(resp *nethttp.Response) error {
			for name := range resp.Header {
				if strings.HasPrefix(strings.ToLower(name), "access-control-") {
					resp.Header.Del(name)
				}
			}
			return nil
		},
		FlushInterval: -1,
		ErrorHandler: func(w nethttp.ResponseWriter, _ *nethttp.Request, err error) {
			logging.Debug("Docker session proxy %s failed for %s: %v", suffix, sessionID, err)
			s.errorResponse(w, nethttp.StatusBadGateway, "Failed to reach delegated agent")
		},
	}
	proxy.ServeHTTP(w, r)
	return true
}

func (s *Server) getDockerDelegatedProviderTransitions(ctx context.Context, sessionID string) ([]session.ProviderTransition, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, false, nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, dockerSessionProxyLookupTimeout)
	defer cancel()

	client := &nethttp.Client{Timeout: dockerSessionProxyRequestTimeout}
	failures := make([]string, 0)
	for candidate := range s.dockerSessionCandidateStages(lookupCtx, sessionID) {
		endpoint := strings.TrimRight(candidate.APIURL, "/") + "/sessions/" + neturl.PathEscape(sessionID) + "/provider-transitions"
		req, err := nethttp.NewRequestWithContext(lookupCtx, nethttp.MethodGet, endpoint, nil)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", candidate.APIURL, err))
			continue
		}
		if resp.StatusCode == nethttp.StatusNotFound {
			_ = resp.Body.Close()
			continue
		}
		if resp.StatusCode < nethttp.StatusOK || resp.StatusCode >= nethttp.StatusMultipleChoices {
			failures = append(failures, fmt.Sprintf("%s returned %s", candidate.APIURL, resp.Status))
			_ = resp.Body.Close()
			continue
		}

		var result struct {
			Version     int                          `json:"version"`
			Transitions []session.ProviderTransition `json:"transitions"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&result)
		_ = resp.Body.Close()
		if decodeErr != nil {
			failures = append(failures, fmt.Sprintf("%s decode provider transitions: %v", candidate.APIURL, decodeErr))
			continue
		}
		if result.Version != 1 {
			failures = append(failures, fmt.Sprintf("%s returned unsupported provider transition version %d", candidate.APIURL, result.Version))
			continue
		}
		s.rememberDockerChildOwner(sessionID, candidate)
		return result.Transitions, true, nil
	}
	if len(failures) > 0 {
		return nil, false, fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil, false, nil
}

func (s *Server) rememberDockerChildOwner(childSessionID string, candidate dockerSessionCandidate) {
	s.dockerChildOwners.Store(childSessionID, candidate)
}

// dockerSessionCandidateStages yields the remembered owner first, so a slow
// session scan or docker ps cannot use up the lookup budget before it is tried.
func (s *Server) dockerSessionCandidateStages(ctx context.Context, childSessionID string) iter.Seq[dockerSessionCandidate] {
	return func(yield func(dockerSessionCandidate) bool) {
		var first string
		if owner, ok := s.dockerChildOwners.Load(childSessionID); ok {
			candidate := owner.(dockerSessionCandidate)
			candidate.APIURL = strings.TrimRight(strings.TrimSpace(candidate.APIURL), "/")
			if candidate.APIURL != "" && !s.isSelfAPIURL(candidate.APIURL) {
				first = candidate.APIURL
				if !yield(candidate) {
					return
				}
			}
		}
		for _, candidate := range s.dockerSessionCandidates(ctx, childSessionID) {
			if candidate.APIURL == first {
				continue
			}
			if !yield(candidate) {
				return
			}
		}
	}
}

func (s *Server) dockerSessionCandidates(ctx context.Context, childSessionID string) []dockerSessionCandidate {
	candidates := make([]dockerSessionCandidate, 0)
	seen := map[string]struct{}{}
	addCandidate := func(candidate dockerSessionCandidate) {
		candidate.APIURL = strings.TrimRight(strings.TrimSpace(candidate.APIURL), "/")
		if candidate.APIURL == "" {
			return
		}
		if s.isSelfAPIURL(candidate.APIURL) {
			return
		}
		key := candidate.APIURL + "\x00" + strings.TrimSpace(candidate.ParentSessionID)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, candidate)
	}

	if owner, ok := s.dockerChildOwners.Load(childSessionID); ok {
		addCandidate(owner.(dockerSessionCandidate))
	}

	if sessions, err := s.sessionManager.List(); err == nil {
		for _, sess := range sessions {
			if sess == nil || sess.Metadata == nil {
				continue
			}
			for _, entry := range dockerChildSessionEntries(sess.Metadata["docker_child_sessions"]) {
				if metadataString(entry["child_session_id"]) != childSessionID {
					continue
				}
				parentID := metadataString(entry["parent_session_id"])
				if parentID == "" {
					parentID = sess.ID
				}
				addCandidate(dockerSessionCandidate{
					APIURL:          metadataString(entry["agent_api_url"]),
					AgentName:       metadataString(entry["agent_name"]),
					Runtime:         metadataString(entry["agent_runtime"]),
					ParentSessionID: parentID,
				})
			}
		}
	} else {
		logging.Debug("Docker session proxy: failed to scan parent session metadata: %v", err)
	}

	agents, err := listLocalBruteContainers(ctx)
	if err != nil {
		// Docker may be absent; host agents can still own child sessions.
		logging.Debug("Docker session proxy: failed to list local Brute containers: %v", err)
	}
	agents = append(agents, s.hostRuntime.list()...)
	for _, agent := range agents {
		if !agent.Running {
			continue
		}
		addCandidate(dockerSessionCandidate{
			APIURL:    agent.APIURL,
			AgentName: agent.Name,
			Runtime:   agent.Runtime,
		})
	}
	return candidates
}

func fetchDockerSession(ctx context.Context, client *nethttp.Client, candidate dockerSessionCandidate, sessionID string, includeMessages bool) (SessionResponse, error) {
	values := neturl.Values{}
	if !includeMessages {
		values.Set("include_messages", "false")
	}
	values.Set("include_metadata", "true")

	endpoint := strings.TrimRight(candidate.APIURL, "/") + "/sessions/" + neturl.PathEscape(sessionID)
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, endpoint, nil)
	if err != nil {
		return SessionResponse{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return SessionResponse{}, fmt.Errorf("%s: %w", candidate.APIURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SessionResponse{}, fmt.Errorf("%s returned %s", candidate.APIURL, resp.Status)
	}

	var sessionResp SessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&sessionResp); err != nil {
		return SessionResponse{}, fmt.Errorf("%s decode session: %w", candidate.APIURL, err)
	}
	if strings.TrimSpace(sessionResp.ID) != sessionID {
		return SessionResponse{}, fmt.Errorf("%s returned session %q for requested %q", candidate.APIURL, sessionResp.ID, sessionID)
	}
	return sessionResp, nil
}

func decorateDockerSessionResponse(resp *SessionResponse, candidate dockerSessionCandidate) {
	if resp == nil {
		return
	}
	if resp.ParentID == "" {
		if candidate.ParentSessionID != "" {
			resp.ParentID = candidate.ParentSessionID
		} else if parentID := metadataString(resp.Metadata["parent_session_id"]); parentID != "" {
			resp.ParentID = parentID
		}
	}
	if resp.Metadata == nil {
		resp.Metadata = map[string]interface{}{}
	}
	resp.Metadata["agent_runtime"] = firstNonEmptyLocalAgentString(candidate.Runtime, "docker")
	resp.Metadata["proxied_from_docker_agent"] = true
	if candidate.AgentName != "" {
		resp.Metadata["docker_agent_name"] = candidate.AgentName
	}
	if candidate.APIURL != "" {
		resp.Metadata["docker_agent_api_url"] = candidate.APIURL
	}
}

func dockerChildSessionEntries(raw interface{}) []map[string]interface{} {
	if raw == nil {
		return nil
	}
	switch value := raw.(type) {
	case []map[string]interface{}:
		return value
	case []interface{}:
		entries := make([]map[string]interface{}, 0, len(value))
		for _, item := range value {
			if entry, ok := item.(map[string]interface{}); ok {
				entries = append(entries, entry)
			}
		}
		return entries
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil
		}
		var entries []map[string]interface{}
		if err := json.Unmarshal(encoded, &entries); err != nil {
			return nil
		}
		return entries
	}
}

func (s *Server) isSelfAPIURL(rawURL string) bool {
	if s == nil || s.Port() <= 0 {
		return false
	}
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.Trim(parsed.Hostname(), "[]"))
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		return false
	}
	return port == s.Port()
}
