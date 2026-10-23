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
					for _, table := range []string{"turn_control", "turn_outcome", "retract_meta", "sum_node", "owner_binding", "sign_in_host", "sign_in_identities", "channel_connections", "telegram_buttons", "exchanges", "conversation_order", "background_runs", "running_seen", "lane_notices", "lane_notice_events"} {
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

func TestPlanningUpgradePreservesPublishedKoreanSearch(t *testing.T) {
	cfg := storeConfig(t)
	// Reproduce the published dev catalog, independently of the pending plan
	// migration. An existing installation at version 33 has no plan tables.
	cfg.Migrations = append(append([]Migration{}, Migrations()[:32]...), Migration{Version: 33, SQL: recordsKoreanSearchSchema})
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO records_korean_fts(id,title,note) VALUES('existing','도서관에서','연필을 준비한다')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = Migrations()
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var tokenizer, fallback string
		if err := tx.QueryRowContext(t.Context(), "SELECT tokenizer,fallback FROM search_index_config WHERE name='records_korean'").Scan(&tokenizer, &fallback); err != nil {
			return err
		}
		if tokenizer != "trigram" || fallback != "hangul_bigram" {
			t.Fatalf("published search configuration changed: %s %s", tokenizer, fallback)
		}
		var matches int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM records_korean_fts WHERE records_korean_fts MATCH ?", TrigramPhrase("도서관")).Scan(&matches); err != nil {
			return err
		}
		if matches != 1 {
			t.Fatalf("upgrade lost existing search data: %d", matches)
		}
		for _, table := range []string{"result_heads", "result_saves", "plan_drafts", "plan_confirmations", "plan_rejections"} {
			var rows int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&rows); err != nil {
				return err
			}
			if rows != 0 {
				t.Fatalf("migration created plan content in %s", table)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanningUpgradePreservesPublishedWaitingDecisions(t *testing.T) {
	cfg := storeConfig(t)
	// Version 34 was published with waiting decisions before planning landed.
	cfg.Migrations = append(append([]Migration{}, Migrations()[:33]...), Migration{Version: 34, SQL: WaitingDecisionsSchema})
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO waiting_decisions(source,item_id,revision,person,agent,answer,answered_at) VALUES('approval','existing','r1','person','agent','approve','2026-10-22T00:00:00Z')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = Migrations()
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var answer string
		if err := tx.QueryRowContext(t.Context(), "SELECT answer FROM waiting_decisions WHERE item_id='existing'").Scan(&answer); err != nil {
			return err
		}
		if answer != "approve" {
			t.Fatalf("upgrade changed published decision: %q", answer)
		}
		var plans int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM plan_drafts").Scan(&plans); err != nil {
			return err
		}
		if plans != 0 {
			t.Fatalf("migration created plan drafts: %d", plans)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanningUpgradePreservesPublishedPresetEffects(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = append(append([]Migration{}, Migrations()[:34]...), Migration{Version: 35, SQL: presetEffectsSchema})
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO authority_delegations(task_id,person,source,source_task,receiver,limits) VALUES('existing','person','source','source-task','receiver','{}')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = Migrations()
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var receiver string
		if err := tx.QueryRowContext(t.Context(), "SELECT receiver FROM authority_delegations WHERE task_id='existing'").Scan(&receiver); err != nil {
			return err
		}
		if receiver != "receiver" {
			t.Fatalf("upgrade changed published delegation: %q", receiver)
		}
		var plans int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM plan_drafts").Scan(&plans); err != nil {
			return err
		}
		if plans != 0 {
			t.Fatalf("migration created plan drafts: %d", plans)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanningUpgradePreservesPublishedSkillMaintenance(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = append(append([]Migration{}, Migrations()[:35]...), Migration{Version: 36, SQL: skillMaintenanceSchema})
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO skill_maintenance(id,revision,operation_hash,kind) VALUES('existing',1,'hash','prune')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = Migrations()
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var hash string
		if err := tx.QueryRowContext(t.Context(), "SELECT operation_hash FROM skill_maintenance WHERE id='existing' AND revision=1").Scan(&hash); err != nil {
			return err
		}
		if hash != "hash" {
			t.Fatalf("upgrade changed published maintenance receipt: %q", hash)
		}
		var plans int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM plan_drafts").Scan(&plans); err != nil {
			return err
		}
		if plans != 0 {
			t.Fatalf("migration created plan drafts: %d", plans)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanningUpgradePreservesPublishedWorkEvents(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = append(append([]Migration{}, Migrations()[:36]...), Migration{Version: 37, SQL: workEngineSchema})
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO work_events(id,kind,at,body) VALUES('existing','fixture',1,'{"state":"accepted"}')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = Migrations()
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var body string
		if err := tx.QueryRowContext(t.Context(), "SELECT body FROM work_events WHERE id='existing'").Scan(&body); err != nil {
			return err
		}
		if body != `{"state":"accepted"}` {
			t.Fatalf("upgrade changed published work event: %q", body)
		}
		var plans int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM plan_drafts").Scan(&plans); err != nil {
			return err
		}
		if plans != 0 {
			t.Fatalf("migration created plan drafts: %d", plans)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanningUpgradePreservesPublishedTodoProjection(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = append(append([]Migration{}, Migrations()[:37]...), Migration{Version: 38, SQL: recordsTodoSchema})
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO records_index(id,path,blob,revision) VALUES('existing','inbox/task.md','blob','revision')`); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), `INSERT INTO records_todo(id,fields,when_value,deadline,repeat_next) VALUES('existing','{"state":"open"}','2026-10-22','','')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = Migrations()
	db = openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var fields string
		if err := tx.QueryRowContext(t.Context(), "SELECT fields FROM records_todo WHERE id='existing'").Scan(&fields); err != nil {
			return err
		}
		if fields != `{"state":"open"}` {
			t.Fatalf("upgrade changed published todo: %q", fields)
		}
		var plans int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM plan_drafts").Scan(&plans); err != nil {
			return err
		}
		if plans != 0 {
			t.Fatalf("migration created plan drafts: %d", plans)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanningUpgradePreservesPublishedWorkSequenceGuard(t *testing.T) {
	cfg := storeConfig(t)
	cfg.Migrations = append(append([]Migration{}, Migrations()[:38]...), Migration{Version: 39, SQL: workEventSequenceSchema})
	db := openStore(t, cfg)
	if err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO work_events(seq,id,kind,at,body) VALUES(1,'existing','fixture',1,'{}')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = Migrations()
	db = openStore(t, cfg)
	err := db.Write(t.Context(), func(tx *Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT OR REPLACE INTO work_events(seq,id,kind,at,body) VALUES(1,'replacement','fixture',2,'{}')`)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "work_ledger_append_only") {
		t.Fatalf("upgrade lost the published sequence guard: %v", err)
	}
	if err := db.Read(t.Context(), func(tx *Tx) error {
		var id string
		if err := tx.QueryRowContext(t.Context(), "SELECT id FROM work_events WHERE seq=1").Scan(&id); err != nil {
			return err
		}
		if id != "existing" {
			t.Fatalf("upgrade replaced published work event: %q", id)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
