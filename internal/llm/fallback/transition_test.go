package fallback

import (
	"context"
	"errors"
	"github.com/A2gent/brute/internal/llm"
	"testing"
)

func TestTransitionBoundaryNeverDisclosesTarget(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "chat", true: "stream"}[stream], func(t *testing.T) {
			a := &countingLLM{err: errors.New("503 unavailable")}
			b := &countingLLM{}
			c := NewClient([]Node{{Name: "a", Model: "one", Client: a}, {Name: "b", Model: "two", Client: b}}, WithMaxRetries(0))
			ctx := WithSessionBoundary(context.Background())
			var err error
			if stream {
				_, err = c.ChatStream(ctx, &llm.ChatRequest{}, nil)
			} else {
				_, err = c.Chat(ctx, &llm.ChatRequest{})
			}
			var next *TransitionRequired
			if !errors.As(err, &next) || next.Source.Name != "a" || next.Target.Name != "b" || next.Index != 1 {
				t.Fatalf("boundary: %#v %v", next, err)
			}
			if b.calls != 0 || c.currentIndex() != 0 {
				t.Fatal("target contacted or advanced before commit")
			}
			c.CommitTransition(next.Index)
			_, err = c.Chat(ctx, &llm.ChatRequest{})
			if err != nil || b.calls != 1 {
				t.Fatalf("resume %v %d", err, b.calls)
			}
		})
	}
}
func TestTransitionBoundarySkipAndUnsafe(t *testing.T) {
	for _, unsafe := range []bool{false, true} {
		t.Run(map[bool]string{false: "skip", true: "unsafe"}[unsafe], func(t *testing.T) {
			a := &countingLLM{err: llm.UnsafeForRetry(errors.New("native tool may have executed"))}
			b := &countingLLM{}
			c := NewClient([]Node{{Name: "a", Client: a}, {Name: "b", Client: b}}, WithMaxRetries(0), WithNodeSkipper(func(context.Context, Node) (bool, string) { return !unsafe, "quota" }))
			_, err := c.Chat(WithSessionBoundary(context.Background()), &llm.ChatRequest{})
			var next *TransitionRequired
			if errors.As(err, &next) == unsafe || b.calls != 0 {
				t.Fatalf("unsafe=%v err=%v calls=%d", unsafe, err, b.calls)
			}
		})
	}
}
