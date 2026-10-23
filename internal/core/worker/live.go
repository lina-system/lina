// SPDX-License-Identifier: Apache-2.0
package worker

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrSession marks an incomplete live-session record. A session without an
// identity, adapter and close operation cannot be ended later and is refused.
var ErrSession = errors.New("invalid worker session")

// Session identifies a worker session that Core keeps past the end of its turn
// while its step is in progress or its pull request is being operated on.
// ID is the handle Core records on the step; Thread is what remains readable
// after the process ends.
type Session struct {
	ID      string    `json:"id"`
	Worker  string    `json:"worker,omitempty"`
	Version string    `json:"version,omitempty"`
	Started time.Time `json:"started"`
	Thread  Thread    `json:"thread"`
}

// Live owns worker sessions between turns. It ends a session without closing
// its thread or session record, so a later execution generation can resume it
// the way the worker's recovery does. The registry is process-local: a session
// of a previous Core process is unavailable for control and only its recorded
// result remains readable.
type Live struct {
	mu       sync.Mutex
	sessions map[string]*liveEntry
	ended    map[string]Session
}

type liveEntry struct {
	Session
	adapter Adapter
	mu      sync.Mutex
	ended   bool
	close   func(context.Context) error
	events  <-chan Event
}

// NewLive returns an empty registry. A nil registry is not usable; callers that
// may not delegate check for nil instead of constructing one per call.
func NewLive() *Live {
	return &Live{sessions: map[string]*liveEntry{}, ended: map[string]Session{}}
}

// Adopt takes ownership of an open worker session. The registry drains its
// events so an idle process cannot block on a full notification channel. An
// existing session for the same handle with another thread is ended first; a
// repeated adoption of the same thread is harmless.
func (l *Live) Adopt(s Session, a Adapter, close func(context.Context) error) error {
	if l == nil || s.ID == "" || a == nil || close == nil || s.Thread.ID == "" {
		return ErrSession
	}
	l.mu.Lock()
	if current := l.sessions[s.ID]; current != nil {
		if current.Thread == s.Thread {
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()
		if err := l.End(context.Background(), s.ID); err != nil {
			return err
		}
		l.mu.Lock()
	}
	e := &liveEntry{Session: s, adapter: a, close: close, events: a.Events()}
	l.sessions[s.ID] = e
	delete(l.ended, s.ID)
	l.mu.Unlock()
	go func() {
		for range e.events {
		}
		// A process that exits on its own ends this entry. The handle can be
		// reused by a later session, so joining by handle would end that
		// replacement when this process's events finish. Joining releases the
		// adapter's process owner; the thread record stays.
		_ = l.finish(context.Background(), e)
	}()
	return nil
}

// End ends one live session. It is idempotent and returns an error only when
// the process could not be joined, so a later reap can retry.
func (l *Live) End(ctx context.Context, id string) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	e := l.sessions[id]
	l.mu.Unlock()
	if e == nil {
		return nil
	}
	return l.finish(ctx, e)
}

// finish joins one adopted entry. A replacement under the same handle is a
// different entry and is left alone, including when this entry's event drain
// finishes after that replacement was installed.
func (l *Live) finish(ctx context.Context, e *liveEntry) error {
	e.mu.Lock()
	if e.ended {
		e.mu.Unlock()
		return nil
	}
	err := e.close(ctx)
	if err == nil {
		e.ended = true
	}
	e.mu.Unlock()
	if err != nil {
		return err
	}
	l.retire(e.ID, e)
	return nil
}

// EndUnlisted ends every live session whose handle is not in keep. Landed and
// closed steps leave keep, so the number of live sessions follows the frontier.
func (l *Live) EndUnlisted(ctx context.Context, keep map[string]bool) error {
	if l == nil {
		return nil
	}
	return l.endWhere(ctx, func(e *liveEntry) bool { return !keep[e.ID] })
}

// EndTask ends every live session of one task before a new attempt starts, so
// two worker processes never share a worktree. It is the dispatch-time guard;
// the frontier rule ends sessions whose step has left the live set.
func (l *Live) EndTask(ctx context.Context, task string) error {
	if l == nil || task == "" {
		return nil
	}
	return l.endWhere(ctx, func(e *liveEntry) bool { return e.Thread.TaskID == task })
}

func (l *Live) endWhere(ctx context.Context, match func(*liveEntry) bool) error {
	l.mu.Lock()
	ids := make([]string, 0, len(l.sessions))
	for id, e := range l.sessions {
		if match(e) {
			ids = append(ids, id)
		}
	}
	l.mu.Unlock()
	sort.Strings(ids)
	var failures []error
	for _, id := range ids {
		if err := l.End(ctx, id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Close ends every live session. It runs on shutdown; a failed join keeps the
// session for a later attempt instead of claiming it ended.
func (l *Live) Close(ctx context.Context) error {
	if l == nil {
		return nil
	}
	return l.EndUnlisted(ctx, nil)
}

// List returns the live sessions ordered by handle.
func (l *Live) List() []Session {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Session, 0, len(l.sessions))
	for _, e := range l.sessions {
		out = append(out, e.Session)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Observe reads one already-owned session without holding the registry lock
// across adapter I/O. An ended or replaced session cannot survive the read.
// Callers authorize the handle before calling; this grants no worker control.
func (l *Live) Observe(ctx context.Context, id string) (Session, ThreadSnapshot, bool, error) {
	if l == nil {
		return Session{}, ThreadSnapshot{}, false, nil
	}
	l.mu.Lock()
	e := l.sessions[id]
	l.mu.Unlock()
	if e == nil {
		return Session{}, ThreadSnapshot{}, false, nil
	}
	snapshot, err := e.adapter.Snapshot(ctx, e.Thread)
	l.mu.Lock()
	current := l.sessions[id] == e
	l.mu.Unlock()
	return e.Session, snapshot, current, err
}

// Count is the number of live worker processes.
func (l *Live) Count() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.sessions)
}

// Ended returns the retained record of an ended session. It is memory-only
// history; the durable thread record is the work ledger's session handle.
func (l *Live) Ended(id string) (Session, bool) {
	if l == nil {
		return Session{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	s, ok := l.ended[id]
	return s, ok
}

func (l *Live) retire(id string, e *liveEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sessions[id] != e {
		return
	}
	delete(l.sessions, id)
	l.ended[id] = e.Session
}
