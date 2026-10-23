// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestSkillMaintenanceNoticeDeliveryRecoversAfterCommit(t *testing.T) {
	for _, table := range []string{"conv_event", "conversation_order"} {
		t.Run(table, func(t *testing.T) {
			r, _, agent, other := managementFixture(t)
			if err := r.agents.Delete(t.Context(), other, r.person, time.Now()); err != nil {
				t.Fatal(err)
			}
			r.agent, r.conversation = agent, "main:"+agent
			var err error
			r.topics, err = clientsrv.NewTopics("1", clientsrv.TopicOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer r.topics.Close()
			r.router = &conversationRouter{root: r, views: map[string]*conversationRuntime{agent: r}}
			r.materials = &conversationMaterials{}
			r.skillMaintenance = &skillMaintenanceRuntime{delivered: map[int64]bool{}}
			r.skills, err = skills.Open(t.TempDir(), r.db, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer r.skills.Close()
			created, err := r.skills.Patch(t.Context(), skills.Patch{ID: "fixture", Files: skills.Bundle{"SKILL.md": {Text: "---\nname: fixture\ndescription: Retired procedure.\n---\nThis procedure is retired."}}})
			if err != nil {
				t.Fatal(err)
			}
			var applied skills.Applied
			if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
				var err error
				applied, err = r.skills.MaintainTx(t.Context(), tx, skills.Maintenance{Kind: "prune", Target: created.Undo, Reason: "Retired."})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), "CREATE TRIGGER fail_notice BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(ABORT,'injected delivery failure'); END;")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err = r.publishSkillMaintenance(t.Context()); err == nil {
				t.Fatal("delivery did not fail")
			}
			catalog, err := r.skills.Catalog(t.Context())
			if err != nil || len(catalog) != 0 {
				t.Fatalf("failed delivery rolled back committed prune: %+v %v", catalog, err)
			}
			if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), "DROP TRIGGER fail_notice")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err = r.publishSkillMaintenance(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, sub, err := r.topics.Subscribe("conversation:"+r.conversation, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			if snapshot.Snapshot == nil || len(snapshot.Snapshot.Items) != 1 {
				t.Fatalf("lost or duplicate recovered notice: %+v", snapshot)
			}
			var item protocol.ItemDone
			if err = json.Unmarshal(snapshot.Snapshot.Items[0].Data, &item); err != nil || item.Text == nil || *item.Text != applied.Notice {
				t.Fatalf("recovered notice=%+v %v", item, err)
			}
			notices, err := r.skills.MaintenanceNotices(t.Context())
			if err != nil || len(notices) != 1 || notices[0].Undo != applied.Undo {
				t.Fatalf("changed receipt=%+v %v", notices, err)
			}
		})
	}
}

func maintenanceNoticeFixture(t *testing.T) (*conversationRuntime, string) {
	t.Helper()
	r, _, _, other := managementFixture(t)
	agents, err := r.agents.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r.agent, r.conversation = agents[0].ID, "main:"+agents[0].ID
	r.topics, err = clientsrv.NewTopics("1", clientsrv.TopicOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.topics.Close)
	r.router = &conversationRouter{root: r, views: map[string]*conversationRuntime{r.agent: r}}
	r.materials = &conversationMaterials{}
	r.skillMaintenance = &skillMaintenanceRuntime{delivered: map[int64]bool{}}
	r.skills, err = skills.Open(t.TempDir(), r.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.skills.Close() })
	created, err := r.skills.Patch(t.Context(), skills.Patch{ID: "fixture", Files: skills.Bundle{"SKILL.md": {Text: "---\nname: fixture\ndescription: Retired.\n---\nRetired procedure."}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := r.skills.MaintainTx(t.Context(), tx, skills.Maintenance{Kind: "prune", Target: created.Undo, Reason: "Retired."})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return r, other
}

func TestSkillMaintenanceNoticeRetryUsesRecordedConversation(t *testing.T) {
	r, other := maintenanceNoticeFixture(t)
	view, err := r.router.view(t.Context(), clientsrv.Session{PersonID: r.person}, other)
	if err != nil {
		t.Fatal(err)
	}
	notices, err := r.skills.MaintenanceNotices(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("skill-maintenance:%d", notices[0].Sequence)
	now := time.Now()
	if _, err := view.journal().Admit(t.Context(), guard.EntrySystem, ledger.Input{ID: id, ConversationID: view.conversation, AgentID: view.agent, Surface: ledger.Surface{Kind: "tui"}, ScaffoldKind: "skill_change", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: notices[0].Notice}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := r.publishSkillMaintenance(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []*conversationRuntime{r, view} {
		snapshot, sub, err := r.topics.Subscribe("conversation:"+v.conversation, nil)
		if err != nil {
			t.Fatal(err)
		}
		sub.Close()
		want := 0
		if v == view {
			want = 1
		}
		got := 0
		if snapshot.Snapshot != nil {
			for _, item := range snapshot.Snapshot.Items {
				if item.Key == id {
					got++
				}
			}
		}
		if got != want {
			t.Fatalf("notice routed to wrong conversation %s: %+v", v.conversation, snapshot.Snapshot)
		}
	}
}

func TestSkillMaintenanceUnavailableRecipientKeepsOutboxMoving(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired=%t", expired), func(t *testing.T) {
			r, other := maintenanceNoticeFixture(t)
			notices, err := r.skills.MaintenanceNotices(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			admit := func(n skills.MaintenanceNotice, agent string) {
				t.Helper()
				now := time.Now()
				_, err := r.journal().Admit(t.Context(), guard.EntrySystem, ledger.Input{ID: fmt.Sprintf("skill-maintenance:%d", n.Sequence), ConversationID: "main:" + agent, AgentID: agent, Surface: ledger.Surface{Kind: "tui"}, ScaffoldKind: "skill_change", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: n.Notice})
				if err != nil {
					t.Fatal(err)
				}
			}
			admit(notices[0], other)
			if _, err = r.skills.Undo(t.Context(), notices[0].Undo); err != nil {
				t.Fatal(err)
			}
			notices, err = r.skills.MaintenanceNotices(t.Context())
			if err != nil || len(notices) != 2 {
				t.Fatalf("undo notices=%+v: %v", notices, err)
			}
			admit(notices[1], r.agent)
			created, err := r.skills.Patch(t.Context(), skills.Patch{ID: "later", Files: skills.Bundle{"SKILL.md": {Text: "---\nname: later\ndescription: Retired.\n---\nRetired procedure."}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := r.skills.MaintainTx(t.Context(), tx, skills.Maintenance{Kind: "prune", Target: created.Undo, Reason: "Retired."})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			if err = r.agents.Delete(t.Context(), other, r.person, now); err != nil {
				t.Fatal(err)
			}
			if expired {
				if err = r.agents.Expire(t.Context(), now.Add(agents.TrashRetention)); err != nil {
					t.Fatal(err)
				}
			}
			// No cached view or delivery marker exists for the old recipient,
			// as on startup. Both an admitted and a new later notice must pass it.
			for range 2 {
				if err = r.publishSkillMaintenance(t.Context()); err != nil {
					t.Fatalf("unavailable recipient blocked outbox: %v", err)
				}
			}
			snapshot, sub, err := r.topics.Subscribe("conversation:"+r.conversation, nil)
			if err != nil {
				t.Fatal(err)
			}
			sub.Close()
			if snapshot.Snapshot == nil || len(snapshot.Snapshot.Items) != 2 || snapshot.Snapshot.Items[0].Key != "skill-maintenance:2" || snapshot.Snapshot.Items[1].Key != "skill-maintenance:3" {
				t.Fatalf("lost, duplicate or rerouted notices: %+v", snapshot.Snapshot)
			}
			old, err := r.journal().Get(t.Context(), "skill-maintenance:1")
			if err != nil || old.AgentID != other || old.ConversationID != "main:"+other {
				t.Fatalf("changed original destination: %+v %v", old, err)
			}
			if !expired {
				if err = r.agents.Restore(t.Context(), other, r.person, now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				if err = r.publishSkillMaintenance(t.Context()); err != nil {
					t.Fatal(err)
				}
				restored, sub, err := r.topics.Subscribe("conversation:main:"+other, nil)
				if err != nil {
					t.Fatal(err)
				}
				sub.Close()
				count := 0
				if restored.Snapshot != nil {
					for _, item := range restored.Snapshot.Items {
						if item.Key == "skill-maintenance:1" {
							count++
						}
					}
				}
				if count != 1 {
					t.Fatalf("restored recipient lost notice: %+v", restored.Snapshot)
				}
			}
		})
	}
}

func TestSkillMaintenanceRecoveryKeepsBoundedHistoryOrder(t *testing.T) {
	for _, later := range []int{3, 199, 205} {
		t.Run(fmt.Sprint(later), func(t *testing.T) {
			r, _ := maintenanceNoticeFixture(t)
			if err := r.publishSkillMaintenance(t.Context()); err != nil {
				t.Fatal(err)
			}
			for i := range later {
				id := fmt.Sprintf("later:%d", i)
				now := time.Now()
				event, err := r.journal().Admit(t.Context(), guard.EntrySystem, ledger.Input{ID: id, ConversationID: r.conversation, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui"}, ScaffoldKind: "failure", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Later event."})
				if err != nil {
					t.Fatal(err)
				}
				text := event.Body.Text()
				if err = r.publish(t.Context(), protocol.KindItemDone, id, protocol.ItemDone{Text: &text}); err != nil {
					t.Fatal(err)
				}
			}
			// A new topic hub and delivery cache model Core startup. History seeds
			// the bounded window before the maintenance outbox is reconciled.
			r.topics.Close()
			var err error
			r.topics, err = clientsrv.NewTopics("2", clientsrv.TopicOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(r.topics.Close)
			r.positioned = nil
			r.skillMaintenance.delivered = map[int64]bool{}
			if err = r.seedHistory(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err = r.publishSkillMaintenance(t.Context()); err != nil {
				t.Fatal(err)
			}
			want, err := r.historyItems(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			snapshot, sub, err := r.topics.Subscribe("conversation:"+r.conversation, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Close()
			if snapshot.Snapshot == nil {
				t.Fatal("missing snapshot")
			}
			var got []clientsrv.TopicEvent
			for _, item := range snapshot.Snapshot.Items {
				if item.Kind != protocol.KindTurnState {
					got = append(got, item)
				}
			}
			// The inactive turn row occupies one of the hub's 200 slots.
			want = want[max(0, len(want)-199):]
			if len(got) != len(want) {
				t.Fatalf("snapshot size=%d want=%d", len(got), len(want))
			}
			for i, item := range got {
				if item.Key != want[i].key {
					t.Fatalf("snapshot[%d]=%s, durable history=%s", i, item.Key, want[i].key)
				}
			}
		})
	}
}

func TestSkillMaintenanceNoticeRetriesFailedTopicPublication(t *testing.T) {
	r, _ := maintenanceNoticeFixture(t)
	r.topics.Close()
	if err := r.publishSkillMaintenance(t.Context()); err == nil {
		t.Fatal("publication to closed topic succeeded")
	}
	var err error
	r.topics, err = clientsrv.NewTopics("1", clientsrv.TopicOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.topics.Close)
	for range 2 {
		if err = r.publishSkillMaintenance(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, sub, err := r.topics.Subscribe("conversation:"+r.conversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if snapshot.Snapshot == nil || len(snapshot.Snapshot.Items) != 1 || snapshot.Snapshot.Items[0].Key != "skill-maintenance:1" {
		t.Fatalf("committed position suppressed failed delivery: %+v", snapshot.Snapshot)
	}
}

func TestSkillMaintenanceNoticeLookupFailureRemainsRetryable(t *testing.T) {
	r, _ := maintenanceNoticeFixture(t)
	if err := r.publishSkillMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.skillMaintenance.delivered = map[int64]bool{}
	rename := func(query string) {
		t.Helper()
		if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := tx.ExecContext(t.Context(), query)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	rename("ALTER TABLE agents RENAME TO unavailable_agents")
	if err := r.publishSkillMaintenance(t.Context()); err == nil {
		t.Fatal("recipient storage failure was swallowed")
	}
	if r.skillMaintenance.delivered[1] {
		t.Fatal("failed lookup marked notice delivered")
	}
	rename("ALTER TABLE unavailable_agents RENAME TO agents")
	if err := r.publishSkillMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !r.skillMaintenance.delivered[1] {
		t.Fatal("recovered lookup did not complete delivery")
	}
}
