package tguser

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// Login states.
const (
	StateStarting         = "starting"
	StateCodeRequired     = "code_required"
	StatePasswordRequired = "password_required"
	StateDone             = "done"
	StateFailed           = "failed"
)

const loginTimeout = 5 * time.Minute

// Login is an in-progress interactive login driven step by step over HTTP.
type Login struct {
	phone string

	mu      sync.Mutex
	state   string
	err     string
	session []byte

	codeCh chan string
	passCh chan string
	cancel context.CancelFunc
}

// StartLogin begins a login; Telegram sends a code to the user's devices.
func StartLogin(apiID int, apiHash, phone string) *Login {
	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	l := &Login{
		phone:  phone,
		state:  StateStarting,
		codeCh: make(chan string, 1),
		passCh: make(chan string, 1),
		cancel: cancel,
	}
	go l.run(ctx, apiID, apiHash)
	return l
}

func (l *Login) setState(state, errMsg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.state, l.err = state, errMsg
}

// Status returns the current state, error text, and the session once done.
func (l *Login) Status() (state, errMsg string, sess []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state, l.err, l.session
}

// SubmitCode provides the login code.
func (l *Login) SubmitCode(code string) error { return submit(l.codeCh, code) }

// SubmitPassword provides the 2FA password.
func (l *Login) SubmitPassword(password string) error { return submit(l.passCh, password) }

// Cancel aborts the login.
func (l *Login) Cancel() { l.cancel() }

func submit(ch chan string, v string) error {
	select {
	case ch <- v:
		return nil
	default:
		return fmt.Errorf("a value is already pending")
	}
}

func (l *Login) wait(ctx context.Context, state string, ch chan string) (string, error) {
	l.setState(state, "")
	select {
	case v := <-ch:
		l.setState(StateStarting, "")
		return v, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (l *Login) run(ctx context.Context, apiID int, apiHash string) {
	defer l.cancel()
	mem := &session.StorageMemory{}
	client := telegram.NewClient(apiID, apiHash, telegram.Options{SessionStorage: mem})
	err := client.Run(ctx, func(ctx context.Context) error {
		return auth.NewFlow(&authenticator{l: l, ctx: ctx}, auth.SendCodeOptions{}).Run(ctx, client.Auth())
	})
	if err != nil {
		l.setState(StateFailed, err.Error())
		return
	}
	data, _ := mem.Bytes(nil)
	l.mu.Lock()
	l.session, l.state, l.err = data, StateDone, ""
	l.mu.Unlock()
}

type authenticator struct {
	l   *Login
	ctx context.Context
}

func (a *authenticator) Phone(context.Context) (string, error) { return a.l.phone, nil }

func (a *authenticator) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	return a.l.wait(ctx, StateCodeRequired, a.l.codeCh)
}

func (a *authenticator) Password(ctx context.Context) (string, error) {
	return a.l.wait(ctx, StatePasswordRequired, a.l.passCh)
}

func (a *authenticator) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return fmt.Errorf("this phone number has no Telegram account")
}

func (a *authenticator) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("this phone number has no Telegram account")
}
