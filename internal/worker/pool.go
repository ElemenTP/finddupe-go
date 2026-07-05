// Package worker provides a bounded goroutine pool for parallel task execution.
package worker

import (
	"context"
	"runtime"
	"sync"
)

// Pool manages a fixed set of goroutines.
// Submit blocks when all workers are busy, providing natural back-pressure.
type Pool struct {
	sem chan struct{}
	wg  sync.WaitGroup
}

// New creates a Pool with the given number of workers.
// If size is <= 0, [runtime.NumCPU] is used.
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

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer func() { <-p.sem }()
		fn(ctx)
	}()
	return true
}

// Wait blocks until all submitted tasks have completed.
func (p *Pool) Wait() {
	p.wg.Wait()
}
