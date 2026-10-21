// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"database/sql"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/runs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type listSource map[string]runs.ListItem

func (s listSource) Candidates(context.Context, *canon.Tx, string) ([]string, error) {
	ids := []string{}
	for id := range s {
		ids = append(ids, id)
	}
	return ids, nil
}
func (s listSource) Read(_ context.Context, _ *canon.Tx, id string) (runs.ListItem, error) {
	r, ok := s[id]
	if !ok {
		return r, sql.ErrNoRows
	}
	return r, nil
}
func listDB(t *testing.T) (*canon.Store, canon.Config) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	ms := canon.Migrations()
	ms = append(ms, canon.Migration{Version: len(ms) + 1, SQL: runs.RunningListSchema})
	cfg := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: c, Migrations: ms}
	return openListDB(t, cfg), cfg
}
func openListDB(t *testing.T, cfg canon.Config) *canon.Store {
	t.Helper()
	db, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}
func TestRunningListRanksBothRunsAndTasksAndRetainsUnseenLandings(t *testing.T) {
	db, cfg := listDB(t)
	base := runs.ListItem{Person: "person", Agent: "agent", Summary: "Compare the options", AgentName: "Guide", Started: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	runSource, taskSource := listSource{}, listSource{}
	for _, state := range []string{"working", "waiting", "needs_input", "complete", "partial", "failed", "cancelled"} {
		r := base
		r.State = state
		if state == "waiting" {
			r.Reason = "run_limit"
		}
		runSource[state] = r
	}
	task := base
	task.State = "waiting"
	task.Reason = "dependency"
	taskSource["task"] = task
	other := base
	other.Person = "other"
	other.State = "working"
	taskSource["other"] = other
	sources := map[string]runs.ListSource{"run": runSource, "task": taskSource}
	list := runs.NewRunningList(db, sources)
	snapshot, err := list.Build(t.Context(), "person")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range snapshot.Entries {
		ids = append(ids, e.Ref.ID)
	}
	if !reflect.DeepEqual(ids, []string{"needs_input", "complete", "partial", "working", "waiting", "task", "cancelled", "failed"}) {
		t.Fatalf("ranking=%v", ids)
	}
	if snapshot.Working != 1 || snapshot.Waiting != 3 || snapshot.NeedsPerson != 1 || snapshot.Landed != 4 {
		t.Fatalf("counts=%+v", snapshot)
	}
	words := map[string]string{"needs_input": "needs_input", "complete": "ready_for_review", "partial": "ready_for_review", "working": "in_progress", "waiting": "waiting", "task": "waiting", "failed": "failed", "cancelled": "stopped"}
	for _, e := range snapshot.Entries {
		if e.View.State != words[e.Ref.ID] {
			t.Fatalf("state=%+v", e)
		}
	}
	if err := list.Open(t.Context(), "person", runs.ListRef{Source: "run", ID: "working"}); err != nil {
		t.Fatal(err)
	}
	if err := list.Dismiss(t.Context(), "other", runs.ListRef{Source: "run", ID: "complete"}); !errors.Is(err, runs.ErrListItem) {
		t.Fatalf("cross person dismissal: %v", err)
	}
	for _, id := range []string{"complete", "failed"} {
		if err := list.Open(t.Context(), "person", runs.ListRef{Source: "run", ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := list.Dismiss(t.Context(), "person", runs.ListRef{Source: "run", ID: "partial"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	list = runs.NewRunningList(openListDB(t, cfg), sources)
	snapshot, err = list.Build(t.Context(), "person")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 5 || snapshot.Landed != 1 || snapshot.Working != 1 {
		t.Fatalf("retention after reopen=%+v", snapshot)
	}
}
func TestRunningListRejectsUnknownStateAndUnexplainedWaiting(t *testing.T) {
	db, _ := listDB(t)
	source := listSource{"x": {Person: "person", Agent: "agent", Summary: "Read sources", State: "invented"}}
	list := runs.NewRunningList(db, map[string]runs.ListSource{"run": source})
	if _, err := list.Build(t.Context(), "person"); !errors.Is(err, runs.ErrListItem) {
		t.Fatalf("unknown state: %v", err)
	}
	r := source["x"]
	r.State = "waiting"
	source["x"] = r
	if _, err := list.Build(t.Context(), "person"); !errors.Is(err, runs.ErrListItem) {
		t.Fatalf("missing reason: %v", err)
	}
}
