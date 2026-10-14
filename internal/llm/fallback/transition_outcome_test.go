package fallback

import (
	"context"
	"errors"
	"github.com/A2gent/brute/internal/llm"
	"testing"
)

func TestTransitionReportsActualAttemptsNotRetryBudget(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, skip := range []bool{false, true} {
			a := &countingLLM{err: errors.New("insufficient_quota")}
			b := &countingLLM{}
			c := NewClient([]Node{{Name: "a", Client: a}, {Name: "b", Client: b}}, WithMaxRetries(5), WithNodeSkipper(func(context.Context, Node) (bool, string) { return skip, "quota" }))
			var err error
			if stream {
				_, err = c.ChatStream(WithSessionBoundary(context.Background()), &llm.ChatRequest{}, nil)
			} else {
				_, err = c.Chat(WithSessionBoundary(context.Background()), &llm.ChatRequest{})
			}
			var next *TransitionRequired
			if !errors.As(err, &next) {
				t.Fatalf("missing boundary: %v", err)
			}
			expected := "failed"
			if skip {
				expected = "skipped_usage_limit"
			}
			if next.SourceOutcome != expected || next.RetryCount != 0 {
				t.Fatalf("stream=%v skip=%v: %+v", stream, skip, next)
			}
			if b.calls != 0 {
				t.Fatal("selected target was called")
			}
		}
	}
}
