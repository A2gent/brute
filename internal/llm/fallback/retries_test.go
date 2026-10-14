package fallback

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2gent/brute/internal/llm"
)

type retryStreamLLM struct{ countingLLM }

func (c *retryStreamLLM) ChatStream(ctx context.Context, req *llm.ChatRequest, _ func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	return c.Chat(ctx, req)
}

func TestSwitchTraceActualAttempts(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			failures int
			err      error
			attempts int
		}{
			{"auth_first", 0, errors.New("authentication failed: 401 unauthorized"), 1},
			{"auth_after_retry", 1, errors.New("authentication failed: 401 unauthorized"), 2},
			{"unsafe_auth", 0, llm.UnsafeForRetry(errors.New("Failed to authenticate: OAuth session expired")), 1},
			{"exhausted", 0, errors.New("service unavailable: 503"), 4},
		} {
			name := "chat/"
			if streaming {
				name = "stream/"
			}
			t.Run(name+tc.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					primary := &attemptLLM{failures: tc.failures, err: tc.err}
					var source llm.Client = primary
					if streaming {
						source = &attemptStreamLLM{primary}
					}
					secondary := &countingLLM{}
					client := NewClient([]Node{{Name: "primary", Client: source}, {Name: "secondary", Client: secondary}})
					start := time.Now()
					var switches []llm.StreamEvent
					_, err := client.ChatStream(context.Background(), &llm.ChatRequest{}, func(ev llm.StreamEvent) error {
						if ev.Phase == "switching_provider" {
							switches = append(switches, ev)
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					if primary.calls != tc.attempts || secondary.calls != 1 {
						t.Fatalf("calls = %d/%d, want %d/1", primary.calls, secondary.calls, tc.attempts)
					}
					if len(switches) != 1 {
						t.Fatalf("switch traces = %d, want 1", len(switches))
					}
					ev := switches[0]
					if ev.Attempt != primary.calls || ev.MaxAttempts != 4 || ev.FallbackTo != "secondary" {
						t.Errorf("incorrect switching trace: %+v", ev)
					}
					if tc.attempts == 1 && time.Since(start) != 0 {
						t.Error("immediate fallback must not wait for retry backoff")
					}
				})
			})
		}
	}
}

type attemptLLM struct {
	calls, failures int
	err             error
}

func (c *attemptLLM) Chat(context.Context, *llm.ChatRequest) (*llm.ChatResponse, error) {
	c.calls++
	if c.calls <= c.failures {
		return nil, errors.New("service unavailable: 503")
	}
	return nil, c.err
}

type attemptStreamLLM struct{ *attemptLLM }

func (c *attemptStreamLLM) ChatStream(ctx context.Context, req *llm.ChatRequest, _ func(llm.StreamEvent) error) (*llm.ChatResponse, error) {
	return c.Chat(ctx, req)
}

func TestRetryCancellationDoesNotSwitch(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "chat", true: "stream"}[stream], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				primary := &retryStreamLLM{countingLLM: countingLLM{err: errors.New("service unavailable: 503")}}
				secondary := &countingLLM{}
				client := NewClient([]Node{{Name: "primary", Client: primary}, {Name: "secondary", Client: secondary}})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				go func() { time.Sleep(time.Second); cancel() }()
				var err error
				if stream {
					_, err = client.ChatStream(ctx, &llm.ChatRequest{}, func(ev llm.StreamEvent) error {
						if ev.Phase == "switching_provider" {
							t.Error("cancellation emitted switch")
						}
						return nil
					})
				} else {
					_, err = client.Chat(ctx, &llm.ChatRequest{})
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v, want cancellation", err)
				}
				if primary.calls != 1 || secondary.calls != 0 {
					t.Fatalf("calls = %d/%d, want 1/0", primary.calls, secondary.calls)
				}
			})
		})
	}
}
