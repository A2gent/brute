package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultHeaderTimeout     = time.Minute
	defaultStreamIdleTimeout = 2 * time.Minute
)

// ErrStreamIdleTimeout is returned by a streaming body that produced no complete line within the idle window.
var ErrStreamIdleTimeout = errors.New("llm stream idle timeout")

var (
	transportsMu sync.Mutex
	transports   = map[time.Duration]*http.Transport{}
)

// SharedHTTPTransport returns a process-wide pooled transport per response-header timeout (0 = default),
// so provider clients reuse connections instead of re-handshaking on every request.
func SharedHTTPTransport(headerTimeout time.Duration) *http.Transport {
	if headerTimeout <= 0 {
		headerTimeout = defaultHeaderTimeout
	}
	transportsMu.Lock()
	defer transportsMu.Unlock()
	if t, ok := transports[headerTimeout]; ok {
		return t
	}
	t := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          128,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
	}
	transports[headerTimeout] = t
	return t
}

func streamIdleTimeout() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("AAGENT_LLM_STREAM_IDLE_TIMEOUT")); err == nil && d > 0 {
		return d
	}
	return defaultStreamIdleTimeout
}

// DoStream sends req without the client's total Timeout (which would cut healthy long streams)
// and instead fails the body read when no complete line arrives within the idle window.
func DoStream(client *http.Client, req *http.Request) (*http.Response, error) {
	idle := streamIdleTimeout()
	ctx, cancel := context.WithCancel(req.Context())
	streaming := *client
	streaming.Timeout = 0
	resp, err := streaming.Do(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	body := &idleBody{rc: resp.Body, cancel: cancel, idle: idle}
	body.timer = time.AfterFunc(idle, func() {
		body.timedOut.Store(true)
		cancel()
	})
	resp.Body = body
	return resp, nil
}

type idleBody struct {
	rc       io.ReadCloser
	cancel   context.CancelFunc
	timer    *time.Timer
	idle     time.Duration
	timedOut atomic.Bool
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	// Only a completed line proves liveness; a stalled partial line must still time out.
	for _, c := range p[:n] {
		if c == '\n' {
			b.timer.Reset(b.idle)
			break
		}
	}
	if err != nil && b.timedOut.Load() {
		err = fmt.Errorf("%w: no data for %s", ErrStreamIdleTimeout, b.idle)
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	err := b.rc.Close()
	b.cancel()
	return err
}
