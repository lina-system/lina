// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestGrowthCommittedAdoptionSurvivesNoticeFailure(t *testing.T) {
	for _, table := range []string{"conv_event", "conversation_order"} {
		t.Run(table, func(t *testing.T) {
			r, session, agent, other := managementFixture(t)
			if err := r.agents.Delete(t.Context(), other, r.person, time.Now()); err != nil {
				t.Fatal(err)
			}
			r.agent, r.conversation = agent, "main:"+agent
			r.activities = lifecycle.NewRegistry(nil)
			r.ctx = t.Context()
			var err error
			r.topics, err = clientsrv.NewTopics("1", clientsrv.TopicOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(r.topics.Close)
			r.identity.Operations = clientsrv.ManagementOperations(r)
			r.router = &conversationRouter{root: r, views: map[string]*conversationRuntime{agent: r}}
			r.materials = &conversationMaterials{}
			if err = r.startGrowth(t.Context(), r.controls); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { r.growth.stop(); <-r.growth.done })
			if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), "CREATE TRIGGER fail_notice BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(ABORT,'injected notice failure'); END;")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			session.CanDecide, session.ClientID = true, "fixture-client"
			op := "growth_adopt"
			result, err := r.growthOperation(t.Context(), session, protocol.Envelope{MessageID: "adopt", Payload: protocol.AgentRequest{Operation: &op, AgentID: &agent, Candidate: &protocol.GrowthCandidate{ID: "step", Expected: "1", Snapshot: "learned", Notice: "fixture learning"}}})
			if err != nil {
				t.Fatalf("committed adoption reported failure: %v", err)
			}
			body := result.Payload.(protocol.AgentResult)
			if result.Effect != protocol.EffectApplied || body.Revision == nil || *body.Revision != "2" || body.Notices == nil || len(*body.Notices) != 1 {
				t.Fatalf("adoption=%+v", result)
			}
			if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), "DROP TRIGGER fail_notice")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// Recovery uses the durable outbox and publishes one item, even on replay.
			for range 2 {
				if err = r.publishGrowth(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, sub, err := r.topics.Subscribe("conversation:"+r.conversation, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			if snapshot.Snapshot == nil || len(snapshot.Snapshot.Items) != 1 {
				t.Fatalf("recovered stream=%+v", snapshot)
			}
			var item protocol.ItemDone
			if err := json.Unmarshal(snapshot.Snapshot.Items[0].Data, &item); err != nil || item.Text == nil || *item.Text != "fixture learning" {
				t.Fatalf("notice=%+v %v", item, err)
			}
			if err := r.db.Read(context.Background(), func(tx *canon.Tx) error {
				for _, table := range []string{"agent_growth_steps", "moirai_tasks", "moirai_applied", "conv_event"} {
					var count int
					if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
						return err
					}
					if count != 1 {
						t.Errorf("%s rows=%d", table, count)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
