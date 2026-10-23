// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/progress"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestProgressOverviewPreservesGoalChildren(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	cards := map[string]work.Card{}
	for _, item := range []struct{ id, parent, state string }{
		{"a-goal", "", "unknown"},
		{"a-running", "a-goal", "working"},
		{"a-waiting", "a-goal", "waiting"},
		{"b-goal", "", "waiting"},
		{"b-running", "b-goal", "working"},
		{"b-needs", "b-goal", "input_needed"},
		{"c-standalone", "", "working"},
	} {
		cards[item.id] = work.Card{
			Task:  work.Task{ID: item.id, GoalID: item.parent, PersonID: r.person, AgentID: r.agent, Title: item.id},
			State: item.state, Run: &work.Run{ID: item.id, Generation: 1},
		}
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		for id, card := range cards {
			raw, err := json.Marshal(card)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(t.Context(), "INSERT INTO work_cards(task_id,record) VALUES(?,?)", id, raw); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"b-goal", "b-needs", "b-running", "a-goal", "a-running", "a-waiting", "c-standalone"}
	entries := progress.Compute(progress.UnplannedWork(cards))
	computed := []string{}
	for _, entry := range entries {
		computed = append(computed, entry.Ref)
	}
	if !reflect.DeepEqual(computed, want) {
		t.Fatalf("source hierarchy = %v, want %v", computed, want)
	}
	mode := "overview"
	result, err := r.identity.Operations[protocol.KindProgressGet].Handle(t.Context(), clientsrv.Session{PersonID: r.person}, protocol.Envelope{Payload: protocol.ProgressGet{View: &mode}})
	if err != nil {
		t.Fatal(err)
	}
	p := result.Payload.(protocol.Progress)
	if p.Rows == nil {
		t.Fatal("missing overview")
	}
	got := []string{}
	for _, row := range *p.Rows {
		got = append(got, row.Ref)
		depth := json.Number("0")
		if cards[row.Ref].Task.GoalID != "" {
			depth = "1"
		}
		if row.Line.Depth != depth {
			t.Errorf("%s depth = %s, want %s", row.Ref, row.Line.Depth, depth)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Core overview separated goal children: got %v, want %v", got, want)
	}
	wantCards := []string{"b-needs", "b-running", "a-running", "c-standalone", "a-waiting"}
	if p.CardOrder == nil || !reflect.DeepEqual(*p.CardOrder, wantCards) {
		t.Fatalf("flat card order = %v, want %v", p.CardOrder, wantCards)
	}
	if err := r.publishOwnerViews(t.Context()); err != nil {
		t.Fatal(err)
	}
	start, sub, err := r.topics.Subscribe("progress", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	var snapshot protocol.ProgressUpdate
	if err := json.Unmarshal(start.Snapshot.Items[0].Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Rows, p.Rows) || !reflect.DeepEqual(snapshot.CardOrder, p.CardOrder) {
		t.Fatal("topic snapshot differs from overview")
	}
	// A changed execution state reorders cards without mixing either family.
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		card := cards["a-waiting"]
		card.State = "working"
		raw, err := json.Marshal(card)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(t.Context(), "UPDATE work_cards SET record=? WHERE task_id=?", raw, card.Task.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.publishOwnerViews(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	frame, err := sub.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var update protocol.ProgressUpdate
	if err := json.Unmarshal(frame.Event.Data, &update); err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, row := range *update.Rows {
		got = append(got, row.Ref)
	}
	wantCards = []string{"b-needs", "b-running", "a-running", "a-waiting", "c-standalone"}
	if !reflect.DeepEqual(got, want) || update.CardOrder == nil || !reflect.DeepEqual(*update.CardOrder, wantCards) {
		t.Fatalf("live update lost hierarchy or card order: %+v", update)
	}
}
