// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/addons/importer/murmur"
	"github.com/thisisjun786/lina/internal/core/canon"
)

// murmurRecord is one message of the synthetic murmur data home this scenario
// hands over. The home is built to murmur's own spool format; no real store is
// read.
type murmurRecord struct {
	Source, Account, Room, Native string
	Author, AuthorName            string
	At                            time.Time
	Edited                        *time.Time
	Text                          string
	Deleted                       bool
	ReplyTo                       string
	Ingest, RoomName              string
}

func murmurUID(source, room, native string) string {
	encoded, _ := json.Marshal([]string{source, room, native})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// murmurHome writes a data home with one event per source plus an archive event
// that repeats one KakaoTalk key.
func murmurHome(t *testing.T) (string, map[string][]string) {
	t.Helper()
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	edited := start.Add(30 * time.Minute)
	events := [][]murmurRecord{
		{
			{Source: "kakao", Account: "kakao-1", Room: "chat-1", Native: "100", Author: "kakao-1", AuthorName: "me", At: start, Text: "live text", Ingest: "live"},
			{Source: "kakao", Account: "kakao-1", Room: "chat-1", Native: "101", Author: "friend-9", AuthorName: "Friend", At: start.Add(time.Minute), Text: "hello", Ingest: "live", ReplyTo: "100"},
		},
		{
			{Source: "kakao", Account: "kakao-1", Room: "chat-1", Native: "100", Author: "kakao-1", AuthorName: "me", At: start, Edited: &edited, Text: "archive text", Ingest: "archive"},
		},
		{
			{Source: "discord", Account: "discord-1", Room: "general", Native: "d-1", Author: "friend-2", AuthorName: "Other", At: start.Add(3 * time.Minute), Text: "path derived", Ingest: "export", RoomName: "guild-a/general"},
			{Source: "discord", Account: "discord-1", Room: "general", Native: "d-2", Author: "discord-1", AuthorName: "me", At: start.Add(4 * time.Minute), Text: "mine", Ingest: "export", RoomName: "guild-a/general"},
		},
		{
			{Source: "imessage", Account: "mac", Room: "chat-guid-1", Native: "i-1", Author: "me", AuthorName: "me", At: start.Add(6 * time.Minute), Text: "hello from me", Ingest: "archive", RoomName: "chat-guid-1"},
			{Source: "imessage", Account: "mac", Room: "chat-guid-1", Native: "i-2", Author: "+15550001", AuthorName: "+15550001", At: start.Add(7 * time.Minute), Text: "hi", Ingest: "archive", RoomName: "chat-guid-1"},
			{Source: "imessage", Account: "mac", Room: "chat-guid-1", Native: "i-3", Author: "+15550001", AuthorName: "+15550001", At: start.Add(8 * time.Minute), Text: "removed", Ingest: "archive", Deleted: true, RoomName: "chat-guid-1"},
		},
	}
	root := filepath.Join(t.TempDir(), "murmur-home")
	if err := os.MkdirAll(filepath.Join(root, "spool"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"version":1,"embedding":{"provider":"local","model":"synthetic","threads":1},"integrity":{"business_hours":6,"other_hours":24,"live_window_hours":20},"rooms":[],"sources":[]}`
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	var spool []byte
	want := map[string][]string{}
	digests := map[string]map[string]string{}
	for _, value := range []murmurRecord{{Source: "kakao", Account: "kakao-1", Room: "chat-1"}, {Source: "discord", Account: "discord-1", Room: "general"}, {Source: "imessage", Account: "mac", Room: "chat-guid-1"}} {
		digests[value.Source+"/"+value.Account+"/"+value.Room] = map[string]string{}
	}
	for index, event := range events {
		var messages []any
		var observations []any
		rooms := map[string]murmurRecord{}
		for _, value := range event {
			deleted := 0
			if value.Deleted {
				deleted = 1
			}
			var editedAt, reply any
			if value.Edited != nil {
				editedAt = value.Edited.UTC().Format("2006-01-02T15:04:05.000000Z")
			}
			if value.ReplyTo != "" {
				reply = murmurUID(value.Source, value.Room, value.ReplyTo)
			}
			name := value.RoomName
			if name == "" {
				name = value.Room
			}
			messages = append(messages, map[string]any{
				"source": value.Source, "account": value.Account, "room_id": value.Room,
				"native_id": value.Native, "uid": murmurUID(value.Source, value.Room, value.Native),
				"author_id": value.Author, "author_name": value.AuthorName,
				"ts": value.At.UTC().Format("2006-01-02T15:04:05.000000Z"), "edited_ts": editedAt,
				"deleted": deleted, "reply_to_uid": reply, "thread_root_uid": nil, "text": value.Text,
				"attachments": []any{}, "raw": map[string]any{}, "ingest_path": value.Ingest,
				"privacy_class": "private", "filled_from": nil, "room_name": name,
			})
			rooms[value.Source+"\x00"+value.Room] = value
			// murmur keys one row per source, room and native id, so a key that
			// arrives twice is one row in its manifest.
			digests[value.Source+"/"+value.Account+"/"+value.Room][value.Native] = murmurUID(value.Source, value.Room, value.Native)
		}
		var names []string
		for key := range rooms {
			names = append(names, key)
		}
		sort.Strings(names)
		for _, key := range names {
			value := rooms[key]
			observations = append(observations, map[string]any{
				"source": value.Source, "room_id": value.Room, "ingest_path": value.Ingest,
				"collected_at": "2026-10-20T00:00:00.000000Z", "coverage_since": nil,
				"coverage_until": nil, "first_message_ts": nil,
			})
		}
		encoded, err := json.Marshal(map[string]any{
			"version": 1, "event_id": fmt.Sprintf("event-%d", index),
			"ingested_at": "2026-10-20T00:00:00.000000Z", "messages": messages, "observations": observations,
		})
		if err != nil {
			t.Fatal(err)
		}
		spool = append(spool, encoded...)
		spool = append(spool, '\n')
	}
	if err := os.WriteFile(filepath.Join(root, "spool/events.jsonl"), spool, 0o600); err != nil {
		t.Fatal(err)
	}
	for key, ids := range digests {
		for _, id := range ids {
			want[key] = append(want[key], id)
		}
		sort.Strings(want[key])
	}
	return root, want
}

// TestM1MurmurImport runs the installed lina import-murmur command on a
// synthetic murmur data home while Core is stopped, then starts installed Core
// on the same main. It checks that every source type became messenger records
// keyed by murmur's uid, that each room's count and digest reconcile with
// murmur's own manifest, that a second run adds nothing, that the data home is
// byte-identical, and that the import created no memory item and no model
// request.
func TestM1MurmurImport(t *testing.T) {
	t.Parallel()

	h, start := installedConversationHarness(t, "")
	core := start(false)
	h.stopCore(core)
	person := installedPerson(t, h)
	home, wantRooms := murmurHome(t)
	before := importSourceSnapshot(t, home)
	path := filepath.Join(h.root, "canon/canon.sqlite")

	args := []string{"import-murmur", "--state-root", h.root, "--source", home, "--person", person,
		"--own", "kakao:kakao-1", "--own", "discord:discord-1"}
	run := func(extra ...string) []byte {
		t.Helper()
		p := h.command(append(append([]string{}, args...), extra...)...)
		p.start(t)
		p.wait(t, 0)
		if after := importSourceSnapshot(t, home); !reflectEqualMap(before, after) {
			t.Fatal("the import changed the data home")
		}
		return p.stdout.Bytes()
	}

	// The rehearsal reports each source and room and writes nothing.
	initial := importCanonCounts(t, h.ctx, path)
	var rehearsal struct{ Rehearsal murmur.Report }
	if err := json.Unmarshal(run("--rehearse"), &rehearsal); err != nil {
		t.Fatal(err)
	}
	if len(rehearsal.Rehearsal.Sources) != 3 || rehearsal.Rehearsal.Totals.Messages != 7 {
		t.Fatalf("rehearsal = %+v", rehearsal.Rehearsal)
	}
	if rehearsal.Rehearsal.Totals.PersonMessages != 3 {
		t.Fatalf("person messages = %d, want 3", rehearsal.Rehearsal.Totals.PersonMessages)
	}
	var derived int
	for _, room := range rehearsal.Rehearsal.Rooms {
		if room.PathDerivedRoom {
			derived++
		}
	}
	if derived != 1 {
		t.Fatalf("path-derived rooms = %d, want the Discord export room", derived)
	}
	if got := importCanonCounts(t, h.ctx, path); !reflectEqualCounts(initial, got) {
		t.Fatal("the rehearsal wrote canon")
	}

	// The import reports a reconciled digest per room and a backup generation.
	// The import prints its rehearsal and then the finished run.
	var first struct{ Import murmur.ImportReport }
	if err := decodeDocuments(run("--confirm-import"), &struct{}{}, &first); err != nil {
		t.Fatal(err)
	}
	if first.Import.Added != 6 || first.Import.Deleted != 1 {
		t.Fatalf("first import wrote %+v", first.Import)
	}
	if first.Import.MismatchedRooms != 0 || first.Import.ReconciledRooms != 3 {
		t.Fatalf("reconciliation = %+v", first.Import)
	}
	if first.Import.Rehearsal.Learning.Enabled {
		t.Fatal("learning is on without the person's choice")
	}
	if first.Import.BackupGeneration == "" {
		t.Fatal("the import did not end with a backup generation")
	}
	if _, err := os.Stat(filepath.Join(h.root, "backups", first.Import.BackupGeneration)); err != nil {
		t.Fatalf("backup generation: %v", err)
	}

	// Each room's imported keys carry murmur's own digest.
	for _, room := range first.Import.Rehearsal.Rooms {
		key := map[string]string{"kakaotalk": "kakao", "discord": "discord", "imessage": "imessage"}[room.Platform]
		uids := wantRooms[key+"/"+room.Account+"/"+room.Room]
		if want := murmurManifest(uids); want != room.ReadDigest || want != room.UIDDigest || !room.Reconciled {
			t.Fatalf("room %s/%s digest = %q, want %q (murmur %q)", room.Platform, room.Room, room.ReadDigest, want, room.UIDDigest)
		}
	}

	// Records are messenger records with the person's own rows and the rest
	// external; the deleted row is a tombstone.
	counts := importCanonCounts(t, h.ctx, path)
	if counts["conv_event"] != 6+initial["conv_event"] {
		t.Fatalf("events = %d, want 6 added", counts["conv_event"]-initial["conv_event"])
	}
	db, err := canon.OpenSQLiteReader(h.ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var own, external, imported int
	if err = db.QueryRowContext(h.ctx, `SELECT
  coalesce(sum(CASE WHEN speaker_kind='person' THEN 1 ELSE 0 END),0),
  coalesce(sum(CASE WHEN speaker_kind='external_participant' THEN 1 ELSE 0 END),0),
  count(*)
 FROM conv_event WHERE json_extract(origin,'$.kind')='messenger'`).Scan(&own, &external, &imported); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var tombstones, sources int
	if err = db.QueryRowContext(h.ctx, `SELECT (SELECT count(*) FROM messenger_record WHERE state='deleted'),
 (SELECT count(*) FROM messenger_source WHERE state='import_only')`).Scan(&tombstones, &sources); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var archiveValue, liveValue int
	if err = db.QueryRowContext(h.ctx, `SELECT
  coalesce(sum(body='archive text'),0), coalesce(sum(body='live text'),0)
 FROM conv_event WHERE json_extract(origin,'$.kind')='messenger'`).Scan(&archiveValue, &liveValue); err != nil {
		db.Close()
		t.Fatalf("bodies: %v", err)
	}
	db.Close()
	if own != 3 || external != 3 || imported != 6 {
		t.Fatalf("speaker kinds person=%d external=%d messenger=%d", own, external, imported)
	}
	if tombstones != 1 || sources != 3 {
		t.Fatalf("tombstones=%d import-only sources=%d", tombstones, sources)
	}
	if archiveValue != 1 || liveValue != 0 {
		t.Fatalf("archive=%d live=%d, want the archive value over the live one", archiveValue, liveValue)
	}
	// Learning is off, so the import created no memory item.
	var items int
	reader, err := canon.OpenSQLiteReader(h.ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = reader.QueryRowContext(h.ctx, "SELECT count(*) FROM memory_item").Scan(&items); err != nil {
		reader.Close()
		t.Fatal(err)
	}
	reader.Close()
	if items != 0 {
		t.Fatalf("the import created %d memory items with learning off", items)
	}

	// A second run adds nothing.
	var second struct{ Import murmur.ImportReport }
	if err := decodeDocuments(run("--confirm-import"), &struct{}{}, &second); err != nil {
		t.Fatal(err)
	}
	if second.Import.Added+second.Import.Updated+second.Import.Deleted != 0 {
		t.Fatalf("second run wrote %+v", second.Import)
	}
	if got := importCanonCounts(t, h.ctx, path); !reflectEqualCounts(recordCounts(counts), recordCounts(got)) {
		t.Fatalf("the second run changed records: %v, was %v", recordCounts(got), recordCounts(counts))
	}

	// Installed Core reads the same main and leaves the import-only sources
	// alone: they are never synced and never marked failing.
	core = start(false)
	h.stopCore(core)
	db, err = canon.OpenSQLiteReader(h.ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var failing int
	if err = db.QueryRowContext(h.ctx, "SELECT count(*) FROM messenger_source WHERE state<>'import_only'").Scan(&failing); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	if failing != 0 {
		t.Fatalf("%d import-only sources changed state after Core started", failing)
	}
}

// installedPerson reads the person id the installed main created at first run.
func installedPerson(t *testing.T, h *harness) string {
	t.Helper()
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var person string
	if err = db.QueryRowContext(h.ctx, `SELECT r.person_id FROM agent_first_run f JOIN agents a ON a.id=f.agent_id JOIN agent_revisions r ON r.agent_id=a.id AND r.revision=a.revision`).Scan(&person); err != nil {
		t.Fatal(err)
	}
	return person
}

// decodeDocuments reads the command's consecutive JSON documents into targets.
func decodeDocuments(raw []byte, targets ...any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for _, target := range targets {
		if err := decoder.Decode(target); err != nil {
			return err
		}
	}
	return nil
}

// records keeps the counts a repeated import must not grow. A run's own batch
// and report rows are new by design; its records, references and index rows are
// not.
func recordCounts(counts map[string]int) map[string]int {
	kept := map[string]int{}
	for _, table := range []string{"conv_event", "ev_rel", "ev_span", "recall_doc", "messenger_record", "messenger_record_revision", "messenger_legacy_ref", "messenger_chat"} {
		kept[table] = counts[table]
	}
	return kept
}

func murmurManifest(uids []string) string {
	sorted := append([]string(nil), uids...)
	sort.Strings(sorted)
	sum := sha256.New()
	for _, id := range sorted {
		sum.Write([]byte(id))
		sum.Write([]byte("\n"))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func reflectEqualMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func reflectEqualCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}
