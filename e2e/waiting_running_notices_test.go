// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/waiting/view"
)

// The wire kinds still reserve their payloads. This scenario exercises the real
// Core APIs and canon writer in #34's isolated state root, without claiming TUI
// or protocol rendering coverage. Its script lives under e2e/m1/.
func TestM1WaitingRunningNotices(t *testing.T) {
	data, err := os.ReadFile("m1/waiting-running-notices.json")
	if err != nil {
		t.Fatal(err)
	}
	var script struct {
		Person, Agent, Conversation string
		Now                         time.Time
		Rows                        []struct {
			ID                         string
			Kind                       view.Kind
			Requested, Due             time.Time
			Costly, Blocking, Finished bool
		}
		Order    []string
		Answer   struct{ ID, Value string }
		Landings []int `json:"landings_seconds"`
		Sizes    []int `json:"notice_sizes"`
	}
	if err := json.Unmarshal(data, &script); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, "")
	controls, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	ms := canon.Migrations()
	ms = append(ms, canon.Migration{Version: len(ms) + 1, SQL: waiting.Schema + runs.RunningListSchema + lane.NoticeSchema + `CREATE TABLE scenario_waiting(id TEXT PRIMARY KEY, record BLOB, handled INTEGER DEFAULT 0);`})
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: ms})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	source := &scenarioWaiting{}
	for _, row := range script.Rows {
		source.ids = append(source.ids, row.ID)
		item := waiting.Item{Person: script.Person, Agent: script.Agent, Revision: 1, Reason: waiting.SignificantChoice, Finished: row.Finished, Blocking: row.Blocking, Costly: row.Costly, Requested: row.Requested,
			View: view.Waiting{Kind: row.Kind, Sentence: "Choose the next step", Answers: []string{"continue", "skip"}, Effort: view.FiveMinutes, Due: row.Due}}
		b, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Write(h.ctx, func(tx *canon.Tx) error {
			_, err := tx.ExecContext(h.ctx, "INSERT INTO scenario_waiting(id,record) VALUES(?,?)", row.ID, b)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	svc := waiting.New(db, map[string]waiting.Source{"work": source})
	snapshot, err := svc.Build(h.ctx, script.Person, waiting.Options{Now: script.Now})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, row := range snapshot.Entries {
		ids = append(ids, row.Ref.ID)
	}
	if !reflect.DeepEqual(ids, script.Order) {
		t.Fatalf("waiting order: %v", ids)
	}
	var answer waiting.Ref
	for _, row := range snapshot.Entries {
		if row.Ref.ID == script.Answer.ID {
			answer = row.Ref
		}
	}
	decision, err := svc.Answer(h.ctx, script.Person, answer, script.Answer.Value, script.Now)
	if err != nil || decision.Person != script.Person || decision.Agent != script.Agent {
		t.Fatalf("decision: %+v %v", decision, err)
	}
	// The producer observes the committed answer and exposes the work as running.
	running := runs.NewRunningList(db, map[string]runs.ListSource{"work": scenarioRunning{person: script.Person, agent: script.Agent, id: answer.ID}})
	rs, err := running.Build(h.ctx, script.Person)
	if err != nil || rs.Working != 1 || len(rs.Entries) != 1 {
		t.Fatalf("running: %+v %v", rs, err)
	}
	notices := lane.NewNotices(db, nil)
	published := make(chan lane.Delivery, len(script.Sizes))
	notices.OnDelivered = func(d lane.Delivery) { published <- d }
	lanes := lane.NewWithNotices(notices)
	t.Cleanup(func() { lanes.Close(context.Background()) })
	started, release := make(chan struct{}), make(chan struct{})
	result, err := lanes.Submit(h.ctx, script.Conversation, func(context.Context) error { close(started); <-release; return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	select {
	case <-started:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	for i, offset := range script.Landings {
		n := lane.Notice{Surface: ledger.Surface{Kind: "tui"}, ID: script.Rows[i].ID, Run: script.Rows[i].ID, Person: script.Person, Agent: script.Agent, Conversation: script.Conversation, Outcome: "complete", Summary: "Compared the options", Result: "result", ResultType: "summary", Landed: script.Now.Add(time.Duration(offset) * time.Second)}
		if err := lanes.Notice(h.ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	before, err := notices.Delivered(h.ctx, script.Conversation)
	if err != nil || len(before) != 0 {
		t.Fatalf("notice during turn: %v %v", before, err)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for range script.Sizes {
		select {
		case d := <-published:
			sizes = append(sizes, len(d.Notices))
		case <-h.ctx.Done():
			t.Fatal(h.ctx.Err())
		}
	}
	if !reflect.DeepEqual(sizes, script.Sizes) {
		t.Fatalf("notice batches: %v", sizes)
	}
}

type scenarioWaiting struct{ ids []string }

func (s *scenarioWaiting) Candidates(context.Context, *canon.Tx, string) ([]string, error) {
	return s.ids, nil
}
func (s *scenarioWaiting) Read(ctx context.Context, tx *canon.Tx, id string) (waiting.Item, error) {
	var data []byte
	var item waiting.Item
	err := tx.QueryRowContext(ctx, "SELECT record FROM scenario_waiting WHERE id=?", id).Scan(&data)
	if err == nil {
		err = json.Unmarshal(data, &item)
	}
	return item, err
}
func (s *scenarioWaiting) Done(ctx context.Context, tx *canon.Tx, id string) error {
	_, err := tx.ExecContext(ctx, "UPDATE scenario_waiting SET handled=1 WHERE id=?", id)
	return err
}
func (s *scenarioWaiting) Answer(ctx context.Context, tx *canon.Tx, id, value string) error {
	return s.Done(ctx, tx, id)
}

type scenarioRunning struct{ person, agent, id string }

func (s scenarioRunning) Candidates(context.Context, *canon.Tx, string) ([]string, error) {
	return []string{s.id}, nil
}
func (s scenarioRunning) Read(ctx context.Context, tx *canon.Tx, id string) (runs.ListItem, error) {
	var handled int
	err := tx.QueryRowContext(ctx, "SELECT handled FROM scenario_waiting WHERE id=?", id).Scan(&handled)
	state := "needs_input"
	if handled == 1 {
		state = "working"
	}
	return runs.ListItem{Person: s.person, Agent: s.agent, Summary: "Compare the options", State: state}, err
}
