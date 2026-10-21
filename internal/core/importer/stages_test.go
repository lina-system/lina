// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func importStore(t *testing.T) *canon.Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	migrations := canon.Migrations()
	migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: canon.ImportSchema})
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: migrations})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}
func importRequest(t *testing.T) Request {
	t.Helper()
	lcm, ldb := hermesFixture(t, "lcm")
	state, sdb := hermesFixture(t, "state")
	// Two canonical bindings, and a distinct host row whose UID is absent in LCM.
	if _, err := ldb.Exec(`UPDATE host_uid_bindings SET lineage_key='home:synthetic-session'; INSERT INTO host_uid_bindings(store_id,uid,lineage_key,kind,binding_version) VALUES(2,'synthetic-message-2','home:synthetic-session','canonical',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sdb.Exec(`INSERT INTO messages(id,session_id,role,content,timestamp,display_identity) VALUES(7,'synthetic-session','tool','tool output',103,x'00ff')`); err != nil {
		t.Fatal(err)
	}
	return Request{BatchID: "batch", Confirmed: true, Profiles: []Profile{{ID: "profile", AgentID: "agent", PersonID: "person", LCMPath: lcm, StatePath: state, OwnerChannels: []OwnerChannel{{Source: "cli", SessionID: "synthetic-session"}}}}}
}
func TestStagedImportReconcilesAndReverses(t *testing.T) {
	req := importRequest(t)
	db := importStore(t)
	plan, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	report, err := plan.Import(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if report.AddedEvents != 3 || report.AddedReferences != 11 {
		t.Fatalf("report %+v", report)
	}
	verified, err := plan.Verify(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Rows != 11 {
		t.Fatalf("verified %+v", verified)
	}
	for _, key := range plan.Keys() {
		ref, err := Resolve(t.Context(), db, key)
		if err != nil {
			t.Fatal(err)
		}
		refs, err := References(t.Context(), db, ref.RecordID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range refs {
			found = found || r.Key == key
		}
		if !found {
			t.Fatalf("missing reverse reference %+v", key)
		}
		row, err := Reverse(t.Context(), db, key)
		if err != nil {
			t.Fatal(err)
		}
		if RowHash(row) != ref.SourceHash {
			t.Fatalf("hash mismatch %+v", key)
		}
	}
	a, _ := Resolve(t.Context(), db, LegacyKey{System: string(HermesLCM), Instance: "profile", Kind: "messages", Key: "1"})
	b, _ := Resolve(t.Context(), db, LegacyKey{System: string(HermesState), Instance: "profile", Kind: "messages", Key: "1"})
	if a.RecordID != b.RecordID {
		t.Fatal("canonical UID copies did not share event")
	}
	row, err := Reverse(t.Context(), db, LegacyKey{System: string(HermesState), Instance: "profile", Kind: "messages", Key: "7"})
	if err != nil {
		t.Fatal(err)
	}
	if row["display_identity"].Kind != "blob" || row["display_identity"].Data != "AP8=" {
		t.Fatalf("blob lost: %+v", row)
	}
	again, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := again.Import(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if replay.AddedReferences != 0 || replay.AddedEvents != 0 || replay.AddedRecords != 0 {
		t.Fatalf("second run added %+v", replay)
	}
	assertCount(t, db, "memory_item", 0)
	assertCount(t, db, "conv_event", 3)
}
func assertCount(t *testing.T, db *canon.Store, table string, want int) {
	t.Helper()
	err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var got int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&got); err != nil {
			return err
		}
		if got != want {
			t.Errorf("%s count %d want %d", table, got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestStagedImportRefusesBeforeWrites(t *testing.T) {
	req := importRequest(t)
	req.Confirmed = false
	if _, err := Prepare(t.Context(), req); !errors.Is(err, ErrSnapshot) {
		t.Fatalf("unconfirmed maps: %v", err)
	}
	req.Confirmed = true
	db, err := canon.OpenSQLite(t.Context(), req.Profiles[0].LCMPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE summary_nodes SET source_ids='[999]'`); err != nil {
		t.Fatal(err)
	}
	if _, err = Prepare(t.Context(), req); !errors.Is(err, ErrSourceMissing) {
		t.Fatalf("missing summary child: %v", err)
	}
}
func TestConversationTransactionRollsBackAndResumes(t *testing.T) {
	req := importRequest(t)
	db := importStore(t)
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	fail := errors.New("injected failure")
	err = db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := p.Apply(t.Context(), tx, 0); err != nil {
			return err
		}
		return fail
	})
	if !errors.Is(err, fail) {
		t.Fatal(err)
	}
	for _, table := range []string{"conv_event", "legacy_ref", "import_record", "import_batch"} {
		assertCount(t, db, table, 0)
	}
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}
