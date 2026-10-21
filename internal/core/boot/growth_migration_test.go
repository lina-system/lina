// SPDX-License-Identifier: Apache-2.0
package boot_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/accounts"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func TestGrowthUpgradesPublishedInstalledSchemas(t *testing.T) {
	// These are the published installed versions before growth. Never derive
	// their order from the new ApplicationMigrations being tested.
	published := canon.Migrations()[:15]
	published = append(published, canon.Migration{Version: 16, SQL: accounts.SignInSchema}, canon.Migration{Version: 17, SQL: canon.ChannelsSchema}, canon.Migration{Version: 18, SQL: canon.TelegramSchema}, canon.Migration{Version: 19, SQL: canon.ExchangeSchema})
	for version := 12; version <= 19; version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			root := stateRoot(t)
			ctl, err := ctlfiles.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer ctl.Close()
			if err = ctl.Acquire(); err != nil {
				t.Fatal(err)
			}
			cfg := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: published[:version]}
			db, err := canon.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), `INSERT INTO agents VALUES('fixture-agent','fixture-name','',1,'{}',0);
 INSERT INTO agent_revisions VALUES('fixture-agent',1,'fixture-commit','fixture-person');
 INSERT INTO agent_preparations VALUES('fixture-turn','fixture-agent',1);`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			cfg.Epoch, cfg.Migrations = 2, boot.ApplicationMigrations()
			db, err = canon.Open(t.Context(), cfg)
			if err != nil {
				t.Fatalf("upgrade from installed v%d: %v", version, err)
			}
			defer db.Close(context.Background())
			if err = db.Read(t.Context(), func(tx *canon.Tx) error {
				var n int
				if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM agent_growth_steps").Scan(&n); err != nil {
					return err
				}
				var rev int
				if err := tx.QueryRowContext(t.Context(), "SELECT revision FROM agent_preparations WHERE turn_id='fixture-turn'").Scan(&rev); err != nil {
					return err
				}
				if rev != 1 {
					t.Fatalf("lost preparation: %d", rev)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
