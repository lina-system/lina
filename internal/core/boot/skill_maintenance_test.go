// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestSkillMaintenanceNoticeDeliveryRecoversAfterCommit(t *testing.T) {
	for _, table := range []string{"conv_event", "conversation_order"} {
		for _, selected := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/selected=%t", table, selected), func(t *testing.T) {
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
				rows, err := r.agents.List(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				var dev string
				for _, a := range rows {
					if a.Preset == "dev" {
						dev = a.ID
					}
				}
				if dev == "" {
					t.Fatal("dev starter missing")
				}
				target, alternate := agent, dev
				if selected {
					target, alternate = dev, agent
					if err := r.agents.Select(t.Context(), target, r.person); err != nil {
						t.Fatal(err)
					}
				}

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
				if table == "conversation_order" {
					// Admission already committed. A new selection must not move
					// that receipt to another agent's conversation on retry.
					if err := r.agents.Select(t.Context(), alternate, r.person); err != nil {
						t.Fatal(err)
					}
				}
				for range 2 {
					if err = r.publishSkillMaintenance(t.Context()); err != nil {
						t.Fatal(err)
					}
				}

				for _, a := range rows {
					snapshot, sub, err := r.topics.Subscribe("conversation:main:"+a.ID, nil)
					if err != nil {
						t.Fatal(err)
					}
					sub.Close()
					if snapshot.Snapshot == nil {
						t.Fatal("missing snapshot")
					}
					count := 0
					for _, entry := range snapshot.Snapshot.Items {
						if entry.Kind != protocol.KindItemDone {
							continue
						}
						var item protocol.ItemDone
						if err := json.Unmarshal(entry.Data, &item); err != nil {
							t.Fatal(err)
						}
						if item.Text != nil && *item.Text == applied.Notice {
							count++
						}
					}
					want := 0
					if a.ID == target {
						want = 1
					}
					if count != want {
						t.Fatalf("recovered notice for %s: count=%d want=%d", a.Preset, count, want)
					}
				}

				notices, err := r.skills.MaintenanceNotices(t.Context())
				if err != nil || len(notices) != 1 || notices[0].Undo != applied.Undo {
					t.Fatalf("changed receipt=%+v %v", notices, err)
				}
			})
		}
	}
}
