// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

func importBatch() messenger.ImportBatch {
	return messenger.ImportBatch{ID: "batch-1", System: "murmur", Instance: "home-1", Person: "person"}
}

func importRecord(id, key, hash, text string, at time.Time, route messenger.Route) messenger.ImportRecord {
	return messenger.ImportRecord{
		Message: message(id, route, at, text),
		Kind:    "uid", Key: key, Hash: hash, Ingest: "export", ObservedAt: at.Add(time.Hour),
	}
}

// A source handed over by an importer is created import-only, and a source the
// person already connected keeps its state and adapter.
func TestEnsureImportSourceKeepsAConnectedSource(t *testing.T) {
	f := newFixture(t)
	source := messenger.Source{Person: "person", Platform: "discord", Account: "account-1", Mode: messenger.ModeOfficial, Adapter: "murmur"}
	stored, err := f.store.EnsureImportSource(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != messenger.StateImportOnly || stored.Adapter != "murmur" {
		t.Fatalf("import source = %+v", stored)
	}
	connected := f.connect(t)
	stored, err = f.store.EnsureImportSource(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != connected.State || stored.Adapter != connected.Adapter {
		t.Fatalf("connected source became %+v", stored)
	}
	other := messenger.Source{Person: "someone", Platform: "imessage", Account: "mac", Mode: messenger.ModeOfficial, Adapter: "murmur"}
	if _, err := f.store.EnsureImportSource(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.EnsureImportSource(t.Context(), messenger.Source{Person: "person", Platform: "imessage", Account: "mac", Mode: messenger.ModeOfficial, Adapter: "murmur"}); err == nil {
		t.Fatal("a source already held by another person was accepted")
	}
}

// One source row becomes one record; a second import of the same rows adds
// nothing, and the reference stays readable in both directions.
func TestImportIsRepeatable(t *testing.T) {
	f := newFixture(t)
	source, err := f.store.EnsureImportSource(t.Context(), messenger.Source{Person: "person", Platform: "kakaotalk", Account: "kakao-1", Mode: messenger.ModeUnofficial, Adapter: "murmur"})
	if err != nil {
		t.Fatal(err)
	}
	chat := messenger.ImportChat{Chat: "chat-1", Name: "Chat", Kind: "group", Records: []messenger.ImportRecord{
		importRecord("100", "uid-100", hashOf("a"), "first", f.now, messenger.RoutePerson),
		importRecord("101", "uid-101", hashOf("b"), "second", f.now.Add(time.Minute), messenger.RouteParticipant),
	}}
	first, err := f.store.AdmitImport(t.Context(), source, importBatch(), chat)
	if err != nil {
		t.Fatal(err)
	}
	if first.Added != 2 || !first.Wrote() {
		t.Fatalf("first import = %+v", first)
	}
	second, err := f.store.AdmitImport(t.Context(), source, importBatch(), chat)
	if err != nil {
		t.Fatal(err)
	}
	if second.Added+second.Updated+second.Linked+second.Deleted != 0 {
		t.Fatalf("second import wrote %+v", second)
	}
	keys, err := f.store.ImportedKeys(t.Context(), "murmur", "home-1", source.Platform, source.Account, "chat-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "uid-100" || keys[1] != "uid-101" {
		t.Fatalf("imported keys = %v", keys)
	}
	if n := count(t, f.db, "SELECT count(*) FROM conv_event"); n != 2 {
		t.Fatalf("events = %d, want 2", n)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_legacy_ref WHERE kind='uid' AND instance='home-1'"); n != 2 {
		t.Fatalf("references = %d, want 2", n)
	}
	if n := count(t, f.db, "SELECT count(*) FROM import_batch WHERE id='batch-1'"); n != 1 {
		t.Fatalf("batches = %d, want 1", n)
	}
}

// A source row whose content changed without an edit is a conflict; an edit
// after the stored revision is a new revision that supersedes the earlier one.
func TestImportConflictAndEdit(t *testing.T) {
	f := newFixture(t)
	source, err := f.store.EnsureImportSource(t.Context(), messenger.Source{Person: "person", Platform: "discord", Account: "account-1", Mode: messenger.ModeOfficial, Adapter: "murmur"})
	if err != nil {
		t.Fatal(err)
	}
	chat := messenger.ImportChat{Chat: "general", Kind: "group", Records: []messenger.ImportRecord{
		importRecord("m-1", "uid-m-1", hashOf("a"), "first", f.now, messenger.RouteParticipant),
	}}
	if _, err := f.store.AdmitImport(t.Context(), source, importBatch(), chat); err != nil {
		t.Fatal(err)
	}
	changed := messenger.ImportChat{Chat: "general", Kind: "group", Records: []messenger.ImportRecord{
		importRecord("m-1", "uid-m-1", hashOf("b"), "rewritten", f.now, messenger.RouteParticipant),
	}}
	result, err := f.store.AdmitImport(t.Context(), source, importBatch(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 1 || result.Updated != 0 {
		t.Fatalf("changed row = %+v", result)
	}
	if n := count(t, f.db, "SELECT count(*) FROM conv_event WHERE body='rewritten'"); n != 0 {
		t.Fatalf("conflict overwrote the record: %d", n)
	}
	edited := importRecord("m-1", "uid-m-1", hashOf("c"), "corrected", f.now, messenger.RouteParticipant)
	edited.Message.Edited = f.now.Add(time.Hour)
	revision, err := f.store.AdmitImport(t.Context(), source, importBatch(), messenger.ImportChat{Chat: "general", Kind: "group", Records: []messenger.ImportRecord{edited}})
	if err != nil {
		t.Fatal(err)
	}
	if revision.Updated != 1 {
		t.Fatalf("edit = %+v", revision)
	}
	if n := count(t, f.db, "SELECT revision FROM messenger_record WHERE platform='discord' AND chat='general' AND message='m-1'"); n != 2 {
		t.Fatalf("revision = %d, want 2", n)
	}
	if n := count(t, f.db, "SELECT count(*) FROM ev_rel WHERE kind='supersedes'"); n != 1 {
		t.Fatalf("supersedes relations = %d, want 1", n)
	}
}

// The archive value of one key wins over a live value, whatever order the run
// meets them in, and a repeated row keeps one reference.
func TestImportArchiveWinsOverLive(t *testing.T) {
	f := newFixture(t)
	source, err := f.store.EnsureImportSource(t.Context(), messenger.Source{Person: "person", Platform: "kakaotalk", Account: "kakao-1", Mode: messenger.ModeUnofficial, Adapter: "murmur"})
	if err != nil {
		t.Fatal(err)
	}
	live := importRecord("100", "uid-100", hashOf("live"), "live text", f.now, messenger.RoutePerson)
	archive := importRecord("100", "uid-100", hashOf("archive"), "archive text", f.now, messenger.RoutePerson)
	archive.Ingest = "archive"
	chat := messenger.ImportChat{Chat: "chat-1", Kind: "group", Records: []messenger.ImportRecord{live, archive}}
	if _, err := f.store.AdmitImport(t.Context(), source, importBatch(), chat); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM conv_event WHERE body='archive text'"); n != 1 {
		t.Fatalf("archive value = %d", n)
	}
	if n := count(t, f.db, "SELECT count(*) FROM conv_event"); n != 1 {
		t.Fatalf("events = %d, want one record per key", n)
	}
	// The live value arriving later does not displace the archive one.
	again := messenger.ImportChat{Chat: "chat-1", Kind: "group", Records: []messenger.ImportRecord{live}}
	result, err := f.store.AdmitImport(t.Context(), source, importBatch(), again)
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 0 {
		t.Fatalf("a live row replaced the archive value: %+v", result)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_legacy_ref WHERE key='uid-100'"); n != 1 {
		t.Fatalf("references = %d, want 1", n)
	}
}

// A source-reported deletion goes through the retraction path and keeps its
// reference, so a repeat import does not bring the content back.
func TestImportDeletionIsATombstone(t *testing.T) {
	f := newFixture(t)
	source, err := f.store.EnsureImportSource(t.Context(), messenger.Source{Person: "person", Platform: "imessage", Account: "mac", Mode: messenger.ModeOfficial, Adapter: "murmur"})
	if err != nil {
		t.Fatal(err)
	}
	record := importRecord("i-1", "uid-i-1", hashOf("a"), "removed", f.now, messenger.RouteParticipant)
	chat := messenger.ImportChat{Chat: "chat-guid-1", Kind: "group", Records: []messenger.ImportRecord{record}}
	if _, err := f.store.AdmitImport(t.Context(), source, importBatch(), chat); err != nil {
		t.Fatal(err)
	}
	tombstone := messenger.ImportChat{Chat: "chat-guid-1", Kind: "group", Deletions: []messenger.ImportDeletion{{
		Message: "i-1", At: f.now.Add(time.Hour), Kind: "uid", Key: "uid-i-1", Hash: hashOf("a"), Ingest: "export", ObservedAt: f.now.Add(time.Hour),
	}}}
	deleted, err := f.store.AdmitImport(t.Context(), source, importBatch(), tombstone)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Deleted != 1 {
		t.Fatalf("deletion = %+v", deleted)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE state='deleted'"); n != 1 {
		t.Fatalf("tombstones = %d, want 1", n)
	}
	if n := count(t, f.db, "SELECT count(*) FROM retract_target WHERE kind='event' AND state<>'active'"); n != 1 {
		t.Fatalf("hidden events = %d, want 1", n)
	}
	// The row comes back through a later import and stays gone.
	again, err := f.store.AdmitImport(t.Context(), source, importBatch(), chat)
	if err != nil {
		t.Fatal(err)
	}
	if again.Added != 0 {
		t.Fatalf("the deleted row returned: %+v", again)
	}
	if n := count(t, f.db, "SELECT count(*) FROM conv_event"); n != 1 {
		t.Fatalf("events = %d, want 1", n)
	}
}

// A reply that comes before its target in the run still keeps its relation: the
// target is admitted first, and a second run adds nothing.
func TestImportKeepsRelationsToLaterTargets(t *testing.T) {
	f := newFixture(t)
	source, err := f.store.EnsureImportSource(t.Context(), messenger.Source{Person: "person", Platform: "discord", Account: "account-1", Mode: messenger.ModeOfficial, Adapter: "murmur"})
	if err != nil {
		t.Fatal(err)
	}
	reply := importRecord("100", "uid-100", hashOf("a"), "reply", f.now, messenger.RouteParticipant)
	reply.Message.ReplyTo = "99"
	reply.Message.Thread = "98"
	thread := importRecord("98", "uid-98", hashOf("b"), "thread start", f.now, messenger.RouteParticipant)
	target := importRecord("99", "uid-99", hashOf("c"), "target", f.now, messenger.RoutePerson)
	target.Message.Thread = "98"
	chat := messenger.ImportChat{Chat: "general", Kind: "group", Records: []messenger.ImportRecord{reply, target, thread}}
	result, err := f.store.AdmitImport(t.Context(), source, importBatch(), chat)
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 3 {
		t.Fatalf("import = %+v", result)
	}
	relation := `SELECT count(*) FROM ev_rel r
JOIN messenger_record a ON a.event=r.event_id JOIN messenger_record b ON b.event=r.target_id
WHERE r.kind=? AND a.message=? AND b.message=?`
	for _, want := range []struct{ kind, from, to string }{{"replies_to", "100", "99"}, {"in_thread", "100", "98"}, {"in_thread", "99", "98"}} {
		if n := count(t, f.db, relation, want.kind, want.from, want.to); n != 1 {
			t.Fatalf("%s %s -> %s = %d, want 1", want.kind, want.from, want.to, n)
		}
	}
	again, err := f.store.AdmitImport(t.Context(), source, importBatch(), chat)
	if err != nil {
		t.Fatal(err)
	}
	if again.Wrote() {
		t.Fatalf("second import wrote %+v", again)
	}
}

// A reply imported with an edit of its stored target links only the revision
// this run admits.
func TestImportLinksOneRevisionOfEditedTarget(t *testing.T) {
	f := newFixture(t)
	source, err := f.store.EnsureImportSource(t.Context(), messenger.Source{Person: "person", Platform: "discord", Account: "account-1", Mode: messenger.ModeOfficial, Adapter: "murmur"})
	if err != nil {
		t.Fatal(err)
	}
	target := importRecord("99", "uid-99", hashOf("a"), "target", f.now, messenger.RouteParticipant)
	if _, err := f.store.AdmitImport(t.Context(), source, importBatch(), messenger.ImportChat{Chat: "general", Kind: "group", Records: []messenger.ImportRecord{target}}); err != nil {
		t.Fatal(err)
	}
	edited := importRecord("99", "uid-99", hashOf("b"), "target, edited", f.now, messenger.RouteParticipant)
	edited.Message.Edited = f.now.Add(time.Hour)
	reply := importRecord("100", "uid-100", hashOf("c"), "reply", f.now.Add(time.Minute), messenger.RouteParticipant)
	reply.Message.ReplyTo = "99"
	reply.Message.Thread = "99"
	result, err := f.store.AdmitImport(t.Context(), source, importBatch(), messenger.ImportChat{Chat: "general", Kind: "group", Records: []messenger.ImportRecord{reply, edited}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 1 || result.Updated != 1 {
		t.Fatalf("import = %+v", result)
	}
	relation := `SELECT count(*) FROM ev_rel r JOIN messenger_record a ON a.event=r.event_id
WHERE r.kind=? AND a.message='100' AND r.target_id=(SELECT event FROM messenger_record WHERE message='99')`
	for _, kind := range []string{"replies_to", "in_thread"} {
		if n := count(t, f.db, relation, kind); n != 1 {
			t.Fatalf("%s to the current revision = %d, want 1", kind, n)
		}
	}
	if n := count(t, f.db, "SELECT count(*) FROM ev_rel r JOIN messenger_record a ON a.event=r.event_id WHERE a.message='100'"); n != 2 {
		t.Fatalf("reply relations = %d, want 2", n)
	}
}

// Rows that reply to each other, or to themselves, are admitted without a
// relation to an event that does not exist yet.
func TestImportBreaksReplyCycles(t *testing.T) {
	f := newFixture(t)
	source, err := f.store.EnsureImportSource(t.Context(), messenger.Source{Person: "person", Platform: "discord", Account: "account-1", Mode: messenger.ModeOfficial, Adapter: "murmur"})
	if err != nil {
		t.Fatal(err)
	}
	first := importRecord("1", "uid-1", hashOf("a"), "one", f.now, messenger.RouteParticipant)
	first.Message.ReplyTo = "2"
	second := importRecord("2", "uid-2", hashOf("b"), "two", f.now, messenger.RouteParticipant)
	second.Message.ReplyTo = "1"
	self := importRecord("3", "uid-3", hashOf("c"), "three", f.now, messenger.RouteParticipant)
	self.Message.ReplyTo = "3"
	chat := messenger.ImportChat{Chat: "general", Kind: "group", Records: []messenger.ImportRecord{first, second, self}}
	result, err := f.store.AdmitImport(t.Context(), source, importBatch(), chat)
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 3 {
		t.Fatalf("import = %+v", result)
	}
	if n := count(t, f.db, "SELECT count(*) FROM ev_rel WHERE kind='replies_to'"); n != 1 {
		t.Fatalf("replies_to relations = %d, want 1", n)
	}
}

func hashOf(text string) string {
	const hexchars = "0123456789abcdef"
	hash := make([]byte, 64)
	for i := range hash {
		hash[i] = hexchars[int(text[0]+byte(i))%len(hexchars)]
	}
	return string(hash)
}
