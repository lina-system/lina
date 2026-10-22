// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
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
