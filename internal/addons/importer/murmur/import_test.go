// SPDX-License-Identifier: Apache-2.0
package murmur_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/addons/importer/murmur"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/retract"
)

// home builds a synthetic murmur data home: its config and a spool of events
// written in murmur's own format. No real store is read.
type home struct {
	root   string
	events []event
}

type event struct {
	messages     []record
	observations []observation
}

type record struct {
	source, account, room, native string
	author, authorName            string
	at                            time.Time
	edited                        *time.Time
	text                          string
	deleted                       bool
	replyTo, thread               string
	ingest                        string
	roomName                      string
}

type observation struct {
	source, room, ingest string
	collected            time.Time
}

func (r record) uid() string { return uid(r.source, r.room, r.native) }

// uid repeats murmur's own identity: SHA-256 of the compact JSON array.
func uid(source, room, native string) string {
	encoded, _ := json.Marshal([]string{source, room, native})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (h *home) write(t *testing.T, rawTail string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(h.root, "spool"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"version": 1, "embedding": map[string]any{"provider": "local", "model": "synthetic", "threads": 1}, "integrity": map[string]any{"business_hours": 6, "other_hours": 24, "live_window_hours": 20}, "rooms": []any{}, "sources": []any{}}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, "config.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	var spool []byte
	for index, event := range h.events {
		line := map[string]any{
			"version": 1, "event_id": fmt.Sprintf("event-%d", index),
			"ingested_at": "2026-10-20T00:00:00.000000Z",
			"messages":    messagesOf(event.messages),
			"observations": func() []map[string]any {
				var out []map[string]any
				for _, value := range event.observations {
					out = append(out, map[string]any{
						"source": value.source, "room_id": value.room, "ingest_path": value.ingest,
						"collected_at":   value.collected.Format("2006-01-02T15:04:05.000000Z"),
						"coverage_since": nil, "coverage_until": nil, "first_message_ts": nil,
					})
				}
				return out
			}(),
		}
		encoded, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		spool = append(spool, encoded...)
		spool = append(spool, '\n')
	}
	spool = append(spool, rawTail...)
	if err := os.WriteFile(filepath.Join(h.root, "spool/events.jsonl"), spool, 0o600); err != nil {
		t.Fatal(err)
	}
}

func messagesOf(records []record) []map[string]any {
	var out []map[string]any
	for _, value := range records {
		deleted := 0
		if value.deleted {
			deleted = 1
		}
		var edited any
		if value.edited != nil {
			edited = value.edited.Format("2006-01-02T15:04:05.000000Z")
		}
		var reply, thread any
		if value.replyTo != "" {
			reply = uid(value.source, value.room, value.replyTo)
		}
		if value.thread != "" {
			thread = uid(value.source, value.room, value.thread)
		}
		name := value.roomName
		if name == "" {
			name = value.room
		}
		out = append(out, map[string]any{
			"source": value.source, "account": value.account, "room_id": value.room,
			"native_id": value.native, "uid": value.uid(), "author_id": value.author,
			"author_name": value.authorName, "ts": value.at.Format("2006-01-02T15:04:05.000000Z"),
			"edited_ts": edited, "deleted": deleted, "reply_to_uid": reply,
			"thread_root_uid": thread, "text": value.text, "attachments": []any{},
			"raw": map[string]any{}, "ingest_path": value.ingest, "privacy_class": "private",
			"filled_from": nil, "room_name": name,
		})
	}
	return out
}

// state is a stopped main with canon, an asset store and the retract store the
// messenger admission needs.
type state struct {
	root  string
	db    *canon.Store
	store *messenger.Store
	now   time.Time
}

func newState(t *testing.T) *state {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(root, 0o700); err != nil {
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
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	removals, err := retract.Open(t.Context(), retract.Config{Canon: db, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removals.Close() })
	s := &state{root: root, db: db, now: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)}
	s.store = messenger.Open(db, nil, removals).WithClock(func() time.Time { return s.now })
	return s
}

func (s *state) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), query, args...).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func synthetic(t *testing.T) *home {
	t.Helper()
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	edited := start.Add(30 * time.Minute)
	h := &home{root: filepath.Join(t.TempDir(), "murmur-home")}
	// One KakaoTalk key arrives twice: the archive value wins over the live one.
	// The archive row also carries an edit and a reply.
	h.events = []event{
		{
			messages: []record{
				{source: "kakao", account: "kakao-1", room: "chat-1", native: "100", author: "kakao-1", authorName: "me", at: start, text: "live text", ingest: "live"},
				{source: "kakao", account: "kakao-1", room: "chat-1", native: "101", author: "friend-9", authorName: "Friend", at: start.Add(time.Minute), text: "reply token=secret-value", ingest: "live", replyTo: "100"},
			},
			observations: []observation{{source: "kakao", room: "chat-1", ingest: "live", collected: start.Add(2 * time.Minute)}},
		},
		{
			messages: []record{
				{source: "kakao", account: "kakao-1", room: "chat-1", native: "100", author: "kakao-1", authorName: "me", at: start, edited: &edited, text: "archive text", ingest: "archive"},
			},
			observations: []observation{{source: "kakao", room: "chat-1", ingest: "archive", collected: start.Add(time.Hour)}},
		},
		{
			messages: []record{
				{source: "discord", account: "discord-1", room: "general", native: "d-1", author: "friend-2", authorName: "Other", at: start.Add(3 * time.Minute), text: "path derived", ingest: "export", roomName: "guild-a/general"},
				{source: "discord", account: "discord-1", room: "general", native: "d-2", author: "discord-1", authorName: "me", at: start.Add(4 * time.Minute), text: "mine", ingest: "export", roomName: "guild-a/general"},
			},
			observations: []observation{{source: "discord", room: "general", ingest: "export", collected: start.Add(5 * time.Minute)}},
		},
		{
			messages: []record{
				{source: "imessage", account: "mac", room: "chat-guid-1", native: "i-1", author: "me", authorName: "me", at: start.Add(6 * time.Minute), text: "hello from me", ingest: "archive", roomName: "chat-guid-1"},
				{source: "imessage", account: "mac", room: "chat-guid-1", native: "i-2", author: "+15550001", authorName: "+15550001", at: start.Add(7 * time.Minute), text: "hi", ingest: "archive", roomName: "chat-guid-1"},
				{source: "imessage", account: "mac", room: "chat-guid-1", native: "gone", author: "+15550001", authorName: "+15550001", at: start.Add(8 * time.Minute), text: "removed later", ingest: "archive", deleted: true, roomName: "chat-guid-1"},
			},
			observations: []observation{{source: "imessage", room: "chat-guid-1", ingest: "archive", collected: start.Add(9 * time.Minute)}},
		},
	}
	return h
}

func profile() murmur.Profile {
	return murmur.Profile{Person: "person:import", Instance: "home-1", Own: map[string]string{"kakao": "kakao-1", "discord": "discord-1"}}
}

func prepare(t *testing.T, h *home, profile murmur.Profile) *murmur.Plan {
	t.Helper()
	staging := t.TempDir()
	plan, err := murmur.Prepare(t.Context(), murmur.Request{Home: h.root, Staging: staging, Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { plan.Close() })
	return plan
}

// The rehearsal writes nothing to canon and reports each source, each room, the
// person's own messages, masked spans and refused rows.
func TestPrepareRehearsesWithoutWritingCanon(t *testing.T) {
	s := newState(t)
	h := synthetic(t)
	h.write(t, "")
	plan := prepare(t, h, profile())
	report := plan.Rehearse()
	if got := s.count(t, "SELECT count(*) FROM conv_event"); got != 0 {
		t.Fatalf("rehearsal wrote %d events", got)
	}
	if len(report.Sources) != 3 {
		t.Fatalf("sources = %+v, want kakao, discord and imessage", report.Sources)
	}
	for _, source := range report.Sources {
		if source.Messages == 0 || source.Rooms == 0 {
			t.Fatalf("source %s reports %+v", source.Source, source)
		}
	}
	// The archive row replaced the live row of one KakaoTalk key.
	if report.Totals.Messages != 7 {
		t.Fatalf("messages = %d, want 7", report.Totals.Messages)
	}
	// The person's own rows: one KakaoTalk, one Discord, one iMessage.
	if report.Totals.PersonMessages != 3 {
		t.Fatalf("person messages = %d, want 3", report.Totals.PersonMessages)
	}
	if report.MaskedSpans == 0 {
		t.Fatal("the token-shaped value was not reported as masked")
	}
	var derived int
	for _, room := range report.Rooms {
		if room.PathDerivedRoom {
			derived++
			if room.Platform != "discord" {
				t.Fatalf("path-derived room on %s", room.Platform)
			}
		}
	}
	if derived != 1 {
		t.Fatalf("path-derived rooms = %d, want the Discord export room", derived)
	}
	if report.LinkedSenders != 0 || report.SenderIdentity == "" {
		t.Fatalf("sender identity report = %+v", report)
	}
	if report.Learning.Enabled {
		t.Fatal("learning is on without the person's choice")
	}
}

// A corrupt spool tail refuses the whole home and writes nothing.
func TestCorruptTailIsRefusedwithNothingWritten(t *testing.T) {
	s := newState(t)
	h := synthetic(t)
	h.write(t, `{"version":1,"event_id":"torn","ingested_at":"2026-10-20T00:00:00.000000Z","messages":[`)
	_, err := murmur.Prepare(t.Context(), murmur.Request{Home: h.root, Staging: t.TempDir(), Profile: profile()})
	if !errors.Is(err, murmur.ErrIncompleteTail) && !errors.Is(err, murmur.ErrRefused) {
		t.Fatalf("prepare error = %v, want a refusal", err)
	}
	if got := s.count(t, "SELECT count(*) FROM conv_event"); got != 0 {
		t.Fatalf("refused home wrote %d events", got)
	}
	if got := s.count(t, "SELECT count(*) FROM messenger_source"); got != 0 {
		t.Fatalf("refused home registered %d sources", got)
	}
}

// A spool of another version or with a field this adapter does not know is
// refused with the difference named.
func TestSchemaGateNamesTheDifference(t *testing.T) {
	for _, tc := range []struct {
		name, line string
	}{
		{"version", `{"version":2,"event_id":"e","ingested_at":"2026-10-20T00:00:00.000000Z","messages":[],"observations":[]}` + "\n"},
		{"field", `{"version":1,"event_id":"e","ingested_at":"2026-10-20T00:00:00.000000Z","messages":[],"observations":[],"extra":true}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &home{root: filepath.Join(t.TempDir(), "home")}
			h.write(t, "")
			if err := os.WriteFile(filepath.Join(h.root, "spool/events.jsonl"), []byte(tc.line), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := murmur.Prepare(t.Context(), murmur.Request{Home: h.root, Staging: t.TempDir(), Profile: profile()})
			if !errors.Is(err, murmur.ErrRefused) {
				t.Fatalf("error = %v, want a refusal", err)
			}
		})
	}
}

// Every source type becomes messenger records keyed by murmur's uid; per-room
// counts and digests match murmur's own manifest; a second run adds nothing; and
// the data home is byte-identical afterwards.
func TestImportReconcilesAndRepeats(t *testing.T) {
	s := newState(t)
	h := synthetic(t)
	h.write(t, "")
	before := tree(t, h.root)
	plan := prepare(t, h, profile())
	imported, err := plan.Import(t.Context(), s.store, "batch-1", func(context.Context) (string, error) { return "generation-1", nil })
	if err != nil {
		t.Fatal(err)
	}
	if imported.MismatchedRooms != 0 || imported.ReconciledRooms != 3 {
		t.Fatalf("reconciliation = %+v", imported)
	}
	if imported.Added != 6 || imported.Deleted != 1 {
		t.Fatalf("wrote %d records and %d deletions, want 6 and 1", imported.Added, imported.Deleted)
	}
	if imported.BackupGeneration != "generation-1" {
		t.Fatalf("backup generation = %q", imported.BackupGeneration)
	}
	if got := s.count(t, "SELECT count(*) FROM messenger_source WHERE state='import_only'"); got != 3 {
		t.Fatalf("import-only sources = %d, want 3", got)
	}
	// No memory item exists: learning is off.
	if got := s.count(t, "SELECT count(*) FROM memory_item"); got != 0 {
		t.Fatalf("learning off created %d memory items", got)
	}
	// Speaker kinds come from the confirmed map and iMessage's own flag.
	if got := s.count(t, "SELECT count(DISTINCT speaker_kind) FROM conv_event"); got != 2 {
		t.Fatalf("speaker kinds = %d, want person and external_participant", got)
	}
	if got := s.count(t, "SELECT count(*) FROM conv_event WHERE speaker_kind='person'"); got != 3 {
		t.Fatalf("person records = %d, want 3", got)
	}
	// The retained report is the rehearsal, and the batch row names the backup.
	if got := s.count(t, "SELECT count(*) FROM messenger_import WHERE backup_generation='generation-1' AND learning=0"); got != 1 {
		t.Fatalf("import rows = %d, want 1", got)
	}
	again := prepare(t, h, profile())
	repeat, err := again.Import(t.Context(), s.store, "batch-2", func(context.Context) (string, error) { return "generation-2", nil })
	if err != nil {
		t.Fatal(err)
	}
	if repeat.Added != 0 || repeat.Updated != 0 || repeat.Deleted != 0 {
		t.Fatalf("second run wrote %+v", repeat)
	}
	if repeat.ReconciledRooms != 3 || repeat.MismatchedRooms != 0 {
		t.Fatalf("second reconciliation = %+v", repeat)
	}
	if got := s.count(t, "SELECT count(*) FROM conv_event"); got != 6 {
		t.Fatalf("records after the second run = %d, want 6", got)
	}
	// A source-reported deletion is a tombstone, not an event.
	if got := s.count(t, "SELECT count(*) FROM messenger_record WHERE state='deleted'"); got != 1 {
		t.Fatalf("tombstones = %d, want 1", got)
	}
	if after := tree(t, h.root); after != before {
		t.Fatal("the data home changed during the import")
	}
}

// An edited source row becomes a new revision that supersedes the earlier one,
// and the archive value keeps winning over a live row of the same key.
func TestEditSupersedesAndArchiveWins(t *testing.T) {
	s := newState(t)
	h := synthetic(t)
	h.write(t, "")
	plan := prepare(t, h, profile())
	if _, err := plan.Import(t.Context(), s.store, "batch-1", capture); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	later := start.Add(2 * time.Hour)
	edited := start.Add(3 * time.Hour)
	// The same KakaoTalk key gains an edit; a live row must not displace it.
	h.events = append(h.events, event{
		messages: []record{
			{source: "kakao", account: "kakao-1", room: "chat-1", native: "100", author: "kakao-1", authorName: "me", at: start, edited: &edited, text: "edited once more", ingest: "archive"},
			{source: "kakao", account: "kakao-1", room: "chat-1", native: "100", author: "kakao-1", authorName: "me", at: start, text: "stale live text", ingest: "live"},
			{source: "kakao", account: "kakao-1", room: "chat-1", native: "102", author: "friend-9", authorName: "Friend", at: later, text: "new row", ingest: "live"},
		},
		observations: []observation{{source: "kakao", room: "chat-1", ingest: "archive", collected: later.Add(time.Minute)}},
	})
	h.write(t, "")
	second := prepare(t, h, profile())
	report, err := second.Import(t.Context(), s.store, "batch-2", capture)
	if err != nil {
		t.Fatal(err)
	}
	if report.Added != 1 || report.Updated != 1 {
		t.Fatalf("second import wrote added=%d updated=%d, want 1 and 1", report.Added, report.Updated)
	}
	if got := s.count(t, "SELECT revision FROM messenger_record WHERE platform='kakaotalk' AND message='100'"); got != 2 {
		t.Fatalf("revision = %d, want 2", got)
	}
	if got := s.count(t, "SELECT count(*) FROM ev_rel WHERE kind='supersedes' AND event_id LIKE '%:100#2'"); got != 1 {
		t.Fatalf("supersedes relations = %d, want 1", got)
	}
	if got := s.count(t, "SELECT count(*) FROM conv_event WHERE body='stale live text'"); got != 0 {
		t.Fatal("the live value displaced the archive value")
	}
	if got := s.count(t, "SELECT count(*) FROM conv_event WHERE body='edited once more'"); got != 1 {
		t.Fatal("the edited archive value is not the newest record")
	}
}

// A source row whose content changed without an edit is a conflict: the import
// keeps what it has and reports the row.
func TestChangedRowIsAConflict(t *testing.T) {
	s := newState(t)
	h := synthetic(t)
	h.write(t, "")
	plan := prepare(t, h, profile())
	if _, err := plan.Import(t.Context(), s.store, "batch-1", capture); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	h.events = []event{{
		messages: []record{
			{source: "discord", account: "discord-1", room: "general", native: "d-1", author: "friend-2", authorName: "Other", at: start.Add(3 * time.Minute), text: "rewritten without an edit", ingest: "export", roomName: "guild-a/general"},
		},
		observations: []observation{{source: "discord", room: "general", ingest: "export", collected: start.Add(time.Hour)}},
	}}
	h.write(t, "")
	second := prepare(t, h, profile())
	report, err := second.Import(t.Context(), s.store, "batch-2", capture)
	if err != nil {
		t.Fatal(err)
	}
	var conflicts int
	for _, room := range report.Rehearsal.Rooms {
		conflicts += len(room.Conflicts)
	}
	if conflicts != 1 {
		t.Fatalf("conflicts = %d, want 1: %+v", conflicts, report.Rehearsal.Rooms)
	}
	if got := s.count(t, "SELECT count(*) FROM conv_event WHERE body='path derived'"); got != 1 {
		t.Fatal("the conflict overwrote the imported record")
	}
}

// With learning on, the run states the count it covers and puts the source's
// watermark at the start of the history so the extraction job covers it.
func TestLearningChoiceIsRecorded(t *testing.T) {
	s := newState(t)
	h := synthetic(t)
	h.write(t, "")
	withLearning := profile()
	withLearning.Learning = true
	plan := prepare(t, h, withLearning)
	report := plan.Rehearse()
	if !report.Learning.Enabled || report.Learning.PersonMessages != 3 || report.Learning.Calls == 0 {
		t.Fatalf("learning card = %+v", report.Learning)
	}
	imported, err := plan.Import(t.Context(), s.store, "batch-1", capture)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Rehearsal.Learning.PersonMessages != 3 {
		t.Fatalf("import report learning = %+v", imported.Rehearsal.Learning)
	}
	if got := s.count(t, "SELECT count(*) FROM messenger_import WHERE learning=1 AND learning_messages=3 AND learning_through<>''"); got != 1 {
		t.Fatalf("learning rows = %d, want 1", got)
	}
	if got := s.count(t, "SELECT count(*) FROM memory_item"); got != 0 {
		t.Fatalf("the importer itself created %d memory items; learning belongs to the extraction job", got)
	}
}

func capture(context.Context) (string, error) { return "generation", nil }

// tree is a stable digest of every file under root: the source must not change.
func tree(t *testing.T, root string) string {
	t.Helper()
	var names []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		names = append(names, path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	sum := sha256.New()
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(sum, "%s\x00%s\x00", name, sha256.Sum256(data))
	}
	return hex.EncodeToString(sum.Sum(nil))
}
