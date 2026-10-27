package http

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/A2gent/brute/internal/websitecredentials"
	"github.com/go-chi/chi/v5"
)

const websiteCredentialsMaxBody = 64 << 10

type websiteCredentialRequest struct {
	URL      string `json:"url"`
	Login    string `json:"login"`
	Password string `json:"password"`
}

func (s *Server) registerWebsiteCredentialRoutes(r chi.Router) {
	r.Route("/secrets", func(r chi.Router) {
		r.Use(s.websiteCredentialsLocalOnly)
		r.Get("/", s.handleListWebsiteCredentials)
		r.Post("/", s.handleSaveWebsiteCredential)
		r.Put("/{id}", s.handleSaveWebsiteCredential)
		r.Delete("/{id}", s.handleDeleteWebsiteCredential)
	})
}

// websiteCredentialsLocalOnly is defense in depth for the vault routes only. It
// does not authenticate the API: the rest of this listener (sessions, settings)
// remains a trusted local-control surface with the configured CORS policy.
//   - Peer: the socket RemoteAddr must be loopback; X-Forwarded-* is ignored,
//     so Square/proxy-mode Caesar cannot reach /secrets.
//   - Host must name loopback, so DNS-rebound attacker hostnames are rejected.
//   - Browser Origin must be local Caesar (Vite :1420, Tauri), the API's own
//     origin, or an exact non-wildcard cors_allowed_origins entry. "null" and
//     every other site are rejected, even under the default "*" CORS policy.
//   - Requests without Origin (curl, tools) are allowed unless the browser marks
//     them cross-site, and POST/PUT require application/json so HTML forms and
//     other CORS-simple requests cannot mutate the vault.
//
// Denials happen before the vault is opened.
func (s *Server) websiteCredentialsLocalOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.websiteCredentialsRequestAllowed(r) {
			w.Header().Set("Cache-Control", "no-store")
			s.errorResponse(w, http.StatusForbidden, "Website credentials are only available to local Caesar")
			return
		}
		next.ServeHTTP(w, r)
	})
}

var websiteCredentialsTauriOrigins = map[string]bool{
	"tauri://localhost":       true,
	"http://tauri.localhost":  true,
	"https://tauri.localhost": true,
}

func isLoopbackHostname(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) websiteCredentialsRequestAllowed(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if !isLoopbackRemoteAddr(r.RemoteAddr) || !isLoopbackHostname(host) {
		return false
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			return false
		}
	}
	origin := strings.ToLower(strings.TrimSpace(r.Header.Get("Origin")))
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	if origin == "null" {
		return false
	}
	if websiteCredentialsTauriOrigins[origin] {
		return true
	}
	if u, err := url.Parse(origin); err == nil && u.User == nil && u.Path == "" && isLoopbackHostname(u.Hostname()) {
		ownScheme := "http"
		if r.TLS != nil {
			ownScheme = "https"
		}
		if (u.Scheme == "http" || u.Scheme == "https") && u.Port() == "1420" {
			return true
		}
		if u.Scheme == ownScheme && strings.EqualFold(u.Host, r.Host) {
			return true
		}
	}
	for _, allowed := range s.config.EffectiveCORSAllowedOrigins() {
		if !strings.Contains(allowed, "*") && strings.EqualFold(strings.TrimSpace(allowed), origin) {
			return true
		}
	}
	return false
}

func (s *Server) websiteCredentialError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "Website credential storage unavailable"
	if errors.Is(err, websitecredentials.ErrNotFound) {
		status, message = http.StatusNotFound, "Website credential not found"
	} else if errors.Is(err, websitecredentials.ErrInvalid) {
		status, message = http.StatusBadRequest, websitecredentials.ErrInvalid.Error()
	}
	s.errorResponse(w, status, message)
}

func (s *Server) handleListWebsiteCredentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	records, err := s.websiteCredentials.List()
	if err != nil {
		s.websiteCredentialError(w, err)
		return
	}
	s.jsonResponse(w, http.StatusOK, records)
}

func (s *Server) handleSaveWebsiteCredential(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, websiteCredentialsMaxBody)
	decoder := json.NewDecoder(r.Body)
	var req websiteCredentialRequest
	err := decoder.Decode(&req)
	if err == nil {
		var extra any
		if endErr := decoder.Decode(&extra); endErr != io.EOF {
			if endErr == nil {
				err = errors.New("trailing JSON")
			} else {
				err = endErr
			}
		}
	}
	if err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		// Never echo decoder errors: malformed input can contain a raw password.
		s.errorResponse(w, status, "Invalid website credential request")
		return
	}
	var metadata websitecredentials.Metadata
	status := http.StatusOK
	if r.Method == http.MethodPost {
		metadata, err = s.websiteCredentials.Create(req.URL, req.Login, req.Password)
		status = http.StatusCreated
	} else {
		metadata, err = s.websiteCredentials.Update(chi.URLParam(r, "id"), req.URL, req.Login, req.Password)
	}
	if err != nil {
		s.websiteCredentialError(w, err)
		return
	}
	s.jsonResponse(w, status, metadata)
}

func (s *Server) handleDeleteWebsiteCredential(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := s.websiteCredentials.Delete(chi.URLParam(r, "id")); err != nil {
		s.websiteCredentialError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
