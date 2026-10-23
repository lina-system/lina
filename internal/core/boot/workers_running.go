// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/worker"
)

// Conversation delegations have no durable task card. Keep their live source
// separate from task/run records, and never treat an idle turn as delivery.
type workerListSource struct {
	workers   *workerRuntime
	person    string
	refreshMu sync.Mutex
	mu        sync.RWMutex
	items     map[string]runs.ListItem
}

func (s *workerListSource) Candidates(_ context.Context, _ *canon.Tx, person string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var ids []string
	for id, item := range s.items {
		if item.Person == person {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// ListSource callbacks run in canon transactions, including the single writer
// for Open/Dismiss. They read only copied observations and canonical metadata.
func (s *workerListSource) Read(ctx context.Context, tx *canon.Tx, id string) (runs.ListItem, error) {
	s.mu.RLock()
	item, ok := s.items[id]
	s.mu.RUnlock()
	if !ok {
		return runs.ListItem{}, sql.ErrNoRows
	}
	var name string
	if err := tx.QueryRowContext(ctx, "SELECT name FROM agents WHERE id=?", item.Agent).Scan(&name); err != nil {
		return runs.ListItem{}, err
	}
	item.AgentName = strings.Join(strings.Fields(laneText(name)), " ")
	item.Summary = item.AgentName + " · " + item.Summary
	return item, nil
}

// Refresh adapter observations before entering any canonical transaction.
// Serialize refreshes without holding the cache lock during process I/O, so
// list actions can always read a complete observation without blocking canon.
func (s *workerListSource) refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	entries := map[string]*workerSession{}
	if s.workers != nil {
		s.workers.mu.Lock()
		if !s.workers.closed {
			for id, e := range s.workers.sessions {
				if e.origin.person == s.person {
					entries[id] = e
				}
			}
		}
		s.workers.mu.Unlock()
	}
	items := map[string]runs.ListItem{}
	for id, e := range entries {
		item, err := observeRunningWorker(ctx, e)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		items[id] = item
	}
	s.mu.Lock()
	s.items = items
	s.mu.Unlock()
	return nil
}

func observeRunningWorker(ctx context.Context, e *workerSession) (runs.ListItem, error) {
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	snapshot, err := e.adapter.Snapshot(readCtx, e.thread)
	if err != nil {
		if ctx.Err() != nil {
			return runs.ListItem{}, ctx.Err()
		}
		snapshot.State = worker.Unknown
		select {
		case <-e.done:
			snapshot.State = worker.Dead
		default:
		}
	}
	e.recoveryMu.Lock()
	recoveryState := e.recovery.State
	e.recoveryMu.Unlock()
	if recoveryState == "unknown" || recoveryState == "retrying" {
		snapshot.State = worker.Unknown
	}
	if recoveryState == "decision" {
		snapshot.State = worker.Waiting
	}
	state := "unknown"
	switch snapshot.State {
	case worker.Idle:
		return runs.ListItem{}, sql.ErrNoRows
	case worker.Active:
		state = "working"
	case worker.Waiting:
		// A pending Core tool call is still Core's work. Only requests that
		// require a person's answer claim attention in the running list.
		for _, request := range snapshot.Pending {
			switch request.Method {
			case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "item/tool/requestUserInput", "mcpServer/elicitation/request":
				state = "needs_input"
			}
		}
		e.mu.Lock()
		for _, q := range e.questions {
			if q.card.Card.Outcome == nil {
				state = "needs_input"
			}
		}
		e.mu.Unlock()
	case worker.Dead:
		state = "failed"
	case worker.Unknown:
	}
	// Use the person's words, not the worker packet, work directory or output.
	summary := strings.Join(strings.Fields(laneText(e.input.Body.Text())), " ")
	if len([]rune(summary)) > 160 {
		summary = string([]rune(summary)[:159]) + "…"
	}
	if summary == "" {
		summary = "Delegated work"
	}
	return runs.ListItem{Person: e.origin.person, Agent: e.origin.agent,
		Summary: workerLabel(snapshot.Worker) + " · " + summary, State: state, Started: e.started}, nil
}

func (r *conversationRuntime) workerRunningSummary(ctx context.Context, person, id string, offset int64) (string, error) {
	if offset != 0 {
		return "", runs.ErrInvalid
	}
	if r.background == nil || r.background.sessions == nil {
		return "", runs.ErrListItem
	}
	source := r.background.sessions
	if err := source.refresh(ctx); err != nil {
		return "", err
	}
	var item runs.ListItem
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		item, err = source.Read(ctx, tx, id)
		if err == nil && item.Person != person {
			return runs.ErrListItem
		}
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", runs.ErrListItem
	}
	return item.Summary, err
}

func (r *conversationRuntime) notifyWorkerRunning() {
	if r.background != nil {
		r.background.notify()
	}
}

func workerLabel(name string) string {
	switch name {
	case "claude":
		return "Claude Code"
	case "antigravity":
		return "Antigravity"
	default:
		return "Codex"
	}
}
