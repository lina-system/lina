// SPDX-License-Identifier: Apache-2.0
package waiting_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/waiting"
)

func TestRunEffectSourceDeliveryOwnershipAndResolution(t *testing.T) {
	for _, test := range []struct {
		name                      string
		state                     runs.State
		delivered                 int
		answered, absent, deleted bool
		person, conversation      string
		want                      int
	}{
		{name: "working", state: runs.Working, delivered: 1, want: 1},
		{name: "landed", state: runs.Landed, delivered: 1, want: 1},
		{name: "queued", state: runs.Landed},
		{name: "answered", state: runs.Landed, delivered: 1, answered: true},
		{name: "ordinary run", state: runs.Landed, delivered: 1, absent: true},
		{name: "different person", state: runs.Landed, delivered: 1, person: "stranger"},
		{name: "different lane", state: runs.Landed, delivered: 1, conversation: "other"},
		{name: "deleted agent", state: runs.Landed, delivered: 1, deleted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, _ := database(t)
			record := runs.Record{ID: "run", State: test.state, Origin: runs.Origin{Person: "person", Agent: "agent", Conversation: "conversation"}, Brief: runs.Brief{Goal: "Prepare report"}, Created: now.Add(-time.Hour).UnixNano(), EffectsQuestion: &runs.EffectQuestion{Noticed: true}}
			if test.answered {
				record.EffectsQuestion.Answer = &runs.EffectAnswer{Person: "person", Action: "decline"}
			}
			if test.absent {
				record.EffectsQuestion = nil
			}
			conversation := record.Origin.Conversation
			if test.conversation != "" {
				conversation = test.conversation
			}
			notice := lane.Notice{ID: "run:effects", Run: record.ID, Conversation: conversation, Summary: "Choose how to continue"}
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			noticeRaw, err := json.Marshal(notice)
			if err != nil {
				t.Fatal(err)
			}
			if err = db.Write(t.Context(), func(tx *canon.Tx) error {
				if _, err := tx.ExecContext(t.Context(), "INSERT INTO background_runs(id,record) VALUES(?,?)", record.ID, raw); err != nil {
					return err
				}
				_, err := tx.ExecContext(t.Context(), "INSERT INTO lane_notices(id,conversation,record,delivered) VALUES(?,?,?,?)", notice.ID, conversation, noticeRaw, test.delivered)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			source := waiting.RunEffectSource{AgentName: func(string) string {
				if test.deleted {
					return ""
				}
				return "Lina"
			}}
			service := waiting.New(db, map[string]waiting.Source{"run_effect": source})
			person := "person"
			if test.person != "" {
				person = test.person
			}
			list, err := service.Build(t.Context(), person, waiting.Options{Now: now})
			if err != nil || list.Total != test.want {
				t.Fatalf("waiting=%+v err=%v", list, err)
			}
			if test.want == 1 {
				entry := list.Entries[0]
				if entry.Ref.ID != notice.ID || entry.Ref.Revision != 1 || entry.View.Context != record.Brief.Goal || entry.View.Sentence != notice.Summary {
					t.Fatalf("card projection=%+v", entry)
				}
				if err = db.Read(t.Context(), func(tx *canon.Tx) error {
					item, err := source.Read(t.Context(), tx, notice.ID)
					if err == nil && !item.Requested.Equal(time.Unix(0, record.Created)) {
						t.Errorf("request age lost: %+v", item)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
