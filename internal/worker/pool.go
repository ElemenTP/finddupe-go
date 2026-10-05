// Package worker provides a bounded goroutine pool for parallel task execution.
package worker

import (
	"context"
	"runtime"
	"sync"
)

// Pool runs submitted functions in their own goroutines, never more than size at
// a time. Submit blocks while that many are running, which is the back-pressure
// the pipeline relies on; the pool itself is stateless between tasks.
type Pool struct {
	sem chan struct{}
	wg  sync.WaitGroup
}

// New creates a Pool with the given number of workers. A non-positive size falls
// back to the CPU count; callers that want a different default (the pipeline
// scales it for the mixed I/O and CPU workload) resolve it themselves.
func New(size int) *Pool {
	if size <= 0 {
		size = runtime.NumCPU()
	}
	return &Pool{
		sem: make(chan struct{}, size),
	}
}

// Submit enqueues a task for execution. This method blocks if all workers are busy.
// If ctx is already cancelled, the task is not executed and Submit returns false.
// If ctx is cancelled while waiting, the task is not executed and Submit returns false.
// The caller must check the return value: if false, the task was not submitted
// and any associated cleanup (e.g. WaitGroup.Done) is the caller's responsibility.
func (p *Pool) Submit(ctx context.Context, fn func(context.Context)) bool {
	select {
	case <-ctx.Done():
		return false
	default:
	}

	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return false
	}

	p.wg.Go(func() {
		defer func() { <-p.sem }()
		fn(ctx)
	})
	return true
}

// Wait blocks until all submitted tasks have completed.
func (p *Pool) Wait() {
	p.wg.Wait()
}
