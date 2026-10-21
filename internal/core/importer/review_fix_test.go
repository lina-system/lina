// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestReviewIdenticalUIDReplayKeepsCanonicalHost(t *testing.T) {
	req := importRequest(t)
	sqlSource(t, req.Profiles[0].LCMPath, `INSERT INTO messages(store_id,session_id,source,conversation_id,role,content,timestamp) VALUES(9,'synthetic-session','cli','synthetic-conversation','user','Synthetic request',100); INSERT INTO host_uid_bindings(store_id,uid,lineage_key,kind,binding_version) VALUES(9,'synthetic-message-1','home:synthetic-session','version',1)`)
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	db := importStore(t)
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	canonical, _ := Resolve(t.Context(), db, LegacyKey{string(HermesLCM), "profile", "messages", "1"})
	host, _ := Resolve(t.Context(), db, LegacyKey{string(HermesState), "profile", "messages", "1"})
	replay, _ := Resolve(t.Context(), db, LegacyKey{string(HermesLCM), "profile", "messages", "9"})
	if canonical.RecordID != host.RecordID || replay.RecordID == host.RecordID {
		t.Fatal("lost canonical association or replay identity")
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}
func TestReviewUnsealedEventsNeverReachLiveLedger(t *testing.T) {
	req := importRequest(t)
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	db := importStore(t)
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"conv_event", "retract_visible_event", "ledger_search_text", "ledger_search_pending", "memory_item"} {
		assertCount(t, db, table, 0)
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}
func TestReviewSidecarHasReadableNativeMaterial(t *testing.T) {
	req := importRequest(t)
	db := importStore(t)
	root := filepath.Join(t.TempDir(), "materials")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	materials, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer materials.Close()
	req.Materials, err = assets.OpenMaterialWriter(t.Context(), materials)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("synthetic tool output")
	path := filepath.Join(t.TempDir(), "output")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	req.Sidecars = []Sidecar{{Key: LegacyKey{string(HermesLCM), "profile", "sidecars", "output"}, Event: LegacyKey{string(HermesLCM), "profile", "messages", "2"}, SnapshotPath: path, SHA256: bytesHash(data)}}
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	ref, err := Resolve(t.Context(), db, req.Sidecars[0].Event)
	if err != nil {
		t.Fatal(err)
	}
	var materialID string
	var revision int64
	err = db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT material_id,material_revision FROM import_event WHERE id=?", ref.RecordID).Scan(&materialID, &revision)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := materials.Read(t.Context(), materialID, revision)
	if err != nil || string(got) != string(data) {
		t.Fatalf("native material=%q err=%v", got, err)
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}

func importMaterials(t *testing.T, db *canon.Store) (*assets.MaterialWriter, *assets.Store) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "materials")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	writer, err := assets.OpenMaterialWriter(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	return writer, store
}

func TestReviewMaterialAdoptionRollsBackAndReplays(t *testing.T) {
	req := importRequest(t)
	db := importStore(t)
	writer, store := importMaterials(t, db)
	req.Materials = writer
	for _, id := range []string{"1", "2"} {
		data := []byte("output " + id)
		path := filepath.Join(t.TempDir(), "sidecar")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		req.Sidecars = append(req.Sidecars, Sidecar{Key: LegacyKey{string(HermesLCM), "profile", "sidecars", id}, Event: LegacyKey{string(HermesLCM), "profile", "messages", id}, SnapshotPath: path, SHA256: bytesHash(data)})
	}
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected rollback after material insert")
	err = db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := p.Apply(t.Context(), tx, 0); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"assets", "asset_revisions", "import_event", "legacy_ref"} {
		assertCount(t, db, table, 0)
	}
	for attempt := range 2 {
		p, err = Prepare(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		report, err := p.Import(t.Context(), db)
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 1 && report.AddedReferences != 0 {
			t.Fatal("replay added references")
		}
		assertCount(t, db, "assets", 2)
		for _, s := range req.Sidecars {
			ref, err := Resolve(t.Context(), db, s.Event)
			if err != nil {
				t.Fatal(err)
			}
			var id string
			var revision int64
			if err = db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT material_id,material_revision FROM import_event WHERE id=?", ref.RecordID).Scan(&id, &revision)
			}); err != nil {
				t.Fatal(err)
			}
			body, err := store.Read(t.Context(), id, revision)
			if err != nil || string(body) != "output "+s.Key.Key {
				t.Fatalf("body=%q err=%v", body, err)
			}
		}
		if _, err = p.Verify(t.Context(), db); err != nil {
			t.Fatal(err)
		}
	}
}
