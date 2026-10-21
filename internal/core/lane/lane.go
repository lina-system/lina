// SPDX-License-Identifier: Apache-2.0

// Package lane serializes turns within each conversation. Admission and turn
// claims remain durable in canon; a lane is only their in-process executor.
package lane

import (
	"context"
	"errors"
	"sync"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

var (
	ErrClosed  = errors.New("conversation lanes closed")
	ErrInvalid = errors.New("conversation and turn are required")
)

// Turn runs synchronously in its conversation's goroutine. It must propagate
// cancellation to its work and return only after that work has stopped.
type Turn func(context.Context) error

type request struct {
	ctx    context.Context
	turn   Turn
	result chan error
	inputs *loop.Inputs
}

type running struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	inputs *loop.Inputs
}
type conversation struct {
	queue  []request
	active *running
}

// Manager is shared by all conversations of one Core instance. Use New, and
// Close it before closing the storage or services used by its turns.
type Manager struct {
	mu      sync.Mutex
	lanes   map[string]*conversation
	ctx     context.Context
	cancel  context.CancelCauseFunc
	closed  bool
	workers sync.WaitGroup
	done    chan struct{}
}

func New() *Manager {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &Manager{lanes: make(map[string]*conversation), ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

// Submit queues an already admitted turn. Calls for one conversation run in
// submission order; different conversations can run concurrently. The buffered
// result receives exactly one error (nil on success), then closes. Abandoning
// a result does not block the lane. This is not a durable input acknowledgement.
// Input received during a turn must go through the turn's steering path, not
// be submitted as another turn here.
func (m *Manager) Submit(ctx context.Context, id string, turn Turn) (<-chan error, error) {
	return m.submit(ctx, id, turn, nil)
}

// SubmitLoop queues a turn with a fresh steering queue. The callback owns the
// queue's Close handoff, including failures before the model loop starts.
// Only already committed input may be passed to Steer. False leaves ownership
// with admission, which submits a follow-up instead of discarding the input.
func (m *Manager) SubmitLoop(ctx context.Context, id string, run func(context.Context, *loop.Inputs) error) (<-chan error, error) {
	if run == nil {
		return nil, ErrInvalid
	}
	inputs := &loop.Inputs{}
	return m.submit(ctx, id, func(ctx context.Context) error { return run(ctx, inputs) }, inputs)
}

func (m *Manager) submit(ctx context.Context, id string, turn Turn, inputs *loop.Inputs) (<-chan error, error) {
	if id == "" || turn == nil {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	job := request{ctx: ctx, turn: turn, result: make(chan error, 1), inputs: inputs}
	lane := m.lanes[id]
	if lane == nil {
		lane = &conversation{}
		m.lanes[id] = lane
		m.workers.Add(1)
		go m.run(id, lane)
	}
	lane.queue = append(lane.queue, job)
	return job.result, nil
}

func (m *Manager) run(id string, lane *conversation) {
	defer m.workers.Done()
	for {
		m.mu.Lock()
		if len(lane.queue) == 0 {
			delete(m.lanes, id)
			m.mu.Unlock()
			return
		}
		job := lane.queue[0]
		lane.queue[0] = request{}
		lane.queue = lane.queue[1:]
		ctx, cancel := context.WithCancelCause(job.ctx)
		lane.active = &running{ctx: ctx, cancel: cancel, inputs: job.inputs}
		m.mu.Unlock()
		err := m.execute(ctx, cancel, job)
		m.mu.Lock()
		lane.active = nil
		m.mu.Unlock()
		job.result <- err
		close(job.result)
	}
}

func (m *Manager) execute(ctx context.Context, cancel context.CancelCauseFunc, job request) error {
	defer cancel(nil)
	if err := context.Cause(m.ctx); err != nil {
		return err
	}
	stop := context.AfterFunc(m.ctx, func() { cancel(ErrClosed) })
	defer stop()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return job.turn(ctx)
}

// Steer snapshots an input for the active turn's next round. It never targets
// queued turns and refuses input after cancellation or the loop's Close.
func (m *Manager) Steer(id string, item provider.Item) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	lane := m.lanes[id]
	if m.closed || lane == nil || lane.active == nil || lane.active.ctx.Err() != nil {
		return false
	}
	return lane.active.inputs.Push(item)
}

// Stop cancels only the currently active turn, preserving the caller's typed
// cause. It does not release the lane until that turn and its tools return.
func (m *Manager) Stop(id string, cause error) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	lane := m.lanes[id]
	if cause == nil || lane == nil || lane.active == nil {
		return false
	}
	lane.active.cancel(cause)
	return true
}

// Close rejects submissions, cancels active turns and skips queued turns. It
// waits for all lane goroutines; a caller timeout does not declare them stopped.
// Close may be called again to wait after a timeout. No turn is retried.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel(ErrClosed)
		go func() { m.workers.Wait(); close(m.done) }()
	}
	m.mu.Unlock()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
