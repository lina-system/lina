// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestInstalledRuntimeMigrationHistory(t *testing.T) {
	for _, retracted := range []bool{false, true} {
		for count := 1; count <= 3; count++ {
			name := "pre_retraction"
			if retracted {
				name = "with_retraction"
			}
			t.Run(name+string(rune('0'+count)), func(t *testing.T) {
				cfg := storeConfig(t)
				legacy := append([]Migration{}, Migrations()[:10]...)
				if retracted {
					legacy = append(legacy, Migration{Version: 11, SQL: retractSchema + retractLineageSchema + retractViewsSchema + retractSearchSchema})
				}
				for _, file := range []string{"turn-v1.sql", "turn-control-v1.sql", "turn-outcome-v1.sql"}[:count] {
					sql, err := os.ReadFile("testdata/" + file)
					if err != nil {
						t.Fatal(err)
					}
					legacy = append(legacy, Migration{Version: len(legacy) + 1, SQL: string(sql)})
				}
				cfg.Migrations = legacy
				db := openStore(t, cfg)
				if err := db.Write(t.Context(), func(tx *Tx) error {
					_, err := tx.ExecContext(t.Context(), `INSERT INTO inbox(conversation,source,native_id,body) VALUES('upgrade','client','old',X'01');
INSERT INTO turn_runs(id,conversation,inbox_id,prepared,state) VALUES('old-turn','upgrade',1,1,'completed');`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				cfg.Migrations = Migrations()
				if !retracted {
					before, err := os.ReadFile(cfg.Path)
					if err != nil {
						t.Fatal(err)
					}
					unexpected, err := Open(t.Context(), cfg)
					if unexpected != nil {
						unexpected.Close(t.Context())
					}
					if !errors.Is(err, ErrDevelopmentSchema) {
						t.Fatalf("mismatched development schema was not clearly refused: %v", err)
					}
					after, err := os.ReadFile(cfg.Path)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(before, after) {
						t.Fatal("refused development database was normalized")
					}
					return
				}
				db = openStore(t, cfg)
				if err := db.Read(t.Context(), func(tx *Tx) error {
					var state string
					if err := tx.QueryRowContext(t.Context(), "SELECT state FROM turn_runs WHERE id='old-turn'").Scan(&state); err != nil {
						return err
					}
					if state != "completed" {
						t.Fatal("old turn changed")
					}
					for _, table := range []string{"turn_control", "turn_outcome", "retract_meta", "sum_node", "owner_binding", "sign_in_host", "sign_in_identities", "channel_connections", "telegram_buttons", "exchanges"} {
						var n int
						if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestFutureMigrationDoesNotRenumberRuntime(t *testing.T) {
	all := Migrations()
	for i, name := range []string{"turn-v1.sql", "turn-control-v1.sql", "turn-outcome-v1.sql"} {
		body, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sql := strings.TrimSpace(strings.TrimPrefix(string(body), "-- SPDX-License-Identifier: Apache-2.0\n"))
		found := 0
		for _, m := range all {
			if strings.TrimSpace(m.SQL) == sql {
				found = m.Version
			}
		}
		if found != 12+i {
			t.Errorf("runtime schema %s version=%d, want=%d", name, found, 12+i)
		}
	}
	signInVersion := 0
	for _, m := range all {
		if m.SQL == AccountSignInSchema {
			signInVersion = m.Version
		}
	}
	if signInVersion != 16 {
		t.Errorf("account sign-in version=%d, want=16", signInVersion)
	}
	if t.Failed() {
		return
	}
	cfg := storeConfig(t)
	cfg.Migrations = all
	db := openStore(t, cfg)
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = append(append([]Migration{}, all...), Migration{Version: len(all) + 1, SQL: "CREATE TABLE future_fixture (id TEXT PRIMARY KEY);"})
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var n int
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM future_fixture").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
}
