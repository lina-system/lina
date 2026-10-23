// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
)

func sqlSource(t *testing.T, path, query string) {
	t.Helper()
	db, err := canon.OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(query); err != nil {
		t.Fatal(err)
	}
}
func TestImportNamespacesAndSourceImmutability(t *testing.T) {
	t.Parallel()
	req := importRequest(t)
	p2 := req.Profiles[0]
	p2.ID = "other-profile"
	p2.AgentID = "other-agent"
	req.Profiles = append(req.Profiles, p2)
	paths := []string{p2.StatePath, p2.StatePath + "-wal", p2.LCMPath, p2.LCMPath + "-wal"}
	before := map[string]string{}
	for _, p := range paths {
		b, _ := os.ReadFile(p)
		before[p] = string(b)
	}
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	db := importStore(t)
	report, err := p.Import(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if report.AddedEvents != 6 {
		t.Fatalf("events %d", report.AddedEvents)
	}
	for _, path := range paths {
		b, _ := os.ReadFile(path)
		if string(b) != before[path] {
			t.Fatalf("source changed: %s", filepath.Base(path))
		}
	}
	for _, system := range []string{string(HermesLCM), string(HermesState)} {
		a, _ := Resolve(t.Context(), db, LegacyKey{system, "profile", "messages", "1"})
		b, _ := Resolve(t.Context(), db, LegacyKey{system, "other-profile", "messages", "1"})
		if a.RecordID == b.RecordID {
			t.Fatal("profile namespaces collapsed")
		}
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}
func TestImportRejectsChangedSourceAndDestination(t *testing.T) {
	t.Parallel()
	req := importRequest(t)
	db := importStore(t)
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	req.Profiles[0].AgentID = "changed"
	changed, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = changed.Import(t.Context(), db); !errors.Is(err, ErrConflict) {
		t.Fatalf("destination changed: %v", err)
	}
	req.Profiles[0].AgentID = "agent"
	sqlSource(t, req.Profiles[0].StatePath, `UPDATE messages SET reasoning='changed source' WHERE id=2`)
	changed, err = Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = changed.Import(t.Context(), db); !errors.Is(err, ErrConflict) {
		t.Fatalf("source changed: %v", err)
	}
	assertCount(t, db, "import_event", 3)
}
func TestReverseUsesAdoptedRecordAndRejectsDamage(t *testing.T) {
	t.Parallel()
	req := importRequest(t)
	db := importStore(t)
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	// Reverse works after every source handle and snapshot is gone.
	for _, profile := range req.Profiles {
		if err = os.Remove(profile.StatePath); err != nil {
			t.Fatal(err)
		}
		if err = os.Remove(profile.LCMPath); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE import_record SET body='corrupt' WHERE kind='summary'")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Verify(t.Context(), db); !errors.Is(err, ErrConflict) {
		t.Fatalf("reverse accepted changed adopted body: %v", err)
	}
}
func TestImportUIDVersionsEngineAndForks(t *testing.T) {
	t.Parallel()
	req := importRequest(t)
	sqlSource(t, req.Profiles[0].LCMPath, `INSERT INTO messages(store_id,session_id,source,conversation_id,role,content,timestamp) VALUES(9,'synthetic-session','cli','synthetic-conversation','assistant','revised',105);
 INSERT INTO host_uid_bindings(store_id,uid,lineage_key,kind,binding_version) VALUES(9,'synthetic-message-2','home:synthetic-session','version',1),(0,'engine-generated','home:synthetic-session','engine',1)`)
	sqlSource(t, req.Profiles[0].StatePath, `UPDATE messages SET content='revised' WHERE id=2;
 UPDATE sessions SET end_reason='compression' WHERE id='synthetic-session';
 INSERT INTO sessions(id,source,started_at,parent_session_id,model_config) VALUES('fork','cli',110,'synthetic-session','{"_branched_from":"synthetic-session"}');
 INSERT INTO messages(id,session_id,role,content,timestamp,message_uid) VALUES(11,'fork','user','fork own copy',110,'synthetic-message-1')`)
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	db := importStore(t)
	report, err := p.Import(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if report.AddedEvents != 5 {
		t.Fatalf("events %d want 5", report.AddedEvents)
	}
	lcm, _ := Resolve(t.Context(), db, LegacyKey{string(HermesLCM), "profile", "messages", "9"})
	host, _ := Resolve(t.Context(), db, LegacyKey{string(HermesState), "profile", "messages", "2"})
	if lcm.RecordID != host.RecordID {
		t.Fatal("host version not mapped")
	}
	orig, _ := Resolve(t.Context(), db, LegacyKey{string(HermesState), "profile", "messages", "1"})
	fork, _ := Resolve(t.Context(), db, LegacyKey{string(HermesState), "profile", "messages", "11"})
	if fork.RecordID == orig.RecordID {
		t.Fatal("explicit fork collapsed into compression parent")
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}
func typedRow(fields map[string]any) Row {
	r := Row{}
	for k, v := range fields {
		r[k], _ = value(v)
	}
	return r
}
func TestHonchoMatchingRetainsSpeakerEvidenceAndHistory(t *testing.T) {
	t.Parallel()
	req := importRequest(t)
	sqlSource(t, req.Profiles[0].StatePath, `INSERT INTO sessions(id,source,started_at) VALUES('scheduled','cron',120); INSERT INTO messages(id,session_id,role,content,timestamp) VALUES(20,'scheduled','user','automated prompt',120)`)
	w := honchoWorkspace{profile: "profile", workspace: "workspace"}
	for _, f := range []map[string]any{
		{"id": "1", "session_id": "synthetic-session", "role": "user", "content": "Synthetic request", "timestamp": float64(100), "owner_claim": "true"},
		{"id": "2", "session_id": "scheduled", "role": "user", "content": "automated prompt", "timestamp": float64(120), "owner_claim": "true"},
		{"id": "3", "session_id": "orphan", "role": "user", "content": "unproven", "timestamp": float64(130), "owner_claim": "true"},
	} {
		w.messages = append(w.messages, typedRow(f))
	}
	for _, kind := range []string{"workspace", "peer", "session", "session_summary", "conclusion", "card", "review_log"} {
		w.history = append(w.history, typedRow(map[string]any{"id": kind + "-1", "kind": kind, "session_id": "synthetic-session", "content": "preserved " + kind}))
	}
	p, err := prepare(t.Context(), req, []honchoWorkspace{w})
	if err != nil {
		t.Fatal(err)
	}
	db := importStore(t)
	report, err := p.Import(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if report.AddedEvents != 5 {
		t.Fatalf("events=%d", report.AddedEvents)
	}
	relabelled, unproven := 0, 0
	for _, r := range report.Speakers {
		relabelled += r.Relabelled
		unproven += r.Unproven
	}
	if relabelled != 2 || unproven != 1 {
		t.Fatalf("attribution=%d,%d", relabelled, unproven)
	}
	for key, want := range map[string]guard.SpeakerKind{"1": guard.Person, "2": guard.Automation, "3": guard.Forwarded} {
		ref, err := Resolve(t.Context(), db, LegacyKey{"honcho", "workspace", "messages", key})
		if err != nil {
			t.Fatal(err)
		}
		err = db.Read(t.Context(), func(tx *canon.Tx) error {
			var kind string
			err := tx.QueryRowContext(t.Context(), "SELECT speaker_kind FROM import_event WHERE id=?", ref.RecordID).Scan(&kind)
			if kind != string(want) {
				t.Errorf("kind %s want %s", kind, want)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, "memory_item", 0)
	assertCount(t, db, "sum_node", 0)
}
func TestImportSidecarsAndSensitiveBodies(t *testing.T) {
	t.Parallel()
	req := importRequest(t)
	db := importStore(t)
	req.Materials, _ = importMaterials(t, db)
	secret := "password=synthetic-import-secret"
	sqlSource(t, req.Profiles[0].LCMPath, `UPDATE messages SET content='password=synthetic-import-secret' WHERE store_id=1`)
	data := []byte(secret)
	path := filepath.Join(t.TempDir(), "sidecar")
	if err := os.WriteFile(path, data, 0600); err != nil {
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
	assertCount(t, db, "import_event_span", 1)
	assertCount(t, db, "ledger_search_text", 0)
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Prepare(t.Context(), req); !errors.Is(err, ErrSnapshot) {
		t.Fatalf("sidecar hash: %v", err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	p, err = Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Import(context.Background(), importStore(t)); err != nil {
		t.Fatal(err)
	}
}

func bytesHash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func TestImportCrossConversationRelations(t *testing.T) {
	t.Parallel()
	req := importRequest(t)
	sqlSource(t, req.Profiles[0].LCMPath, `INSERT INTO messages(store_id,session_id,source,conversation_id,role,content,timestamp) VALUES(20,'elsewhere','cron','another-conversation','user','background',120); INSERT INTO message_relations(relation_id,store_id,kind,related_store_id) VALUES(5,20,'supersedes',1)`)
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	db := importStore(t)
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}
func TestCanonicalRowStorageClasses(t *testing.T) {
	t.Parallel()
	r := typedRow(map[string]any{"null": nil, "empty": "", "blob": []byte{0, 255}, "integer": int64(9223372036854775807), "real": float64(1.25), "text": "한글\x00bytes"})
	if err := checkRow(r); err != nil {
		t.Fatal(err)
	}
	for key, v := range r {
		changed := r.clone()
		delete(changed, key)
		if RowHash(changed) == RowHash(r) {
			t.Fatalf("column %s was not hashed", key)
		}
		if key == "empty" {
			v.Kind = "blob"
			changed = r.clone()
			changed[key] = v
			if RowHash(changed) == RowHash(r) {
				t.Fatal("text/blob collapsed")
			}
		}
	}
}
func TestHonchoMatchesCompressedSessionCopies(t *testing.T) {
	t.Parallel()
	req := importRequest(t)
	sqlSource(t, req.Profiles[0].StatePath, `UPDATE sessions SET end_reason='compression' WHERE id='synthetic-session'; INSERT INTO sessions(id,source,started_at,parent_session_id) VALUES('resumed','cli',105,'synthetic-session'); INSERT INTO messages(id,session_id,role,content,timestamp,message_uid) VALUES(10,'resumed','user','Synthetic request',100,'synthetic-message-1')`)
	req.Profiles[0].OwnerChannels = append(req.Profiles[0].OwnerChannels, OwnerChannel{SessionID: "resumed", Source: "cli"})
	w := honchoWorkspace{profile: "profile", workspace: "workspace", messages: []Row{typedRow(map[string]any{"id": "resumed-message", "session_id": "resumed", "role": "user", "content": "Synthetic request", "timestamp": float64(100), "owner_claim": "true"})}}
	p, err := prepare(t.Context(), req, []honchoWorkspace{w})
	if err != nil {
		t.Fatal(err)
	}
	db := importStore(t)
	r, err := p.Import(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if r.AddedEvents != 3 {
		t.Fatalf("matching host session copy added event: %d", r.AddedEvents)
	}
}

func TestHistoryLinksAndRetractionsStayOutsideMemory(t *testing.T) {
	t.Parallel()
	req := importRequest(t)
	w := honchoWorkspace{profile: "profile", workspace: "workspace", messages: []Row{typedRow(map[string]any{"id": "m", "session_id": "synthetic-session", "role": "user", "content": "Synthetic request", "timestamp": float64(100)})}, history: []Row{
		typedRow(map[string]any{"kind": "review_log", "id": "deleted", "conclusion_id": "old", "deleted": "true", "content": "logged old text"}),
		typedRow(map[string]any{"kind": "conclusion", "id": "new", "session_id": "synthetic-session", "content": "current statement", "source_ids": "[\"m\"]", "supersedes": "[\"old\"]"}),
	}}
	p, err := prepare(t.Context(), req, []honchoWorkspace{w})
	if err != nil {
		t.Fatal(err)
	}
	db := importStore(t)
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	ref, err := Resolve(t.Context(), db, LegacyKey{"honcho", "workspace", "conclusion", "new"})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Read(t.Context(), func(tx *canon.Tx) error {
		var n int
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM import_link WHERE record_id=?", ref.RecordID).Scan(&n); err != nil {
			return err
		}
		if n != 2 {
			t.Fatalf("history links=%d", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, "memory_item", 0)
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	w.history[1]["source_ids"], _ = value(`["missing"]`)
	if _, err = prepare(t.Context(), req, []honchoWorkspace{w}); !errors.Is(err, ErrSourceMissing) {
		t.Fatalf("unresolved history evidence: %v", err)
	}
}

func TestDuplicateSourceRelationsKeepBothReferences(t *testing.T) {
	t.Parallel()
	req := importRequest(t)
	sqlSource(t, req.Profiles[0].LCMPath, `INSERT INTO message_relations(relation_id,store_id,kind,related_store_id) VALUES(2,2,'view_of',1)`)
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	db := importStore(t)
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, "import_event_relation", 1)
}
func TestPerConversationResumeAndJobValidation(t *testing.T) {
	t.Parallel()
	req := importRequest(t)
	second := req.Profiles[0]
	second.ID = "second"
	req.Profiles = append(req.Profiles, second)
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p.Conversations() != 2 {
		t.Fatal(p.Conversations())
	}
	db := importStore(t)
	tasks := p.Tasks()
	if err = db.Write(t.Context(), func(tx *canon.Tx) error { return p.ApplyJob(t.Context(), tx, tasks[0], tasks[0].Input) }); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("interrupted conversation")
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		if err := p.ApplyJob(t.Context(), tx, tasks[1], tasks[1].Input); err != nil {
			return err
		}
		return failure
	}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	assertCount(t, db, "import_event", 3)
	r, err := p.Import(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if r.AddedEvents != 3 || r.AddedReferences != 11 {
		t.Fatalf("resume added %+v", r)
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	err = db.Write(t.Context(), func(tx *canon.Tx) error {
		return p.ApplyJob(t.Context(), tx, tasks[0], []byte(`{"Conversation":"wrong"}`))
	})
	if err == nil {
		t.Fatal("accepted changed worker echo")
	}
	assertCount(t, db, "import_event", 6)
}
