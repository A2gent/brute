package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/A2gent/brute/internal/config"
	"github.com/go-chi/chi/v5"
)

type namedRouterRequest struct {
	Name                  string              `json:"name"`
	RouterProvider        string              `json:"router_provider"`
	RouterModel           string              `json:"router_model"`
	RouterReasoningEffort string              `json:"router_reasoning_effort"`
	RouterRules           []config.RouterRule `json:"router_rules"`
}

func (s *Server) routersReadOnly(w http.ResponseWriter) bool {
	if strings.TrimSpace(os.Getenv("A2GENT_PARENT_PROXY_URL")) == "" {
		return false
	}
	s.errorResponse(w, http.StatusForbidden, "Router settings are managed by the parent agent in Docker safe mode")
	return true
}

func (s *Server) buildNamedRouter(id string, req namedRouterRequest) (config.NamedRouter, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return config.NamedRouter{}, fmt.Errorf("name is required")
	}
	router := config.NamedRouter{
		ID:                    id,
		Name:                  name,
		RouterProvider:        config.NormalizeProviderRef(req.RouterProvider),
		RouterModel:           strings.TrimSpace(req.RouterModel),
		RouterReasoningEffort: strings.TrimSpace(req.RouterReasoningEffort),
	}
	router.RouterRules = req.RouterRules
	if err := s.validateAutoRouterProvider(router.AsProvider()); err != nil {
		return config.NamedRouter{}, err
	}
	router.RouterRules, _ = s.normalizeAndValidateRouterRules(req.RouterRules)
	return router, nil
}

func (s *Server) handleListRouters(w http.ResponseWriter, r *http.Request) {
	routers := s.config.Routers
	if routers == nil {
		routers = []config.NamedRouter{}
	}
	s.jsonResponse(w, http.StatusOK, routers)
}

func (s *Server) handleCreateRouter(w http.ResponseWriter, r *http.Request) {
	if s.routersReadOnly(w) {
		return
	}
	var req namedRouterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	id := config.NormalizeToken(req.Name)
	if id == "" {
		id = "router"
	}
	baseID := id
	for suffix := 2; ; suffix++ {
		if _, exists := s.config.FindRouter(id); !exists {
			break
		}
		id = fmt.Sprintf("%s-%d", baseID, suffix)
	}
	router, err := s.buildNamedRouter(id, req)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	s.config.Routers = append(s.config.Routers, router)
	if err := s.config.Save(config.GetConfigPath()); err != nil {
		s.errorResponse(w, http.StatusInternalServerError, "Failed to save router: "+err.Error())
		return
	}
	s.jsonResponse(w, http.StatusCreated, router)
}

func (s *Server) handleUpdateRouter(w http.ResponseWriter, r *http.Request) {
	if s.routersReadOnly(w) {
		return
	}
	id := config.NormalizeToken(chi.URLParam(r, "routerID"))
	var req namedRouterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	for i := range s.config.Routers {
		if config.NormalizeToken(s.config.Routers[i].ID) != id {
			continue
		}
		router, err := s.buildNamedRouter(s.config.Routers[i].ID, req)
		if err != nil {
			s.errorResponse(w, http.StatusBadRequest, err.Error())
			return
		}
		s.config.Routers[i] = router
		if err := s.config.Save(config.GetConfigPath()); err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to save router: "+err.Error())
			return
		}
		s.jsonResponse(w, http.StatusOK, router)
		return
	}
	s.errorResponse(w, http.StatusNotFound, "Router not found: "+id)
}

func (s *Server) handleDeleteRouter(w http.ResponseWriter, r *http.Request) {
	if s.routersReadOnly(w) {
		return
	}
	id := config.NormalizeToken(chi.URLParam(r, "routerID"))
	for i := range s.config.Routers {
		if config.NormalizeToken(s.config.Routers[i].ID) != id {
			continue
		}
		s.config.Routers = append(s.config.Routers[:i], s.config.Routers[i+1:]...)
		if err := s.config.Save(config.GetConfigPath()); err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to save routers: "+err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.errorResponse(w, http.StatusNotFound, "Router not found: "+id)
}
