// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestWorkerWaitingPreservesRequestTime(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	session := &workerSession{origin: r, thread: worker.Thread{ID: "thread", ProcessGeneration: 1}, input: ledger.Event{Surface: ledger.Surface{Kind: "tui"}}, questions: map[string]*workerQuestion{}}
	r.workers = &workerRuntime{sessions: map[string]*workerSession{"thread": session}}
	t.Cleanup(func() { r.workers = nil })
	source := workerWaitingSource{r: r, names: map[string]string{r.agent: "Lina"}}
	before := time.Now()
	session.presentQuestion(worker.Event{}, "question", "input", "Choose a label", []protocol.FormField{{Name: "answer", Type: protocol.FormFieldTypeText}}, nil)
	after := time.Now()
	items, err := source.Snapshot(t.Context(), r.person)
	if err != nil || len(items) != 1 {
		t.Fatalf("question not published: %+v %v", items, err)
	}
	requested := items[0].Item.Requested
	if requested.IsZero() || requested.Before(before) || requested.After(after) {
		t.Errorf("request time = %v, want Core receipt between %v and %v", requested, before, after)
	}
	again, err := source.Snapshot(t.Context(), r.person)
	if err != nil || len(again) != 1 || !again[0].Item.Requested.Equal(requested) {
		t.Fatalf("refresh changed request time: %+v %v", again, err)
	}
}

func TestWorkerWaitingOrdersAlongsideRunInput(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	session := &workerSession{origin: r, thread: worker.Thread{ID: "thread", ProcessGeneration: 1}, input: ledger.Event{Surface: ledger.Surface{Kind: "tui"}}, questions: map[string]*workerQuestion{}}
	r.workers = &workerRuntime{sessions: map[string]*workerSession{"thread": session}}
	t.Cleanup(func() { r.workers = nil })
	older := time.Now().Add(-time.Hour)
	session.presentQuestion(worker.Event{}, "question", "input", "Choose a label", []protocol.FormField{{Name: "answer", Type: protocol.FormFieldTypeText}}, nil)
	newer := time.Now().Add(time.Hour)
	// Seed the persisted run producer on either side of the live question.
	for id, at := range map[string]time.Time{"older-run": older, "newer-run": newer} {
		record := runs.Record{ID: id, Origin: runs.Origin{Person: r.person, Agent: r.agent}, Brief: runs.Brief{Goal: "Choose a route"}, State: runs.NeedsInput, NoticeVersion: 1, NoticeAt: at.UnixNano()}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := tx.ExecContext(t.Context(), "INSERT INTO background_runs(id,record) VALUES(?,?)", id, raw)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	source := workerWaitingSource{r: r, names: map[string]string{r.agent: "Lina"}}
	service := waiting.NewWithLive(r.db, map[string]waiting.Source{"run": &waiting.RunInputSource{}}, map[string]waiting.LiveSource{"worker": source})
	want := []waiting.Ref{{Source: "run", ID: "older-run", Revision: 1}, {Source: "worker", ID: session.requestKey("question"), Revision: 1}, {Source: "run", ID: "newer-run", Revision: 1}}
	for _, order := range []waiting.Order{waiting.Priority, waiting.Lightest} {
		for refresh := 0; refresh < 2; refresh++ {
			list, err := service.Build(t.Context(), r.person, waiting.Options{Now: newer, Order: order})
			if err != nil {
				t.Fatal(err)
			}
			got := []waiting.Ref{}
			for _, entry := range list.Entries {
				got = append(got, entry.Ref)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s refresh %d: got %v, want %v", order, refresh, got, want)
			}
		}
	}
}
