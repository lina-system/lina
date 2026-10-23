// SPDX-License-Identifier: Apache-2.0
package reactor

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/worker"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRecoveryAllowanceSurvivesStoreReopen(t *testing.T) {
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
	cfg := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()}
	db, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	rec := Record{ID: "thread", Person: "person", Agent: "agent", Input: "input", State: "unknown", Thread: worker.Thread{ID: "thread", TurnID: "turn"}}
	snap := worker.ThreadSnapshot{Thread: rec.Thread, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure}, EffectsKnown: true}
	rec, send := Observe(rec, snap)
	if !send {
		t.Fatal("first retry refused")
	}
	if err = (Recovery{DB: db}).Put(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	foreign := rec
	foreign.Person = "other-person"
	if err = (Recovery{DB: db}).Put(t.Context(), foreign); err == nil {
		t.Fatal("foreign task replaced recovery binding")
	}
	replay := rec
	replay.Retries = 0
	if err = (Recovery{DB: db}).Put(t.Context(), replay); err == nil {
		t.Fatal("stored recovery allowance reset")
	}

	if err = db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, err = canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	records, err := (Recovery{DB: db}).Records(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%+v %v", records, err)
	}
	if !reflect.DeepEqual(records[0], rec) {
		t.Fatal("reopen changed recovery intent")
	}
	if _, send = Observe(records[0], snap); send {
		t.Fatal("reopen granted a second send")
	}
}
