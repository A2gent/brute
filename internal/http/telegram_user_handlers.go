package http

// Telegram user-account (MTProto) integration: interactive login and chat discovery.
// Reading messages for agents lives in tools/integrationtools/telegram_user_read.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/A2gent/brute/internal/storage"
	"github.com/A2gent/brute/internal/tguser"
	"github.com/A2gent/brute/internal/tools/integrationtools"
	"github.com/go-chi/chi/v5"
)

type telegramUserLoginRegistry struct {
	mu     sync.Mutex
	logins map[string]*tguser.Login
}

func (r *telegramUserLoginRegistry) get(id string) *tguser.Login {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.logins[id]
}

func (r *telegramUserLoginRegistry) put(id string, l *tguser.Login) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.logins == nil {
		r.logins = map[string]*tguser.Login{}
	}
	if old := r.logins[id]; old != nil {
		old.Cancel()
	}
	r.logins[id] = l
}

func (r *telegramUserLoginRegistry) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.logins, id)
}

type telegramUserLoginStatusResponse struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

func (s *Server) telegramUserIntegration(w http.ResponseWriter, r *http.Request) *storage.Integration {
	integration, err := s.store.GetIntegration(chi.URLParam(r, "integrationID"))
	if err != nil || integration == nil {
		s.errorResponse(w, http.StatusNotFound, "Integration not found")
		return nil
	}
	if integration.Provider != integrationtools.TelegramUserProvider {
		s.errorResponse(w, http.StatusBadRequest, "Integration is not a telegram_user integration")
		return nil
	}
	return integration
}

func (s *Server) handleTelegramUserLoginStart(w http.ResponseWriter, r *http.Request) {
	integration := s.telegramUserIntegration(w, r)
	if integration == nil {
		return
	}
	var req struct {
		Phone string `json:"phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Phone) == "" {
		s.errorResponse(w, http.StatusBadRequest, "phone is required (international format, e.g. +491234567890)")
		return
	}
	creds, err := integrationtools.TelegramUserCredentials(integration)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	s.telegramUserLogins.put(integration.ID, tguser.StartLogin(creds.APIID, creds.APIHash, strings.TrimSpace(req.Phone)))
	s.jsonResponse(w, http.StatusAccepted, telegramUserLoginStatusResponse{State: tguser.StateStarting})
}

func (s *Server) handleTelegramUserLoginCode(w http.ResponseWriter, r *http.Request) {
	s.telegramUserSubmit(w, r, "code", func(l *tguser.Login, v string) error { return l.SubmitCode(v) })
}

func (s *Server) handleTelegramUserLoginPassword(w http.ResponseWriter, r *http.Request) {
	s.telegramUserSubmit(w, r, "password", func(l *tguser.Login, v string) error { return l.SubmitPassword(v) })
}

func (s *Server) telegramUserSubmit(w http.ResponseWriter, r *http.Request, field string, submit func(*tguser.Login, string) error) {
	integration := s.telegramUserIntegration(w, r)
	if integration == nil {
		return
	}
	login := s.telegramUserLogins.get(integration.ID)
	if login == nil {
		s.errorResponse(w, http.StatusConflict, "No login in progress; start one first")
		return
	}
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body[field]) == "" {
		s.errorResponse(w, http.StatusBadRequest, field+" is required")
		return
	}
	if err := submit(login, strings.TrimSpace(body[field])); err != nil {
		s.errorResponse(w, http.StatusConflict, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusAccepted, telegramUserLoginStatusResponse{State: tguser.StateStarting})
}

// handleTelegramUserLoginStatus reports progress and persists the session once login completes.
func (s *Server) handleTelegramUserLoginStatus(w http.ResponseWriter, r *http.Request) {
	integration := s.telegramUserIntegration(w, r)
	if integration == nil {
		return
	}
	login := s.telegramUserLogins.get(integration.ID)
	if login == nil {
		state := "logged_out"
		if strings.TrimSpace(integration.Config["session"]) != "" {
			state = tguser.StateDone
		}
		s.jsonResponse(w, http.StatusOK, telegramUserLoginStatusResponse{State: state})
		return
	}
	state, errMsg, sess := login.Status()
	switch state {
	case tguser.StateDone:
		integrationtools.TelegramUserSetSession(integration, sess)
		integration.UpdatedAt = time.Now()
		if err := s.store.SaveIntegration(integration); err != nil {
			s.errorResponse(w, http.StatusInternalServerError, "Failed to save session: "+err.Error())
			return
		}
		s.telegramUserLogins.remove(integration.ID)
	case tguser.StateFailed:
		s.telegramUserLogins.remove(integration.ID)
	}
	s.jsonResponse(w, http.StatusOK, telegramUserLoginStatusResponse{State: state, Error: errMsg})
}

type telegramUserDialogResponse struct {
	tguser.Dialog
	Allowed bool `json:"allowed"`
}

// handleTelegramUserDialogs lists all chats of the account so the user can choose the allowlist.
func (s *Server) handleTelegramUserDialogs(w http.ResponseWriter, r *http.Request) {
	integration := s.telegramUserIntegration(w, r)
	if integration == nil {
		return
	}
	creds, err := integrationtools.TelegramUserCredentials(integration)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	dialogs, sess, err := tguser.ListDialogs(ctx, creds)
	if len(sess) > 0 {
		if fresh, getErr := s.store.GetIntegration(integration.ID); getErr == nil && fresh != nil {
			integrationtools.TelegramUserSetSession(fresh, sess)
			_ = s.store.SaveIntegration(fresh)
		}
	}
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, err.Error())
		return
	}
	allowed := integrationtools.TelegramUserAllowedChats(integration)
	out := make([]telegramUserDialogResponse, 0, len(dialogs))
	for _, d := range dialogs {
		_, ok := allowed[d.ID]
		out = append(out, telegramUserDialogResponse{Dialog: d, Allowed: ok})
	}
	s.jsonResponse(w, http.StatusOK, out)
}

func (s *Server) testTelegramUserIntegration(ctx context.Context, integration *storage.Integration) (bool, string) {
	creds, err := integrationtools.TelegramUserCredentials(integration)
	if err != nil {
		return false, err.Error()
	}
	if len(creds.Session) == 0 {
		return false, "Not logged in yet. Complete the Telegram login first."
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	dialogs, _, err := tguser.ListDialogs(ctx, creds)
	if err != nil {
		return false, "Telegram user session check failed: " + err.Error()
	}
	return true, fmt.Sprintf("Connected. Visible chats: %d", len(dialogs))
}

// handleTelegramUserAvatars returns small avatars (data URLs) keyed by chat id.
func (s *Server) handleTelegramUserAvatars(w http.ResponseWriter, r *http.Request) {
	integration := s.telegramUserIntegration(w, r)
	if integration == nil {
		return
	}
	var req struct {
		ChatIDs []string `json:"chat_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.ChatIDs) == 0 {
		s.errorResponse(w, http.StatusBadRequest, "chat_ids is required")
		return
	}
	creds, err := integrationtools.TelegramUserCredentials(integration)
	if err != nil {
		s.errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	avatars, sess, err := tguser.Avatars(ctx, creds, req.ChatIDs)
	if len(sess) > 0 {
		if fresh, getErr := s.store.GetIntegration(integration.ID); getErr == nil && fresh != nil {
			integrationtools.TelegramUserSetSession(fresh, sess)
			_ = s.store.SaveIntegration(fresh)
		}
	}
	if err != nil {
		s.errorResponse(w, http.StatusBadGateway, err.Error())
		return
	}
	s.jsonResponse(w, http.StatusOK, avatars)
}
