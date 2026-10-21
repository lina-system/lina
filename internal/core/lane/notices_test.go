// SPDX-License-Identifier: Apache-2.0
package lane_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func noticeDB(t *testing.T) *canon.Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	ms := canon.Migrations()
	ms = append(ms, canon.Migration{Version: len(ms) + 1, SQL: lane.NoticeSchema})
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: c, Migrations: ms})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}
func notice(id, conv string, at time.Time) lane.Notice {
	return lane.Notice{Surface: ledger.Surface{Kind: "tui"}, ID: id, Run: "run-" + id, Person: "person", Agent: "agent", Conversation: conv, Outcome: "complete", Summary: "Compared the sources", Result: "material", ResultType: "summary", Landed: at}
}
func TestNoticesWaitForWholeTurnAndQueuedInputThenCoalesce(t *testing.T) {
	// SQLite has real I/O: channels establish the boundary; the sink announces
	// committed ordering without a clock delay or assuming a goroutine got CPU.
	db := noticeDB(t)
	order := make(chan string, 8)
	notices := lane.NewNotices(db, nil)
	notices.OnDelivered = func(lane.Delivery) { order <- "notice" }
	lanes := lane.NewWithNotices(notices)
	t.Cleanup(func() { lanes.Close(context.Background()) })
	started, release := make(chan struct{}), make(chan struct{})
	first := submit(t, lanes, t.Context(), "conversation", func(context.Context) error { close(started); <-release; order <- "answer"; return nil })
	<-started
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, n := range []lane.Notice{notice("one", "conversation", at), notice("two", "conversation", at.Add(10*time.Second)), notice("three", "conversation", at.Add(10*time.Second+time.Nanosecond))} {
		if err := lanes.Notice(t.Context(), n); err != nil {
			t.Fatal(err)
		}
	}
	delivered, err := notices.Delivered(t.Context(), "conversation")
	if err != nil || len(delivered) != 0 {
		t.Fatalf("delivered during reply: %+v %v", delivered, err)
	}
	second := submit(t, lanes, t.Context(), "conversation", func(context.Context) error { order <- "input"; return nil })
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	var got []string
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for len(got) < 4 {
		select {
		case v := <-order:
			got = append(got, v)
		case <-timeout.C:
			t.Fatalf("missing delivery: %v", got)
		}
	}
	if !reflect.DeepEqual(got, []string{"answer", "input", "notice", "notice"}) {
		t.Fatal(got)
	}
	// The callback runs only after the durable delivery commits.
	if err := lanes.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	delivered, err = notices.Delivered(t.Context(), "conversation")
	if err != nil || len(delivered) != 2 || len(delivered[0].Notices) != 2 || len(delivered[1].Notices) != 1 {
		t.Fatalf("coalescing: %+v %v", delivered, err)
	}
}
func TestIdleNoticeIsImmediateAndOtherConversationIsIndependent(t *testing.T) {
	db := noticeDB(t)
	done := make(chan struct{}, 2)
	notices := lane.NewNotices(db, nil)
	notices.OnDelivered = func(lane.Delivery) { done <- struct{}{} }
	lanes := lane.NewWithNotices(notices)
	defer lanes.Close(context.Background())
	started, release := make(chan struct{}), make(chan struct{})
	busy := submit(t, lanes, t.Context(), "busy", func(context.Context) error { close(started); <-release; return nil })
	<-started
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	n := notice("idle", "idle", time.Now())
	if err := lanes.Notice(t.Context(), n); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("idle notice was delayed")
	}
	if err := lanes.Notice(t.Context(), n); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-busy; err != nil {
		t.Fatal(err)
	}
	if err := lanes.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := notices.Delivered(t.Context(), "idle")
	if err != nil || len(rows) != 1 {
		t.Fatalf("duplicate notice: %v %v", rows, err)
	}
}
func TestFailedDeliveryRemainsDurableForResume(t *testing.T) {
	db := noticeDB(t)
	failed := make(chan struct{})
	notices := lane.NewNotices(db, func(context.Context, *canon.Tx, lane.Delivery) error {
		close(failed)
		return errors.New("ledger unavailable")
	})
	lanes := lane.NewWithNotices(notices)
	if err := lanes.Notice(t.Context(), notice("retry", "conversation", time.Now())); err != nil {
		t.Fatal(err)
	}
	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery never attempted")
	}
	if err := lanes.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	delivered, err := notices.Delivered(t.Context(), "conversation")
	if err != nil || len(delivered) != 0 {
		t.Fatalf("failed write delivered: %v %v", delivered, err)
	}
	done := make(chan struct{})
	restored := lane.NewNotices(db, nil)
	restored.OnDelivered = func(lane.Delivery) { close(done) }
	replacement := lane.NewWithNotices(restored)
	defer replacement.Close(context.Background())
	if err := replacement.ResumeNotices(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("durable notice lost")
	}
	if err := replacement.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	delivered, err = restored.Delivered(t.Context(), "conversation")
	if err != nil || len(delivered) != 1 {
		t.Fatalf("resume: %v %v", delivered, err)
	}
}

func TestDeliveredNoticeIsSystemEvidenceInConversationLedger(t *testing.T) {
	db := noticeDB(t)
	notices := lane.NewNotices(db, nil)
	done := make(chan lane.Delivery, 1)
	notices.OnDelivered = func(d lane.Delivery) { done <- d }
	lanes := lane.NewWithNotices(notices)
	defer lanes.Close(context.Background())
	n := notice("ledger", "conversation", time.Now())
	if err := lanes.Notice(t.Context(), n); err != nil {
		t.Fatal(err)
	}
	var delivery lane.Delivery
	select {
	case delivery = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no committed delivery")
	}
	var count int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM conv_event WHERE conversation_id=? AND speaker_kind='system' AND scaffold_kind='background_notice'", delivery.Conversation).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT id FROM conv_event WHERE conversation_id=?", delivery.Conversation).Scan(&eventID)
	}); err != nil {
		t.Fatal(err)
	}
	event, err := ledger.New(db, nil).Get(t.Context(), eventID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := event.Message()
	if err != nil {
		t.Fatal(err)
	}
	evidence, external := message.Evidence()
	if event.SpeakerKind != guard.System || !external || !evidence.Untrusted || evidence.Role != "user" {
		t.Fatalf("notice acquired authority: %+v", evidence)
	}
	if count != 1 {
		t.Fatalf("conversation ledger notices=%d", count)
	}
}

func TestNoticeMasksSummaryBeforeOutboxPersistence(t *testing.T) {
	db := noticeDB(t)
	notices := lane.NewNotices(db, nil)
	lanes := lane.NewWithNotices(notices)
	defer lanes.Close(context.Background())
	started, release := make(chan struct{}), make(chan struct{})
	running := submit(t, lanes, t.Context(), "conversation", func(context.Context) error { close(started); <-release; return nil })
	<-started
	defer func() { close(release); <-running }()
	n := notice("masked", "conversation", time.Now())
	token := "sk-proj-" + strings.Repeat("A7", 24)
	n.Summary = "Checked " + token + " in the source"
	if err := lanes.Notice(t.Context(), n); err != nil {
		t.Fatal(err)
	}
	var record []byte
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT record FROM lane_notices WHERE id=?", n.ID).Scan(&record)
	}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(record, []byte(token)) {
		t.Fatal("raw secret persisted in notice outbox")
	}
}
