// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/worker"
)

// Conversation delegations have no durable task card. Keep their live source
// separate from task/run records, and never treat an idle turn as delivery.
type workerListSource struct{ workers *workerRuntime }

func (s workerListSource) Candidates(_ context.Context, _ *canon.Tx, person string) ([]string, error) {
	if s.workers == nil {
		return nil, nil
	}
	s.workers.mu.Lock()
	defer s.workers.mu.Unlock()
	var ids []string
	if !s.workers.closed {
		for id, e := range s.workers.sessions {
			if e.origin.person == person {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

func (s workerListSource) Read(ctx context.Context, tx *canon.Tx, id string) (runs.ListItem, error) {
	if s.workers == nil {
		return runs.ListItem{}, sql.ErrNoRows
	}
	s.workers.mu.Lock()
	e := s.workers.sessions[id]
	closed := s.workers.closed
	s.workers.mu.Unlock()
	if closed || e == nil {
		return runs.ListItem{}, sql.ErrNoRows
	}
	var name string
	if err := tx.QueryRowContext(ctx, "SELECT name FROM agents WHERE id=?", e.origin.agent).Scan(&name); err != nil {
		return runs.ListItem{}, err
	}
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
	name = strings.Join(strings.Fields(laneText(name)), " ")
	return runs.ListItem{Person: e.origin.person, Agent: e.origin.agent, AgentName: name,
		Summary: name + " · Codex · " + summary, State: state, Started: e.started}, nil
}

func (r *conversationRuntime) workerRunningSummary(ctx context.Context, person, id string, offset int64) (string, error) {
	if offset != 0 {
		return "", runs.ErrInvalid
	}
	var item runs.ListItem
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		item, err = (workerListSource{workers: r.workers}).Read(ctx, tx, id)
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
