// SPDX-License-Identifier: Apache-2.0
package records

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func todoDocument(id, fields string) []byte {
	return []byte(fmt.Sprintf("---\nid: %q\nkind: todo\nstate: open\nperson_id: \"person\"\n%s---\n\n# Task\n", id, fields))
}

func TestTodoAmbiguousDateDoesNotPublish(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"next Friday", "다음 금요일", "10/11", "2026-02-30", "2026-10-22T09:00:00"} {
		t.Run(value, func(t *testing.T) {
			s, _, _ := setup(t)
			before, err := s.Head(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Commit(t.Context(), Change{ID: "todo", Path: "inbox/task.md", Data: todoDocument("todo", fmt.Sprintf("when: %q\n", value))}, actor)
			if err == nil {
				t.Fatal("ambiguous date was silently saved")
			}
			after, err := s.Head(t.Context())
			if err != nil || before != after {
				t.Fatal("invalid date changed history", err)
			}
		})
	}
}

func TestTodoDependencyCycleDoesNotPublish(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	a := Change{ID: "a", Path: "inbox/a.md", Data: todoDocument("a", "")}
	a.Base = commit(t, s, a)
	commit(t, s, Change{ID: "b", Path: "inbox/b.md", Data: todoDocument("b", "blocked_by: [\"a\"]\n")})
	commit(t, s, Change{ID: "c", Path: "inbox/c.md", Data: todoDocument("c", "blocked_by: [\"b\"]\n")})
	before, _ := s.Head(t.Context())
	a.Data = todoDocument("a", "blocked_by: [\"c\"]\n")
	_, err := s.Commit(t.Context(), a, actor)
	if err == nil || !strings.Contains(err.Error(), "dependency_cycle") {
		t.Fatalf("cycle was not rejected: %v", err)
	}
	after, _ := s.Head(t.Context())
	if before != after {
		t.Fatal("cycle changed history")
	}
	a.Data = todoDocument("a", "blocked_by: [\"a\"]\n")
	if _, err = s.Commit(t.Context(), a, actor); err == nil || errors.Is(err, ErrStaleRevision) {
		t.Fatal("self-cycle or prior mutation", err)
	}
}

func TestTodoTodayFixtureAndViews(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/today.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Day, Zone string
		Cases     []struct {
			ID, When, Deadline, State string
			Repeat                    *Repeat
			Today, Overdue            bool
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	s, root, db := setup(t)
	want := []string{}
	overdue := map[string]bool{}
	for _, c := range fixture.Cases {
		v := Todo{State: c.State, When: c.When, Deadline: c.Deadline, Repeat: c.Repeat}
		if v.Deadline != "" {
			v.DeadlineSource = "source:fixture"
		}
		front, err := WithTodo(fmt.Sprintf("---\nid: %q\nkind: todo\nperson_id: \"person\"", c.ID), v)
		if err != nil {
			t.Fatal(err)
		}
		commit(t, s, Change{ID: c.ID, Path: "inbox/" + c.ID + ".md", Data: []byte(front + "\n---\n\n# Task\n")})
		if c.Today {
			want = append(want, c.ID)
		}
		overdue[c.ID] = c.Overdue
	}
	check := func(repo *Store) {
		t.Helper()
		rows, err := repo.Query(t.Context(), Params{Person: "person", View: "today", Day: fixture.Day, Zone: fixture.Zone})
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, r := range rows {
			ids = append(ids, r.ID)
			if r.Overdue != overdue[r.ID] || r.Todo == nil {
				t.Fatal("bad today row", r)
			}
		}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("today ids=%v want=%v", ids, want)
		}
		for view, expected := range map[string][]string{
			"anytime":  {"e-anytime", "m-backlog"},
			"later":    {"m-backlog", "n-backlog-dated"},
			"upcoming": {"c-cutoff", "f-future", "k-next-midnight"},
			"week":     {"b-start", "c-cutoff", "d-repeat", "f-future", "i-local-midnight", "k-next-midnight", "n-backlog-dated"},
		} {
			rows, err := repo.Query(t.Context(), Params{Person: "person", View: view, Day: fixture.Day, Zone: fixture.Zone})
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			for _, r := range rows {
				ids = append(ids, r.ID)
			}
			if !reflect.DeepEqual(ids, expected) {
				t.Errorf("%s=%v want %v", view, ids, expected)
			}
		}
		// Every board category remains represented, and id cursors remain exclusive.
		rows, err = repo.Query(t.Context(), Params{Person: "person", View: "board", Limit: 1})
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		next, err := repo.Query(t.Context(), Params{Person: "person", View: "board", After: rows[0].ID})
		if err != nil || len(next) != len(fixture.Cases)-1 {
			t.Fatal(next, err)
		}
		foreign, err := repo.Query(t.Context(), Params{Person: "other", View: "today", Day: fixture.Day, Zone: fixture.Zone})
		if err != nil || len(foreign) != 0 {
			t.Fatal("foreign view", foreign, err)
		}
	}
	check(s)
	before := indexDump(t, db)
	if err = s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	if after := indexDump(t, db); after != before {
		t.Fatal("rebuild changed projections")
	}
	// A missing to-do projection is repaired even when the index head still matches.
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DELETE FROM records_todo")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	check(reopened)
}

func TestTodoDatesAndDependencyValidation(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	for _, fields := range []string{
		"deadline: \"2026-10-22\"\n", // no source
		"when: \"2026-10-22\"\nwaiting_on: {\"who\":\"supplier\",\"since\":\"yesterday\",\"next_check\":\"2026-10-23\"}\n",
		"blocked_by: [\"missing\"]\n",
		"belongs_to: [\"missing\"]\n",
	} {
		if _, err := s.Commit(t.Context(), Change{ID: "bad", Path: "inbox/bad.md", Data: todoDocument("bad", fields)}, actor); err == nil {
			t.Fatal("invalid todo accepted", fields)
		}
	}
	a := Change{ID: "a", Path: "inbox/a.md", Data: todoDocument("a", "")}
	a.Base = commit(t, s, a)
	b := Change{ID: "b", Path: "inbox/b.md", Data: todoDocument("b", "blocked_by: [\"a\"]\n")}
	b.Base = commit(t, s, b)
	rows, err := s.Query(t.Context(), Params{Person: "person", View: "board", After: "a"})
	if err != nil || len(rows) != 1 || !rows[0].Blocked {
		t.Fatal(rows, err)
	}
	// A human dependency is a signal, not a completion gate.
	b.Data = bytes.Replace(b.Data, []byte("state: open"), []byte("state: completed"), 1)
	commit(t, s, b)
	a.Data = bytes.Replace(a.Data, []byte("state: open"), []byte("state: completed"), 1)
	commit(t, s, a)
	rows, err = s.Query(t.Context(), Params{Person: "person", View: "board"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Blocked {
			t.Fatal("resolved overlay remains", r)
		}
	}
	// A foreign blocker is rejected without disclosing its record.
	foreign := bytes.Replace(todoDocument("foreign", ""), []byte(`"person"`), []byte(`"other"`), 1)
	commit(t, s, Change{ID: "foreign", Path: "inbox/foreign.md", Data: foreign})
	if _, err = s.Commit(t.Context(), Change{ID: "bad", Path: "inbox/bad.md", Data: todoDocument("bad", "blocked_by: [\"foreign\"]\n")}, actor); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestTodoTodayDSTBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ day, start, end string }{{"2026-03-08", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z"}, {"2026-11-01", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z"}} {
		bounds, err := viewBounds(Params{View: "today", Day: tc.day, Zone: "America/New_York"})
		if err != nil || bounds[1] != tc.start || bounds[2] != tc.end {
			t.Fatal(bounds, err)
		}
	}
}

func TestTodoConcurrentCyclesSerialize(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	a := Change{ID: "a", Path: "inbox/a.md", Data: todoDocument("a", "")}
	a.Base = commit(t, s, a)
	b := Change{ID: "b", Path: "inbox/b.md", Data: todoDocument("b", "")}
	b.Base = commit(t, s, b)
	a.Data = todoDocument("a", "blocked_by: [\"b\"]\n")
	b.Data = todoDocument("b", "blocked_by: [\"a\"]\n")
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, c := range []Change{a, b} {
		go func() { <-start; _, err := s.Commit(t.Context(), c, actor); results <- err }()
	}
	close(start)
	first, second := <-results, <-results
	if first != nil {
		first, second = second, first
	}
	if first != nil || !errors.Is(second, ErrDependencyCycle) {
		t.Fatal("concurrent graph updates", first, second)
	}
}

func TestTodoFieldsRoundTrip(t *testing.T) {
	t.Parallel()
	v := Todo{State: "started", When: "2026-10-22", Important: true, Tags: []string{"work"}, Source: "mail:1", Checklist: []ChecklistItem{{Text: "Read", Checked: true}}, WaitingOn: &WaitingOn{Who: "supplier", Since: "2026-10-20T10:00:00+09:00", NextCheck: "2026-10-23"}, Assignee: &Assignee{Kind: "person", ID: "person"}, Issue: "https://example.invalid/issues/1", Priority: "high"}
	front, err := WithTodo("---\nid: \"task\"\nkind: todo\nperson_id: \"person\"\ncustom: \"keep\"", v)
	if err != nil {
		t.Fatal(err)
	}
	d, err := parseDocument([]byte(front + "\n---\n\n# Task\n\nNote\n"))
	if err != nil || !reflect.DeepEqual(d.todo, &v) || !strings.Contains(front, `custom: "keep"`) {
		t.Fatal(d, err)
	}
	for _, v := range []Todo{
		{State: "unknown"},
		{Deadline: "2026-10-22", DeadlineSource: "mail:1", Repeat: &Repeat{Rule: "FREQ=DAILY", Next: "2026-10-22"}},
		{Checklist: []ChecklistItem{{Text: ""}}},
		{Assignee: &Assignee{Kind: "unknown", ID: "1"}},
	} {
		if v.Validate() == nil {
			t.Fatal("invalid fields", v)
		}
	}
}

func TestTodoProjectionUpgrade(t *testing.T) {
	t.Parallel()
	testTodoUpgrade(t, todoDocument("existing", "when: \"2026-10-22\"\n"), "open", "")
}

func testTodoUpgrade(t *testing.T, data []byte, wantState, wantSource string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	cfg := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()[:35]}
	db, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := Change{ID: "existing", Path: "inbox/existing.md", Data: data}
	rev := seedLegacyRecord(t, root, db, c)
	if err = db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = canon.Migrations()
	cfg.Epoch = 2
	db, err = canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	s, err := Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.Query(t.Context(), Params{Person: "person", View: "board"})
	if err != nil || len(rows) != 1 || rows[0].Revision != rev || rows[0].ID != c.ID || rows[0].State != wantState || rows[0].Todo.DeadlineSource != wantSource {
		t.Fatal("upgrade lost existing todo", rows, err)
	}
	r, gotRev, err := s.Read(t.Context(), c.ID)
	if err != nil || gotRev != rev || !bytes.Equal(r.Data, c.Data) {
		t.Fatal("upgrade changed canon", r, gotRev, err)
	}
}

func TestTodoLegacyImportedProjectionUpgrade(t *testing.T) {
	t.Parallel()
	hash := strings.Repeat("a", 64)
	source := "snapshot:" + hash + ":tracker:milestone:old"
	for _, state := range []string{"active", "done"} {
		t.Run(state, func(t *testing.T) {
			data := todoDocument("existing", fmt.Sprintf("deadline: \"2026-10-22\"\nprovenance: {\"source\":\"tracker\",\"kind\":\"milestone\",\"id\":\"old\",\"snapshot_sha256\":%q}\n", hash))
			data = bytes.Replace(data, []byte("state: open"), []byte("state: "+state), 1)
			want := "open"
			if state == "done" {
				want = "completed"
			}
			testTodoUpgrade(t, data, want, source)
		})
	}
}
