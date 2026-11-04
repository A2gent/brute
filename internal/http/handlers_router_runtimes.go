package http

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/A2gent/brute/internal/config"
)

const routerRuntimeSessionProbeTimeout = 3 * time.Second

type routerRuntime struct {
	AgentID         string `json:"agent_id"`
	RuntimeName     string `json:"runtime_name"`
	RunningSessions int    `json:"running_sessions"`
	SessionsKnown   bool   `json:"sessions_known"`
}

type restartRouterRuntimesRequest struct {
	Router   string   `json:"router"`
	AgentIDs []string `json:"agent_ids"`
	Force    bool     `json:"force"`
}

// routerFromFingerprint reads the router id and provider back from a runtime's
// llm fingerprint label ("provider|model|router", older ones lack the router part), which older runtimes already carry.
func routerFromFingerprint(fingerprint string) (provider string, router string) {
	parts := strings.Split(fingerprint, "|")
	if len(parts) < 2 {
		return "", ""
	}
	if len(parts) == 2 {
		return parts[0], ""
	}
	return parts[0], parts[len(parts)-1]
}

func runtimeUsesRouter(agent LocalDockerAgent, routerID string) bool {
	provider, router := routerFromFingerprint(agent.Labels[dockerRuntimeLLMFingerprintLabelKey])
	if routerID == "" {
		return router == "" && provider == "automatic_router"
	}
	return config.NormalizeToken(router) == routerID
}

func countRunningSessions(ctx context.Context, apiURL string) (int, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, routerRuntimeSessionProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, strings.TrimRight(apiURL, "/")+"/sessions", nil)
	if err != nil {
		return 0, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	var items []struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return 0, false
	}
	running := 0
	for _, item := range items {
		if strings.EqualFold(item.Status, "running") || strings.EqualFold(item.Status, "waiting_external") {
			running++
		}
	}
	return running, true
}

// routerRuntimes lists live runtimes that were started with the given router
// ("" means the default one) together with how many sessions each is running.
func (s *Server) routerRuntimes(ctx context.Context, routerID string) []routerRuntime {
	agents := append([]LocalDockerAgent{}, s.hostRuntime.list()...)
	if containers, err := listLocalBruteContainers(ctx); err == nil {
		agents = append(agents, containers...)
	}
	result := []routerRuntime{}
	for _, agent := range agents {
		defID := strings.TrimSpace(agent.Labels[dockerRuntimeAgentDefLabelKey])
		if defID == "" || !agent.Running || !runtimeUsesRouter(agent, routerID) {
			continue
		}
		if agent.Labels[dockerRuntimeRouterRulesLabelKey] == s.routerRulesHash(routerID) {
			continue
		}
		item := routerRuntime{AgentID: defID, RuntimeName: agent.Name}
		if agent.APIURL != "" {
			item.RunningSessions, item.SessionsKnown = countRunningSessions(ctx, agent.APIURL)
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AgentID < result[j].AgentID })
	return result
}

func (s *Server) handleListRouterRuntimes(w http.ResponseWriter, r *http.Request) {
	routerID := config.NormalizeToken(r.URL.Query().Get("router"))
	s.jsonResponse(w, http.StatusOK, s.routerRuntimes(r.Context(), routerID))
}

// handleRestartRouterRuntimes stops runtimes so the next delegation recreates them
// with the current router rules. Busy runtimes are skipped unless force is set.
func (s *Server) handleRestartRouterRuntimes(w http.ResponseWriter, r *http.Request) {
	var req restartRouterRuntimesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	selected := map[string]bool{}
	for _, id := range req.AgentIDs {
		selected[strings.TrimSpace(id)] = true
	}
	restarted := []string{}
	skipped := []string{}
	for _, rt := range s.routerRuntimes(r.Context(), config.NormalizeToken(req.Router)) {
		if len(selected) > 0 && !selected[rt.AgentID] {
			continue
		}
		busy := rt.RunningSessions > 0 || !rt.SessionsKnown
		if busy && !req.Force {
			skipped = append(skipped, rt.AgentID)
			continue
		}
		s.removeManagedContainersForAgentDefinition(r.Context(), rt.AgentID, "")
		restarted = append(restarted, rt.AgentID)
	}
	s.jsonResponse(w, http.StatusOK, map[string]interface{}{"restarted": restarted, "skipped": skipped})
}
