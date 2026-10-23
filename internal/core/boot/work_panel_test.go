// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
)

func TestWorkPanelCoreSessionStart(t *testing.T) {
	r, _ := runningWorkerFixture(t)
	p, err := r.ownerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if p.Rows == nil || len(*p.Rows) != 1 {
		t.Fatalf("missing worker: %+v", p)
	}
	raw, _ := json.Marshal((*p.Rows)[0].Line)
	var line map[string]any
	json.Unmarshal(raw, &line)
	if line["started_at"] != time.Unix(100, 0).UTC().Format(time.RFC3339Nano) {
		t.Fatalf("Core start missing: %s", raw)
	}
	if line["now"] == nil || line["now"] == "" {
		t.Fatalf("Core activity missing: %s", raw)
	}
}

func TestWorkPanelActivityChangesOnlyOnObservation(t *testing.T) {
	r, _ := runningWorkerFixture(t)
	session := r.workers.sessions["fixture"]
	var last string
	for _, phase := range []string{"command", "file_change", "delta"} {
		session.observeActivity(worker.Event{Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: phase, Text: "private command and output"}})
		p, err := r.ownerSnapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		line := (*p.Rows)[0].Line
		if line.Now == nil || *line.Now == "" || *line.Now == last || strings.Contains(*line.Now, "private") {
			t.Fatalf("activity lost or leaked: %+v", line)
		}
		last = *line.Now
		p, err = r.ownerSnapshot(t.Context())
		if err != nil || *(*p.Rows)[0].Line.Now != last {
			t.Fatal("no observation changed Now", err)
		}
	}
}

func TestWorkPanelBackgroundRun(t *testing.T) {
	r := runRuntime(t)
	record := runs.Record{ID: "panel-run", Origin: runs.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation}, Brief: runs.Brief{Goal: "Read the guide"}, State: runs.Working, Created: time.Now().Add(-2 * time.Hour).UnixNano()}
	raw, _ := json.Marshal(record)
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO background_runs(id,record) VALUES(?,?)", record.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p, err := r.ownerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if p.Rows == nil || len(*p.Rows) != 1 || (*p.Rows)[0].Line.StartedAt == nil || (*p.Rows)[0].Line.Name != record.Brief.Goal {
		t.Fatalf("background card absent: %+v", p.Rows)
	}
}

func TestWorkPanelCoreAttentionOrder(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		for i := 0; i < 50; i++ {
			id := fmt.Sprintf("item-%02d", i)
			status := "working"
			if i >= 47 {
				status = "input_needed"
			}
			card := work.Card{Task: work.Task{ID: id, PersonID: r.person, AgentID: r.agent, Title: id}, State: status, Run: &work.Run{ID: id, Generation: 1}}
			raw, err := json.Marshal(card)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_cards(task_id,record) VALUES(?,?)", id, raw); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p, err := r.ownerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if p.Rows == nil || len(*p.Rows) != 50 {
		t.Fatal("wrong number of Core cards")
	}
	for i, row := range *p.Rows {
		if (row.Line.Waiting == "1") != (i < 3) {
			t.Fatalf("waiting order lost at %d: %+v", i, row)
		}
	}
}
