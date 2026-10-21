// SPDX-License-Identifier: Apache-2.0
package budget

import (
	"sync"
	"time"
)

type Level string

const (
	Under Level = "under"
	Soft  Level = "soft"
	Hard  Level = "hard"
)

// Meter measures placement, not the shared token allowance. Its zero value uses
// the monotonic clock. Now can be supplied for deterministic acceptance tests.
type Meter struct{ Now func() time.Time }

type Snapshot struct {
	Turn       string
	ReceivedAt time.Time
	Rounds     int
	ToolTime   time.Duration
	Waiting    time.Duration
	Level      Level
}

// Continue restores a completed segment's meter without resetting its budget.
// Persisted state is Core-owned, never supplied by a model or a client.
func (m Meter) Continue(s Snapshot) *Gauge {
	g := m.Start(s.Turn, s.ReceivedAt)
	g.rounds, g.toolTime = s.Rounds, s.ToolTime
	return g
}

// Gauge belongs to one turn, including its steering and continuations. Callers
// carry the same gauge into a continuation and start a new one for a new input.
// Overlapping executions contribute their union, not the sum of tool durations.
type Gauge struct {
	mu       sync.Mutex
	now      func() time.Time
	turn     string
	received time.Time
	rounds   int
	active   int
	started  time.Time
	toolTime time.Duration
}

func (m Meter) Start(turn string, receivedAt time.Time) *Gauge {
	now := m.Now
	if now == nil {
		now = time.Now
	}
	if receivedAt.IsZero() {
		receivedAt = now()
	}
	return &Gauge{now: now, turn: turn, received: receivedAt}
}

// Tool starts timing an actual executor, excluding model generation, validation,
// authorization waits, and calls that never run. Its completion is idempotent.
func (g *Gauge) Tool() func() {
	g.mu.Lock()
	if g.active == 0 {
		g.started = g.now()
	}
	g.active++
	g.mu.Unlock()
	return sync.OnceFunc(func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.active--
		if g.active == 0 {
			g.toolTime += max(0, g.now().Sub(g.started))
		}
	})
}

// Round records one response's completed tool batch, including rejected calls.
func (g *Gauge) Round() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rounds++
}

func (g *Gauge) Snapshot() Snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	s := Snapshot{Turn: g.turn, ReceivedAt: g.received, Rounds: g.rounds, ToolTime: g.toolTime, Waiting: max(0, now.Sub(g.received)), Level: Under}
	if g.active > 0 {
		s.ToolTime += max(0, now.Sub(g.started))
	}
	if s.Rounds >= 10 || s.ToolTime >= 60*time.Second {
		s.Level = Hard
	} else if s.Rounds >= 6 || s.ToolTime >= 30*time.Second {
		s.Level = Soft
	}
	return s
}
