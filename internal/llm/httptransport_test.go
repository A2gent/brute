package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSharedHTTPTransport(t *testing.T) {
	transport := SharedHTTPTransport(0)
	if transport != SharedHTTPTransport(0) || transport != SharedHTTPTransport(time.Minute) {
		t.Fatal("default clients must share a connection pool")
	}
	if !transport.ForceAttemptHTTP2 || transport.MaxIdleConnsPerHost < 32 || transport.MaxIdleConns < 128 ||
		transport.IdleConnTimeout != 90*time.Second || transport.TLSHandshakeTimeout != 10*time.Second ||
		transport.ResponseHeaderTimeout != time.Minute {
		t.Fatalf("unexpected transport settings: %+v", transport)
	}
	custom := SharedHTTPTransport(20 * time.Millisecond)
	t.Cleanup(custom.CloseIdleConnections)
	if custom != SharedHTTPTransport(20*time.Millisecond) || custom == transport {
		t.Fatal("custom header timeouts must reuse their own pool without changing the default")
	}
}

func TestDoStreamOutlivesClientTimeout(t *testing.T) {
	t.Setenv("AAGENT_LLM_STREAM_IDLE_TIMEOUT", "250ms")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 8; i++ {
			io.WriteString(w, ": heartbeat\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	// Scale the former multi-minute total timeout down so this regression stays fast.
	client := &http.Client{Transport: SharedHTTPTransport(0), Timeout: 50 * time.Millisecond}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	resp, err := DoStream(client, req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("healthy stream was cut short: %q, %v", body, err)
	}
	if client.Timeout != 50*time.Millisecond {
		t.Fatal("streaming must not mutate the non-streaming client")
	}
}

func TestDoStreamIdleTimeout(t *testing.T) {
	for _, partialLine := range []bool{false, true} {
		t.Run(map[bool]string{false: "headers_only", true: "bytes_without_line"}[partialLine], func(t *testing.T) {
			t.Setenv("AAGENT_LLM_STREAM_IDLE_TIMEOUT", "40ms")
			canceled := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.(http.Flusher).Flush()
				if partialLine {
					io.WriteString(w, "data: unfinished")
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
				close(canceled)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			started := time.Now()
			resp, err := DoStream(&http.Client{Transport: SharedHTTPTransport(0)}, req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			_, err = io.ReadAll(resp.Body)
			if !errors.Is(err, ErrStreamIdleTimeout) || !strings.Contains(err.Error(), "40ms") {
				t.Fatalf("expected descriptive idle timeout, got %v", err)
			}
			if time.Since(started) > 500*time.Millisecond || ctx.Err() != nil {
				t.Fatal("watchdog must fail fast without canceling the caller")
			}
			select {
			case <-canceled:
			case <-ctx.Done():
				t.Fatal("watchdog did not cancel the HTTP request")
			}
		})
	}
}

func TestDoStreamHeaderTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	_, err := DoStream(&http.Client{Transport: SharedHTTPTransport(20 * time.Millisecond)}, req)
	if err == nil || !strings.Contains(err.Error(), "timeout awaiting response headers") || ctx.Err() != nil {
		t.Fatalf("expected header timeout, got %v", err)
	}
}

func TestDoStreamPreservesCallerCancellation(t *testing.T) {
	t.Setenv("AAGENT_LLM_STREAM_IDLE_TIMEOUT", "1s")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	resp, err := DoStream(&http.Client{Transport: SharedHTTPTransport(0)}, req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	cancel()
	_, err = io.ReadAll(resp.Body)
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrStreamIdleTimeout) {
		t.Fatalf("caller cancellation lost: %v", err)
	}
}
