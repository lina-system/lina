// SPDX-License-Identifier: Apache-2.0
package waiting_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/waiting/view"
)

var now = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

type source struct {
	ids, reads []string
	beforeRead func(context.Context, *canon.Tx) error
	failAnswer bool
}

func (s *source) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	if s.beforeRead != nil {
		if err := s.beforeRead(ctx, tx); err != nil {
			return nil, err
		}
	}
	return s.ids, nil
}
func (s *source) Read(ctx context.Context, tx *canon.Tx, id string) (waiting.Item, error) {
	s.reads = append(s.reads, id)
	var b []byte
	err := tx.QueryRowContext(ctx, "SELECT record FROM fixture WHERE id=?", id).Scan(&b)
	var item waiting.Item
	if err == nil {
		err = json.Unmarshal(b, &item)
	}
	return item, err
}
func (s *source) Done(ctx context.Context, tx *canon.Tx, id string) error {
	_, err := tx.ExecContext(ctx, "UPDATE fixture SET handled=1 WHERE id=?", id)
	return err
}
func (s *source) Answer(ctx context.Context, tx *canon.Tx, id, answer string) error {
	_, err := tx.ExecContext(ctx, "UPDATE fixture SET handled=1 WHERE id=?", id)
	if s.failAnswer {
		return errors.New("source refused")
	}
	return err
}
func database(t *testing.T) (*canon.Store, canon.Config) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	ms := canon.Migrations()
	ms = append(ms, canon.Migration{Version: len(ms) + 1, SQL: `CREATE TABLE fixture(id TEXT PRIMARY KEY, record BLOB, handled INTEGER DEFAULT 0);`})
	cfg := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: ms}
	return open(t, cfg), cfg
}
func open(t *testing.T, cfg canon.Config) *canon.Store {
	t.Helper()
	db, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}
func item() waiting.Item {
	return waiting.Item{Person: "person", Agent: "agent", Revision: 1, Reason: waiting.SignificantChoice,
		View: view.Waiting{Kind: view.Decide, Sentence: "Choose a route", AgentName: "Guide", Answers: []string{"first", "second"}, Effort: view.ThirtyMinutes}, Requested: now.Add(-time.Hour)}
}
func put(t *testing.T, db *canon.Store, id string, row waiting.Item) {
	t.Helper()
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT OR REPLACE INTO fixture(id,record) VALUES(?,?)", id, b)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func ids(list waiting.List) []string {
	out := []string{}
	for _, e := range list.Entries {
		out = append(out, e.Ref.ID)
	}
	return out
}
func TestPriorityAndLightestFixture(t *testing.T) {
	db, _ := database(t)
	src := &source{ids: []string{"old", "due", "costly", "overdue", "blocked", "soon", "light"}}
	for _, id := range src.ids {
		r := item()
		switch id {
		case "old":
			r.View.Kind = view.Reply
			r.Requested = now.Add(-48 * time.Hour)
		case "due":
			r.View.Due = now.Add(48 * time.Hour)
		case "costly":
			r.Costly = true
		case "overdue":
			r.View.Due = now.Add(-time.Hour)
		case "blocked":
			r.Blocking = true
			r.Irreversible = true
		case "soon":
			r.View.Due = now.Add(24 * time.Hour)
		case "light":
			r.View.Effort = view.FiveMinutes
		}
		put(t, db, id, r)
	}
	svc := waiting.New(db, map[string]waiting.Source{"work": src})
	list, err := svc.Build(t.Context(), "person", waiting.Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"blocked", "soon", "overdue", "costly", "due", "old", "light"}
	if !reflect.DeepEqual(ids(list), want) {
		t.Fatalf("priority=%v want %v", ids(list), want)
	}
	list, err = svc.Build(t.Context(), "person", waiting.Options{Now: now, Order: waiting.Lightest, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(list), []string{"light", "blocked", "soon", "overdue", "costly"}) || list.Total != 7 || list.More != 2 {
		t.Fatalf("home lightest=%+v", list)
	}
}
func TestRereadAndExcludeFinishedOtherPeopleAndRoutineWork(t *testing.T) {
	db, _ := database(t)
	src := &source{ids: []string{"changed", "done", "other", "routine", "todo", "promised"}}
	for _, id := range src.ids {
		r := item()
		switch id {
		case "done":
			r.Finished = true
		case "other":
			r.Person = "someone-else"
		case "routine":
			r.Reason = ""
		case "todo":
			r.View.Kind = view.Do
			r.Reason = waiting.PersonalAction
		case "promised":
			r.View.Kind = view.Do
			r.Reason = waiting.PersonalAction
			r.Promised = true
		}
		put(t, db, id, r)
	}
	src.beforeRead = func(ctx context.Context, tx *canon.Tx) error {
		r := item()
		r.Revision = 2
		r.View.Sentence = "Choose the updated route"
		b, _ := json.Marshal(r)
		_, err := tx.ExecContext(ctx, "UPDATE fixture SET record=? WHERE id='changed'", b)
		return err
	}
	list, err := waiting.New(db, map[string]waiting.Source{"work": src}).Build(t.Context(), "person", waiting.Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(list), []string{"changed", "promised"}) || !reflect.DeepEqual(src.reads, src.ids) {
		t.Fatalf("list=%+v reads=%v", list, src.reads)
	}
	if list.Entries[0].Ref.Revision != 2 || list.Entries[0].View.Sentence != "Choose the updated route" {
		t.Fatal("stale source shown")
	}
	var handled int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT handled FROM fixture WHERE id='done'").Scan(&handled)
	}); err != nil || handled != 1 {
		t.Fatalf("finished not marked: %d %v", handled, err)
	}
}
func TestAnswerAtomicAttributedDurableAndNotRepeated(t *testing.T) {
	db, cfg := database(t)
	put(t, db, "choice", item())
	src := &source{ids: []string{"choice"}}
	svc := waiting.New(db, map[string]waiting.Source{"work": src})
	ref := waiting.Ref{Source: "work", ID: "choice", Revision: 1}
	if _, err := svc.Answer(t.Context(), "other", ref, "first", now); !errors.Is(err, waiting.ErrPerson) {
		t.Fatalf("wrong person: %v", err)
	}
	stale := ref
	stale.Revision = 2
	if _, err := svc.Answer(t.Context(), "person", stale, "first", now); !errors.Is(err, waiting.ErrStale) {
		t.Fatalf("stale: %v", err)
	}
	if _, err := svc.Answer(t.Context(), "person", ref, "invented", now); !errors.Is(err, waiting.ErrInvalid) {
		t.Fatalf("choice: %v", err)
	}
	src.failAnswer = true
	if _, err := svc.Answer(t.Context(), "person", ref, "first", now); err == nil {
		t.Fatal("source failure hidden")
	}
	records, err := svc.Decisions(t.Context(), "person")
	if err != nil || len(records) != 0 {
		t.Fatalf("failed answer recorded: %v %v", records, err)
	}
	var handled int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT handled FROM fixture WHERE id='choice'").Scan(&handled)
	}); err != nil || handled != 0 {
		t.Fatalf("source not rolled back: %d %v", handled, err)
	}
	src.failAnswer = false
	d, err := svc.Answer(t.Context(), "person", ref, "first", now)
	if err != nil || d.Person != "person" || d.Agent != "agent" || d.Answer != "first" || d.Ref != ref {
		t.Fatalf("decision=%+v %v", d, err)
	}
	if _, err := svc.Answer(t.Context(), "person", ref, "second", now); !errors.Is(err, waiting.ErrAnswered) {
		t.Fatalf("duplicate: %v", err)
	}
	list, err := svc.Build(t.Context(), "person", waiting.Options{Now: now})
	if err != nil || list.Total != 0 {
		t.Fatalf("answered row remains: %+v %v", list, err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	svc = waiting.New(open(t, cfg), map[string]waiting.Source{"work": src})
	records, err = svc.Decisions(t.Context(), "person")
	if err != nil || len(records) != 1 || !reflect.DeepEqual(records[0], d) {
		t.Fatalf("reopened decisions=%+v %v", records, err)
	}
}
