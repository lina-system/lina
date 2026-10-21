// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"context"
	"database/sql"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/runs/view"
	"sort"
	"time"
)

var ErrListItem = errors.New("invalid_running_list_item")

type ListRef struct{ Source, ID string }
type ListItem struct {
	Person, Agent                     string
	Summary, AgentName, State, Reason string
	Started                           time.Time
}
type ListSource interface {
	Candidates(context.Context, *canon.Tx, string) ([]string, error)
	Read(context.Context, *canon.Tx, string) (ListItem, error)
}
type ListCanon interface {
	Write(context.Context, func(*canon.Tx) error) error
	Read(context.Context, func(*canon.Tx) error) error
}
type RunningEntry struct {
	Ref   ListRef
	Group string
	View  view.Running
}
type RunningSnapshot struct {
	Entries                               []RunningEntry
	Working, Waiting, NeedsPerson, Landed int
}
type RunningList struct {
	db      ListCanon
	sources map[string]ListSource
}

func NewRunningList(db ListCanon, sources map[string]ListSource) *RunningList {
	copy := make(map[string]ListSource, len(sources))
	for name, source := range sources {
		copy[name] = source
	}
	return &RunningList{db: db, sources: copy}
}

const RunningListSchema = `CREATE TABLE running_seen(source TEXT NOT NULL, item_id TEXT NOT NULL, person TEXT NOT NULL, PRIMARY KEY(source,item_id,person));`

// Build combines authoritative run and task records. It never infers progress
// from elapsed time, and one waiting reason comes from each owning scheduler.
func (l *RunningList) Build(ctx context.Context, person string) (RunningSnapshot, error) {
	if person == "" {
		return RunningSnapshot{}, ErrListItem
	}
	type ranked struct {
		entry    RunningEntry
		priority int
		started  time.Time
	}
	var rankedRows []ranked
	snapshot := RunningSnapshot{Entries: []RunningEntry{}}
	err := l.db.Read(ctx, func(tx *canon.Tx) error {
		for name, source := range l.sources {
			if name == "" || source == nil {
				return ErrListItem
			}
			ids, err := source.Candidates(ctx, tx, person)
			if err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, id := range ids {
				if id == "" {
					return ErrListItem
				}
				if seen[id] {
					continue
				}
				seen[id] = true
				item, err := source.Read(ctx, tx, id)
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				if item.Person != person {
					continue
				}
				display, group, priority, err := projectListItem(item)
				if err != nil {
					return err
				}
				if group == "landed" {
					var hidden int
					if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM running_seen WHERE source=? AND item_id=? AND person=?", name, id, person).Scan(&hidden); err != nil {
						return err
					}
					if hidden != 0 {
						continue
					}
					snapshot.Landed++
				}
				if group == "in_progress" {
					snapshot.Working++
				}
				if group == "waiting" {
					snapshot.Waiting++
				}
				if display.State == "needs_input" {
					snapshot.NeedsPerson++
				}
				rankedRows = append(rankedRows, ranked{RunningEntry{ListRef{name, id}, group, display}, priority, item.Started})
			}
		}
		return nil
	})
	if err != nil {
		return RunningSnapshot{}, err
	}
	sort.Slice(rankedRows, func(i, j int) bool {
		a, b := rankedRows[i], rankedRows[j]
		if a.priority != b.priority {
			return a.priority < b.priority
		}
		if !a.started.Equal(b.started) {
			return a.started.Before(b.started)
		}
		if a.entry.Ref.Source != b.entry.Ref.Source {
			return a.entry.Ref.Source < b.entry.Ref.Source
		}
		return a.entry.Ref.ID < b.entry.Ref.ID
	})
	for _, row := range rankedRows {
		snapshot.Entries = append(snapshot.Entries, row.entry)
	}
	return snapshot, nil
}
func projectListItem(item ListItem) (view.Running, string, int, error) {
	v := view.Running{Summary: item.Summary, AgentName: item.AgentName}
	if item.Summary == "" {
		return v, "", 0, ErrListItem
	}
	switch item.State {
	case "working":
		v.State = "in_progress"
		return v, "in_progress", 2, nil
	case "waiting":
		switch item.Reason {
		case "dependency", "area", "quota", "host_capacity", "ci_pending", "run_limit", "person", "budget":
		default:
			return v, "", 0, ErrListItem
		}
		v.State = "waiting"
		v.Reason = item.Reason
		return v, "waiting", 3, nil
	case "needs_input":
		v.State = "needs_input"
		v.Reason = "person"
		return v, "waiting", 0, nil
	case "complete", "partial":
		v.State = "ready_for_review"
		return v, "landed", 1, nil
	case "failed":
		v.State = "failed"
		return v, "landed", 4, nil
	case "cancelled":
		v.State = "stopped"
		return v, "landed", 4, nil
	}
	return v, "", 0, ErrListItem
}

// Open removes only landed rows. Opening live work never hides it.
func (l *RunningList) Open(ctx context.Context, person string, ref ListRef) error {
	return l.markSeen(ctx, person, ref, false)
}

// Dismiss removes a landed row without deleting its source or result.
func (l *RunningList) Dismiss(ctx context.Context, person string, ref ListRef) error {
	return l.markSeen(ctx, person, ref, true)
}
func (l *RunningList) markSeen(ctx context.Context, person string, ref ListRef, dismiss bool) error {
	source := l.sources[ref.Source]
	if person == "" || ref.ID == "" || source == nil {
		return ErrListItem
	}
	return l.db.Write(ctx, func(tx *canon.Tx) error {
		item, err := source.Read(ctx, tx, ref.ID)
		if err != nil {
			return err
		}
		if item.Person != person {
			return ErrListItem
		}
		_, group, _, err := projectListItem(item)
		if err != nil {
			return err
		}
		if group != "landed" {
			if dismiss {
				return ErrListItem
			}
			return nil
		}
		_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO running_seen(source,item_id,person) VALUES(?,?,?)", ref.Source, ref.ID, person)
		return err
	})
}
