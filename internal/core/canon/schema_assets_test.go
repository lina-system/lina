// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"testing"
)

func TestApplicationMigrationsIncludeAssetsAndInbox(t *testing.T) {
	t.Parallel()
	for _, upgrade := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "from_assets_v1"}[upgrade], func(t *testing.T) {
			cfg := storeConfig(t)
			if upgrade {
				cfg.Migrations = []Migration{{Version: 1, SQL: schemaAssets}}
				old := openStore(t, cfg)
				if err := old.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			cfg.Migrations = Migrations()
			s := openStore(t, cfg)
			for _, table := range []string{"assets", "inbox"} {
				var count int
				err := s.Read(t.Context(), func(tx *Tx) error {
					return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?", table).Scan(&count)
				})
				if err != nil || count != 1 {
					t.Errorf("table %s: count=%d err=%v", table, count, err)
				}
			}
		})
	}
}

func TestAssetMigrationUpgradesAndReopensExistingCanon(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	cfg.Migrations = nil
	s := openStore(t, cfg)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = Migrations()
	s = openStore(t, cfg)
	if err := s.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO assets(id,area,observed_path,revision,person_id,agent_id) VALUES ('test','user','name',1,'person','agent')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, cfg)
	var name string
	if err := s.Read(t.Context(), func(tx *Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT observed_path FROM assets WHERE id='test'").Scan(&name)
	}); err != nil || name != "name" {
		t.Fatalf("reopen: %q, %v", name, err)
	}
	version, epoch, err := Inspect(t.Context(), cfg.Path)
	if err != nil || version != len(Migrations()) || epoch != cfg.Epoch {
		t.Fatalf("migration state: %d, %d, %v", version, epoch, err)
	}
}
