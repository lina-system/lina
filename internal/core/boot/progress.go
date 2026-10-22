// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/progress"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/protocol"
)

type laneCursor struct {
	Source string `json:"source"`
	After  string `json:"after"`
}

func nextLaneCursor(source, after string) *string {
	raw, _ := json.Marshal(laneCursor{source, after})
	s := string(raw)
	return &s
}

func (r *conversationRuntime) startWorkerLanes() {
	r.identity.Operations[protocol.KindProgressGet] = clientsrv.Operation{
		Capability: protocol.Capability{Name: "worker_lanes", Version: "1.0"},
		Handle: func(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
			if err := r.authorize(s, nil); err != nil {
				return clientsrv.Result{}, err
			}
			agents, err := r.agents.List(ctx)
			if err != nil {
				return clientsrv.Result{}, err
			}
			names := map[string]string{}
			for _, a := range agents {
				names[a.ID] = a.Name
			}
			cursor := laneCursor{Source: "work"}
			if p := e.Payload.(protocol.ProgressGet); p.After != nil {
				if json.Unmarshal([]byte(*p.After), &cursor) != nil || cursor.Source != "work" && cursor.Source != "session" {
					return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
				}
			}
			lanes := []protocol.WorkerLane{}
			p := protocol.Progress{Lanes: &lanes}
			if cursor.Source == "work" {
				p, err = progress.Lanes(ctx, r.db, s.PersonID, cursor.After, names)
				if err != nil {
					return clientsrv.Result{}, err
				}
				if p.NextCursor != nil {
					p.NextCursor = nextLaneCursor("work", *p.NextCursor)
					return clientsrv.Result{Kind: protocol.KindProgress, Payload: p}, nil
				}
				cursor.After = ""
			}
			rows, next := r.sessionLanes(ctx, s.PersonID, cursor.After, names, progress.PageSize-len(*p.Lanes))
			*p.Lanes = append(*p.Lanes, rows...)
			p.NextCursor = next
			return clientsrv.Result{Kind: protocol.KindProgress, Payload: p}, nil
		},
	}
}

// Runtime sessions have no durable task receipt yet. Read their owning adapter's
// snapshot, preserving idle/unknown as session states, never as verified delivery.
func (r *conversationRuntime) sessionLanes(ctx context.Context, person, after string, names map[string]string, limit int) ([]protocol.WorkerLane, *string) {
	rows := []protocol.WorkerLane{}
	if r.workers == nil {
		return rows, nil
	}
	b := r.workers
	b.mu.Lock()
	entries := map[string]*workerSession{}
	if !b.closed {
		for id, e := range b.sessions {
			if _, ok := names[e.origin.agent]; ok && e.origin.person == person && id > after {
				entries[id] = e
			}
		}
	}
	b.mu.Unlock()
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, id := range ids {
		if len(rows) == limit {
			return rows, nextLaneCursor("session", after)
		}
		e := entries[id]
		snapshot, err := e.adapter.Snapshot(ctx, e.thread)
		if err != nil {
			snapshot = worker.ThreadSnapshot{Thread: e.thread, Worker: "Codex", State: worker.Unknown}
		}
		thread := snapshot.Thread
		rows = append(rows, protocol.WorkerLane{LaneID: "session:" + id, TaskID: thread.TaskID, AgentID: e.origin.agent, AgentName: laneText(names[e.origin.agent]), Worker: laneText(snapshot.Worker), ThreadID: id, TurnID: thread.TurnID, NodeID: r.main, Generation: strconv.FormatUint(thread.ProcessGeneration, 10), State: string(snapshot.State), Title: "Worker session", Details: []string{"version: " + laneText(snapshot.Version), "worker (worker_report): " + laneText(snapshot.Output)}})
		after = id
	}
	return rows, nil
}
func laneText(s string) string {
	masked, err := secrets.Mask(s)
	if err != nil {
		return ""
	}
	r := []rune(masked.Text())
	if len(r) > 512 {
		return string(r[:511]) + "…"
	}
	return masked.Text()
}
