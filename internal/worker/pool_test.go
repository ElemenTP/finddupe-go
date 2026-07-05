package worker_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"finddupe/internal/worker"
)

func TestPool_AllTasksExecute(t *testing.T) {
	t.Parallel()
	p := worker.New(4)
	var count atomic.Int32

	for range 10 {
		if !p.Submit(context.Background(), func(_ context.Context) {
			count.Add(1)
		}) {
			t.Error("Submit returned false with live context")
		}
	}
	p.Wait()

	if n := count.Load(); n != 10 {
		t.Errorf("expected 10 tasks executed, got %d", n)
	}
}

func TestPool_Bounded(t *testing.T) {
	t.Parallel()
	const size = 2
	p := worker.New(size)
	var active atomic.Int32
	var maxActive atomic.Int32

	for range 10 {
		if !p.Submit(context.Background(), func(_ context.Context) {
			n := active.Add(1)
			if n > maxActive.Load() {
				maxActive.Store(n)
			}
			time.Sleep(10 * time.Millisecond)
			active.Add(-1)
		}) {
			t.Error("Submit returned false with live context")
		}
	}
	p.Wait()

	if maxActive.Load() > int32(size) {
		t.Errorf("expected at most %d concurrent, got %d", size, maxActive.Load())
	}
}

func TestPool_ContextCancellation(t *testing.T) {
	t.Parallel()
	p := worker.New(2)
	var count atomic.Int32

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	// Submit should return false when context is already cancelled.
	if p.Submit(ctx, func(_ context.Context) {
		count.Add(1)
	}) {
		t.Error("expected Submit to return false with cancelled context")
	}
	p.Wait()

	if count.Load() != 0 {
		t.Errorf("expected 0 tasks after cancel, got %d", count.Load())
	}
}

func TestPool_WaitBlocks(t *testing.T) {
	t.Parallel()
	p := worker.New(4)
	var done atomic.Int32

	if !p.Submit(context.Background(), func(_ context.Context) {
		time.Sleep(50 * time.Millisecond)
		done.Store(1)
	}) {
		t.Error("Submit returned false with live context")
	}

	// Wait should block until task completes.
	p.Wait()

	if done.Load() != 1 {
		t.Error("expected task to complete after Wait()")
	}
}

func TestPool_ZeroSize(t *testing.T) {
	t.Parallel()
	p := worker.New(0) // Should default to runtime.NumCPU().
	var count atomic.Int32

	if !p.Submit(context.Background(), func(_ context.Context) {
		count.Add(1)
	}) {
		t.Error("Submit returned false with live context")
	}
	p.Wait()

	if count.Load() != 1 {
		t.Errorf("expected 1 task executed, got %d", count.Load())
	}
}
