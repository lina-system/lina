// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"slices"
	"time"

	"github.com/thisisjun786/lina/internal/core/progress"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/doing"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (e *workerSession) observeActivity(event worker.Event) {
	activity := worker.Activity(event)
	e.mu.Lock()
	defer e.mu.Unlock()
	if event.Doing != nil && event.Thread.ID == e.thread.ID && event.Thread.TurnID == e.thread.TurnID {
		e.doing = doing.Clean(event.Doing.Line)
	}
	if activity != "" {
		e.activity = "work." + activity
	} else if event.Kind == worker.FinishEvent {
		e.activity = ""
	}
}

// Background runs outside delegated tasks belong to the same work panel.
func (r *conversationRuntime) backgroundProgress(ctx context.Context, names map[string]string) ([]progress.Unit, error) {
	if r.background == nil {
		return nil, nil
	}
	snapshot, err := runs.NewRunningList(r.db, map[string]runs.ListSource{"run": runs.RunListSource{}}).Build(ctx, r.person)
	if err != nil {
		return nil, err
	}
	records, err := r.runRecords(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]runs.Record{}
	for _, record := range records {
		byID[record.ID] = record
	}
	units := []progress.Unit{}
	for _, entry := range snapshot.Entries {
		record := byID[entry.Ref.ID]
		if _, ok := names[record.Origin.Agent]; !ok || record.Origin.Person != r.person {
			continue
		}
		language := design.ConversationLanguage(record.Brief.Goal, design.English)
		u := progress.Unit{ID: "run:" + record.ID, Kind: "project", Name: laneText(record.Brief.Goal), State: entry.View.State, Now: design.Text(language, "running.state."+entry.View.State), Active: record.State != runs.Landed, Landed: record.State == runs.Landed, Updated: record.Updated}
		if record.Created != 0 {
			u.StartedAt = time.Unix(0, record.Created)
		}
		if record.State == runs.NeedsInput {
			u.Waiting = 1
		}
		units = append(units, u)
	}
	return units, nil
}

// Cards are flat; overview rows retain the goal-child groups from Compute.
func workCardOrder(rows []protocol.ProgressRow) []string {
	cards := make([]protocol.ProgressRow, 0, len(rows))
	for _, row := range rows {
		if row.Line.Kind != "goal" {
			cards = append(cards, row)
		}
	}
	slices.SortStableFunc(cards, func(a, b protocol.ProgressRow) int { return workCardRank(a.Line) - workCardRank(b.Line) })
	order := make([]string, 0, len(cards))
	for _, card := range cards {
		order = append(order, card.Ref)
	}
	return order
}

func workCardRank(line protocol.ProgressLine) int {
	if line.Waiting != "0" {
		return 0
	}
	if line.State == nil {
		return 2
	}
	switch *line.State {
	case protocol.RunningStateNeedsInput:
		return 0
	case protocol.RunningStateInProgress:
		return 1
	case protocol.RunningStateDone:
		return 3
	case protocol.RunningStateWaiting, protocol.RunningStateReadyForReview, protocol.RunningStateFailed, protocol.RunningStateStopped, protocol.RunningStateUnknown:
		return 2
	default:
		return 2
	}
}
