// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// identityBoot composes a runtime the way Core does for a sender-identity test:
// canon, vault, removals, records and materials, with no people record yet.
func identityBoot(t *testing.T) *conversationRuntime {
	t.Helper()
	ctx := t.Context()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(ctx, canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations(), NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	vault, err := secrets.OpenFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { vault.Close() })
	removals, err := retract.Open(ctx, retract.Config{Canon: db, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removals.Close() })
	recordStore, err := records.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { recordStore.Close() })
	materials, err := assets.Open(ctx, root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { materials.Close() })
	writer, err := assets.OpenMaterialWriter(ctx, materials)
	if err != nil {
		t.Fatal(err)
	}
	return &conversationRuntime{ctx: ctx, db: db, person: "owner", vault: vault, records: recordStore, removals: removals, materials: &conversationMaterials{store: materials, writer: writer, actor: assets.Actor{PersonID: "owner"}}}
}

// addMira commits the people record whose contact is Mira's email. The record
// is committed at the point the test needs it, so a test can add it after the
// messages have arrived.
func addMira(t *testing.T, r *conversationRuntime) {
	t.Helper()
	if _, err := r.records.Commit(t.Context(), records.Change{ID: "person-mira", Path: "areas/people/mira.md", Data: []byte("---\nid: \"person-mira\"\nkind: person\nstate: active\nperson_id: \"owner\"\n---\n\n# Mira\n\nA short profile.\n\n## Contacts\n\n- email mira@example.com — Webex profile\n")}, records.Actor{Person: "owner", Agent: "owner"}); err != nil {
		t.Fatal(err)
	}
}

// identityRows counts the admitted record, the sender identity and the usable
// exact link to the people record for one author.
func identityRows(ctx context.Context, db *canon.Store, platform, user string) (senders, links, records int, err error) {
	err = db.Read(ctx, func(tx *canon.Tx) error {
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM messenger_record WHERE message='m-1' AND state='live'").Scan(&records); err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM identity_sender WHERE platform=? AND user=?", platform, user).Scan(&senders); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM identity_link WHERE platform=? AND user=? AND record='person-mira' AND class='exact' AND state='active'", platform, user).Scan(&links)
	})
	return senders, links, records, err
}
