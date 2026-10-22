// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/records"
)

func setup(t *testing.T) (*records.Store, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	repo, err := records.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	return repo, filepath.Join(root, "takeover.json")
}

var actor = records.Actor{Person: "person", Agent: "agent"}

func input(t *testing.T, rows ...string) ([]byte, string) {
	t.Helper()
	counts := map[string]int{}
	for _, row := range rows {
		var r struct{ Source string }
		_ = json.Unmarshal([]byte(row), &r)
		counts[r.Source]++
	}
	b, err := json.Marshal(map[string]any{"version": 1, "counts": counts, "records": json.RawMessage("[" + strings.Join(rows, ",") + "]")})
	if err != nil {
		t.Fatal(err)
	}
	return b, fmt.Sprintf("%x", sha256.Sum256(b))
}
func TestImportAccountingAndExactlyOnce(t *testing.T) {
	repo, journal := setup(t)
	data, hash := input(t,
		`{"source":"tracker","kind":"issue","id":"task-old","title":"Write report","done_condition":"Report checked","project":{"source":"tracker","kind":"project","id":"project-old"},"payload":{"labels":["evidence"]}}`,
		`{"source":"tracker","kind":"project","id":"project-old","title":"Workshop","done_condition":"Publish report","parent":{"source":"tracker","kind":"initiative","id":"goal-old"}}`,
		`{"source":"tracker","kind":"initiative","id":"goal-old","title":"Understand the result","done":true}`,
		`{"source":"tracker","kind":"milestone","id":"checkpoint-old","title":"Checkpoint","deadline":"2026-12-01","done_condition":"Evidence checked","done":true}`,
		`{"source":"intent","kind":"intent_card","id":"intent-old","title":"Reliability","intent":"Keep my exact words."}`,
		`{"source":"crw","kind":"receipt","id":"receipt-old","title":"Historical result","payload":{"outcome":"ready_for_review","manifest":[]}}`,
		`{"source":"tracker","kind":"cycle","id":"cycle-old","title":"Unsupported"}`,
		`{"source":"tracker","kind":"issue","id":"broken-old"}`,
		`null`)
	report, err := Import(t.Context(), repo, actor, data, hash, journal)
	if err == nil {
		t.Fatal("loss must fail the run")
	}
	if err := report.Validate(9); err != nil {
		t.Fatal(err)
	}
	if report.Rows[6].Outcome != "unsupported" || report.Rows[7].Outcome != "lost" || report.Rows[8].Outcome != "lost" {
		t.Fatal(report)
	}
	ids := map[string]bool{}
	for i := 0; i < 6; i++ {
		row := report.Rows[i]
		if row.Outcome != "imported" || row.Reused || row.LINAID == "" || ids[row.LINAID] {
			t.Fatal(row)
		}
		ids[row.LINAID] = true
		r, _, err := repo.Read(t.Context(), row.LINAID)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(r.Data), "provenance:") {
			t.Fatal(string(r.Data))
		}
	}
	task, _, _ := repo.Read(t.Context(), report.Rows[0].LINAID)
	if !strings.Contains(string(task.Data), report.Rows[1].LINAID) || !strings.Contains(string(task.Data), `"labels"`) {
		t.Fatal(string(task.Data))
	}
	goal, _, _ := repo.Read(t.Context(), report.Rows[2].LINAID)
	if !strings.Contains(string(goal.Data), "state: draft") {
		t.Fatal(string(goal.Data))
	}
	milestone, _, _ := repo.Read(t.Context(), report.Rows[3].LINAID)
	if !strings.Contains(string(milestone.Data), "kind: todo") || !strings.Contains(string(milestone.Data), "2026-12-01") {
		t.Fatal(string(milestone.Data))
	}
	card, _, _ := repo.Read(t.Context(), report.Rows[4].LINAID)
	if !strings.Contains(string(card.Data), "Keep my exact words.") {
		t.Fatal(string(card.Data))
	}
	if _, _, err := repo.Read(t.Context(), "task-old"); !errors.Is(err, records.ErrNotFound) {
		t.Fatal("source id resolved", err)
	}
	head, _ := repo.Head(t.Context())
	again, _ := Import(t.Context(), repo, actor, data, hash, journal)
	after, _ := repo.Head(t.Context())
	if head != after {
		t.Fatal("second run created new commits")
	}
	for i := 0; i < 6; i++ {
		if again.Rows[i].LINAID != report.Rows[i].LINAID || !again.Rows[i].Reused {
			t.Fatal(again.Rows[i])
		}
	}
}
func TestVerificationAndSnapshotBinding(t *testing.T) {
	repo, journal := setup(t)
	data, hash := input(t, `{"source":"tracker","kind":"issue","id":"one","title":"One"}`)
	before, _ := repo.Head(t.Context())
	if _, err := Import(t.Context(), repo, actor, data, strings.Repeat("0", 64), journal); err == nil {
		t.Fatal("wrong hash accepted")
	}
	after, _ := repo.Head(t.Context())
	if before != after {
		t.Fatal("wrote before verification")
	}
	if _, err := os.Stat(journal); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unverified input reserved ids", err)
	}
	if _, err := Import(t.Context(), repo, actor, data, hash, journal); err != nil {
		t.Fatal(err)
	}
	other, otherHash := input(t, `{"source":"tracker","kind":"issue","id":"two","title":"Two"}`)
	if _, err := Import(t.Context(), repo, actor, other, otherHash, journal); err == nil {
		t.Fatal("second snapshot accepted")
	}
	if _, err := Import(t.Context(), repo, records.Actor{Person: "someone", Agent: "agent"}, data, hash, journal); err == nil {
		t.Fatal("changed owner accepted")
	}
}
func TestPublicationFailureResumesReservedID(t *testing.T) {
	repo, journal := setup(t)
	data, hash := input(t, `{"source":"crw","kind":"scope","id":"scope-old","title":"Historical scope","payload":{"allowedRecipients":["old-parent"]}}`)
	repo.PublicationHook = func(stage string) error {
		if stage == "checkpoint" {
			return errors.New("injected after publication")
		}
		return nil
	}
	first, err := Import(t.Context(), repo, actor, data, hash, journal)
	if err == nil {
		t.Fatal("injected failure ignored")
	}
	repo.PublicationHook = nil
	second, err := Import(t.Context(), repo, actor, data, hash, journal)
	if err != nil {
		t.Fatal(err)
	}
	if second.Rows[0].Outcome != "imported" || !second.Rows[0].Reused || second.Rows[0].LINAID != first.Rows[0].LINAID {
		t.Fatal(first, second)
	}
	head, _ := repo.Head(t.Context())
	third, err := Import(t.Context(), repo, actor, data, hash, journal)
	if err != nil || third.Rows[0].LINAID != second.Rows[0].LINAID {
		t.Fatal(third, err)
	}
	after, _ := repo.Head(t.Context())
	if after != head {
		t.Fatal("retry republished")
	}
}
func TestMissingAndDuplicateReferencesReported(t *testing.T) {
	for _, rows := range [][]string{
		{`{"source":"tracker","kind":"issue","id":"one","title":"One","project":{"source":"tracker","kind":"project","id":"absent"}}`},
		{`{"source":"tracker","kind":"issue","id":"one","title":"One"}`, `{"source":"tracker","kind":"issue","id":"one","title":"Duplicate"}`},
	} {
		repo, journal := setup(t)
		data, hash := input(t, rows...)
		report, err := Import(t.Context(), repo, actor, data, hash, journal)
		if err == nil {
			t.Fatal("bad relationship accepted")
		}
		if err := report.Validate(len(rows)); err != nil {
			t.Fatal(err)
		}
		for _, row := range report.Rows {
			if row.Outcome != "lost" {
				t.Fatal(row)
			}
		}
	}
}
func TestReportCoverage(t *testing.T) {
	good := Report{Rows: []Row{{Index: 0, Outcome: "imported", LINAID: "new-id"}, {Index: 1, Outcome: "unsupported", Reason: "kind"}}}
	if err := good.Validate(2); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Report{
		{Rows: good.Rows[:1]},
		{Rows: []Row{good.Rows[0], good.Rows[0]}},
		{Rows: []Row{{Index: 0, Outcome: "skipped"}, good.Rows[1]}},
		{Rows: []Row{{Index: 0, Outcome: "imported"}, good.Rows[1]}},
		{Rows: []Row{good.Rows[0], {Index: 1, Outcome: "imported", LINAID: "new-id"}}},
	} {
		if bad.Validate(2) == nil {
			t.Fatal("incomplete report accepted", bad)
		}
	}
}

func TestRetryKeepsLaterLINAEdits(t *testing.T) {
	repo, journal := setup(t)
	data, hash := input(t, `{"source":"tracker","kind":"issue","id":"one","title":"Original"}`)
	first, err := Import(t.Context(), repo, actor, data, hash, journal)
	if err != nil {
		t.Fatal(err)
	}
	r, rev, err := repo.Read(t.Context(), first.Rows[0].LINAID)
	if err != nil {
		t.Fatal(err)
	}
	r.Data = append(r.Data, []byte("\nA later user decision.\n")...)
	if _, err = repo.Commit(t.Context(), records.Change{ID: r.ID, Base: rev, Path: r.Path, Data: r.Data}, actor); err != nil {
		t.Fatal(err)
	}
	head, _ := repo.Head(t.Context())
	again, err := Import(t.Context(), repo, actor, data, hash, journal)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := repo.Head(t.Context())
	if head != after || !again.Rows[0].Reused {
		t.Fatal("retry overwrote later work")
	}
}
func TestInventoryAndCycles(t *testing.T) {
	data, _ := input(t, `{"source":"tracker","kind":"issue","id":"one","title":"One"}`)
	data = bytes.Replace(data, []byte(`"tracker":1`), []byte(`"tracker":2`), 1)
	if Verify(data, fmt.Sprintf("%x", sha256.Sum256(data))) == nil {
		t.Fatal("incomplete fetched inventory accepted")
	}
	repo, journal := setup(t)
	data, hash := input(t,
		`{"source":"intent","kind":"intent_card","id":"a","title":"A","parent":{"source":"intent","kind":"intent_card","id":"b"}}`,
		`{"source":"intent","kind":"intent_card","id":"b","title":"B","parent":{"source":"intent","kind":"intent_card","id":"a"}}`)
	report, err := Import(t.Context(), repo, actor, data, hash, journal)
	if err == nil || report.Validate(2) != nil {
		t.Fatal(report, err)
	}
	for _, row := range report.Rows {
		if row.Outcome != "lost" {
			t.Fatal(row)
		}
	}
}

func TestBindingsEnterNativeCoreIndex(t *testing.T) {
	repo, journal := setup(t)
	data, hash := input(t,
		`{"source":"tracker","kind":"issue","id":"task-old","title":"Measured result","project":{"source":"tracker","kind":"project","id":"project-old"}}`,
		`{"source":"tracker","kind":"project","id":"project-old","title":"Experiment","done_condition":"Result recorded"}`)
	report, err := Import(t.Context(), repo, actor, data, hash, journal)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := canon.OpenReader(t.Context(), filepath.Join(filepath.Dir(journal), "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	check := func() {
		t.Helper()
		var edges int
		err := reader.Read(t.Context(), func(tx *canon.Tx) error {
			return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM records_edges WHERE source=? AND relation='belongs_to' AND target=?", report.Rows[0].LINAID, report.Rows[1].LINAID).Scan(&edges)
		})
		if err != nil || edges != 1 {
			t.Fatalf("native project binding: edges=%d error=%v", edges, err)
		}
	}
	check()
	if err = repo.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	check()
	hits, err := repo.Query(t.Context(), records.Params{Person: actor.Person, Kind: "todo", Search: "Measured result"})
	if err != nil || len(hits) != 1 || hits[0].ID != report.Rows[0].LINAID {
		t.Fatal(hits, err)
	}
}

func TestImportedTodoPlanningViews(t *testing.T) {
	repo, journal := setup(t)
	data, hash := input(t,
		`{"source":"tracker","kind":"issue","id":"task-old","title":"Write report"}`,
		`{"source":"tracker","kind":"milestone","id":"checkpoint-old","title":"Checkpoint","deadline":"2026-12-01","done_condition":"Evidence checked","done":true}`)
	report, err := Import(t.Context(), repo, actor, data, hash, journal)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := repo.Query(t.Context(), records.Params{Person: actor.Person, View: "board"})
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	for _, row := range rows {
		if row.ID == report.Rows[0].LINAID && row.State != "open" {
			t.Fatal(row)
		}
		if row.ID == report.Rows[1].LINAID && (row.State != "completed" || row.Todo == nil || row.Todo.DeadlineSource == "") {
			t.Fatal(row)
		}
	}
}
