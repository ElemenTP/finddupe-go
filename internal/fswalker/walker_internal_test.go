package fswalker

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestSendResult covers the rule that keeps a walker goroutine from parking on a
// send after its consumer has gone away.
func TestSendResult(t *testing.T) {
	t.Parallel()

	t.Run("delivers when the consumer reads", func(t *testing.T) {
		t.Parallel()

		ch := make(chan Result, 1)
		if !sendResult(context.Background(), ch, Result{}) {
			t.Fatal("sendResult reported failure for a readable channel")
		}
	})

	t.Run("abandons the send when the walk is cancelled", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		ch := make(chan Result) // nobody will ever read
		done := make(chan bool, 1)

		go func() {
			done <- sendResult(ctx, ch, Result{Err: errors.New("walk error")})
		}()
		cancel()

		select {
		case delivered := <-done:
			if delivered {
				t.Error("sendResult delivered a result nobody read")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("sendResult blocked after cancellation")
		}
	})
}
