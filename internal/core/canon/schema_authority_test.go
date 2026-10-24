// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"testing"
)

func TestApprovalMigrationPreservesExistingEffectClaims(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	migrations := Migrations()
	approvalIndex := -1
	for i, m := range migrations {
		if m.SQL == authorityApprovalsSchema {
			approvalIndex = i
			break
		}
	}
	if approvalIndex < 1 || migrations[approvalIndex-1].SQL != authorityEffectsSchema {
		t.Fatal("approval migration must follow effect schema")
	}
	// Start with the complete integrated schema immediately before approvals,
	// including the published assets/inbox migrations and the effect ledger.
	cfg.Migrations = migrations[:approvalIndex]
	s := openStore(t, cfg)
	version, _, err := Inspect(t.Context(), cfg.Path)
	if err != nil {
		t.Fatal(err)
	}
	if want := migrations[approvalIndex].Version - 1; version != want {
		t.Fatalf("upgrade starts at version %d, want %d", version, want)
	}
	if err := s.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO authority_effects(effect_key,fingerprint,record,running) VALUES('effect','hash',?,1)", []byte(`{"kept":true}`))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	cfg.Migrations = Migrations()
	s = openStore(t, cfg)
	if err := s.Read(t.Context(), func(tx *Tx) error {
		var record []byte
		var running bool
		if err := tx.QueryRowContext(t.Context(), "SELECT record,running FROM authority_effects WHERE effect_key='effect'").Scan(&record, &running); err != nil {
			return err
		}
		if string(record) != `{"kept":true}` || !running {
			t.Fatal("migration changed existing claim")
		}
		var count int
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM authority_approvals").Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	openStore(t, cfg)
}

func TestMigrationsInstallAssetsInboxAndAuthorityTables(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	cfg.Migrations = Migrations()
	s := openStore(t, cfg)
	if err := s.Read(t.Context(), func(tx *Tx) error {
		for _, table := range []string{"assets", "asset_revisions", "asset_exports", "asset_trash_events", "inbox", "authority_effects", "authority_targets", "authority_approvals", "authority_standing"} {
			var count int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
