// SPDX-License-Identifier: Apache-2.0
package gateway_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/lease"
)

var now = time.Unix(1700000000, 0)
var bot = channels.Bot{Platform: "test", Account: "bot"}
var chat = channels.Address{Bot: bot, Chat: "chat"}

func openDB(t *testing.T, root string, epoch uint64) *canon.Store {
	t.Helper()
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	ms := canon.Migrations()
	ms = append(ms, canon.Migration{Version: len(ms) + 1, SQL: channels.Schema}, canon.Migration{Version: len(ms) + 2, SQL: turn.Schema}, canon.Migration{Version: len(ms) + 3, SQL: turn.ControlSchema})
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: ms})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}
func setupAt(t *testing.T, root string) (*canon.Store, *channels.Store, string) {
	t.Helper()
	db := openDB(t, root, 1)
	as, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { as.Close() })
	a, err := as.Create(t.Context(), agents.Create{Name: "Synthetic", PersonID: "owner", Layers: agents.Layers{Core: "core", DefaultVoice: "voice"}})
	if err != nil {
		t.Fatal(err)
	}
	s := channels.New(db)
	if err = s.Connect(t.Context(), bot, "owner"); err != nil {
		t.Fatal(err)
	}
	code, err := s.PairingCode(t.Context(), bot, "owner", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Pair(t.Context(), bot, code, "123", chat.Chat, now); err != nil {
		t.Fatal(err)
	}
	if err = s.Bind(t.Context(), channels.Binding{Address: chat, Agent: a.ID, Conversation: "conversation", Person: "owner", Scope: channels.PrivateScope()}); err != nil {
		t.Fatal(err)
	}
	return db, s, a.ID
}
func setup(t *testing.T) (*canon.Store, *channels.Store, string) {
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	return setupAt(t, root)
}
func event(id, previous, next string) gateway.Inbound {
	return gateway.Inbound{Address: chat, NativeID: id, Sender: "123", Direct: true, PreviousCursor: previous, Cursor: next, Body: []byte("hello")}
}
func scalar(t *testing.T, db *canon.Store, query string) int {
	t.Helper()
	var n int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error { return tx.QueryRowContext(t.Context(), query).Scan(&n) }); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestAdmissionDedupeCursorAndUnpaired(t *testing.T) {
	db, s, _ := setup(t)
	var turns []gateway.Delivery
	g := gateway.New(db, func(_ context.Context, d gateway.Delivery) error { turns = append(turns, d); return nil })
	e := event("1", "", "next-1")
	if err := g.Admit(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if err := g.Admit(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	outsider := event("2", "next-1", "next-2")
	outsider.Sender = "456"
	if err := g.Admit(t.Context(), outsider); err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].InboxID <= 0 || !turns[0].Scope.Tools || scalar(t, db, "SELECT count(*) FROM inbox") != 1 {
		t.Fatalf("turns: %+v", turns)
	}
	cursor, err := s.Cursor(t.Context(), bot)
	if err != nil || cursor != "next-2" {
		t.Fatalf("cursor=%q %v", cursor, err)
	}
	if err = g.Admit(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	cursor, _ = s.Cursor(t.Context(), bot)
	if cursor != "next-2" {
		t.Fatal("duplicate rewound cursor")
	}
	if err = g.Admit(t.Context(), event("3", "old", "next-3")); !errors.Is(err, channels.ErrCursor) {
		t.Fatal(err)
	}
	if scalar(t, db, "SELECT count(*) FROM inbox") != 1 {
		t.Fatal("stale cursor admitted")
	}
}

type rollbackDB struct{ *canon.Store }

func (db rollbackDB) Write(ctx context.Context, f func(*canon.Tx) error) error {
	return db.Store.Write(ctx, func(tx *canon.Tx) error {
		if err := f(tx); err != nil {
			return err
		}
		return errors.New("injected rollback")
	})
}
func TestAdmissionRollbackNeverDispatches(t *testing.T) {
	db, s, _ := setup(t)
	calls := 0
	g := gateway.New(rollbackDB{db}, func(context.Context, gateway.Delivery) error { calls++; return nil })
	if err := g.Admit(t.Context(), event("1", "", "next")); err == nil {
		t.Fatal("missing failure")
	}
	c, _ := s.Cursor(t.Context(), bot)
	if calls != 0 || c != "" || scalar(t, db, "SELECT count(*) FROM inbox") != 0 {
		t.Fatal("partial admission")
	}
}
func TestBindingSpecificityScopeAndAgentDeletion(t *testing.T) {
	db, s, id := setup(t)
	topic := chat
	topic.Topic = "topic"
	b := channels.Binding{Address: topic, Agent: id, Conversation: "topic", Person: "owner", Scope: channels.Scope{Reads: channels.Addressed, Writes: channels.ReplyOnly}}
	if err := s.Bind(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	var turns []gateway.Delivery
	g := gateway.New(db, func(_ context.Context, d gateway.Delivery) error { turns = append(turns, d); return nil })
	e := event("1", "", "a")
	e.Address = topic
	e.Direct = false
	if err := g.Admit(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	e.NativeID = "2"
	e.PreviousCursor = "a"
	e.Cursor = "b"
	e.Addressed = true
	if err := g.Admit(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Conversation != "topic" || turns[0].Scope.Tools || turns[0].Scope.Memory {
		t.Fatalf("%+v", turns)
	}
	// Deletion uses the existing agents transaction; restoring it cannot revive a binding.
	as, err := agents.Open(filepath.Dir(dbPath(t, db)), db)
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	if err = as.Delete(t.Context(), id, "owner", now); err != nil {
		t.Fatal(err)
	}
	if err = as.Restore(t.Context(), id, "owner", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.NativeID = "3"
	e.PreviousCursor = "b"
	e.Cursor = "c"
	if err := g.Admit(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatal("deleted binding dispatched")
	}
}

type sender struct {
	calls  int
	result gateway.SendResult
	err    error
	hook   func()
}

func (s *sender) Send(context.Context, lease.Lease, gateway.Outbound) (gateway.SendResult, error) {
	s.calls++
	if s.hook != nil {
		s.hook()
	}
	return s.result, s.err
}
func enqueue(t *testing.T, db *canon.Store, s *channels.Store, id string, parts ...string) {
	t.Helper()
	g := gateway.New(db, func(context.Context, gateway.Delivery) error { return nil })
	if err := g.Admit(t.Context(), event("1", "", "next")); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(t.Context(), channels.Message{ID: id, Address: chat, Agent: bindingAgent(t, s), LedgerRef: "reply-" + id, ReplyTo: "1", Parts: parts}); err != nil {
		t.Fatal(err)
	}
}
func bindingAgent(t *testing.T, s *channels.Store) string {
	t.Helper()
	b, err := s.Binding(t.Context(), chat)
	if err != nil {
		t.Fatal(err)
	}
	return b.Agent
}
func TestUnknownOutcomeNeverRepeated(t *testing.T) {
	db, s, _ := setup(t)
	enqueue(t, db, s, "one", "part 1", "part 2")
	a := &sender{err: errors.New("connection lost")}
	l := lease.New(func() {})
	defer l.Release()
	q := gateway.NewQueue(db, a)
	if _, err := q.SendNext(t.Context(), l, chat, now); err == nil {
		t.Fatal("expected transport failure")
	}
	if err := q.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	enqueue(t, db, s, "one", "changed")
	if sent, err := q.SendNext(t.Context(), l, chat, now); err != nil || sent {
		t.Fatalf("retried: %v %v", sent, err)
	}
	m, err := s.Message(t.Context(), "one")
	if err != nil || m.State != channels.Unknown || m.NextPart != 0 || m.Parts[0] != "part 1" || a.calls != 1 {
		t.Fatalf("%+v %v calls=%d", m, err, a.calls)
	}
}
func TestRetryAfterResumesRefusedPartAndBlocksFollowingReply(t *testing.T) {
	db, s, _ := setup(t)
	enqueue(t, db, s, "one", "first", "second")
	enqueue(t, db, s, "two", "later")
	a := &sender{result: gateway.SendResult{Outcome: gateway.Delivered, MessageID: "remote-1"}}
	l := lease.New(func() {})
	defer l.Release()
	q := gateway.NewQueue(db, a)
	if sent, err := q.SendNext(t.Context(), l, chat, now); err != nil || !sent {
		t.Fatal(sent, err)
	}
	a.result = gateway.SendResult{Outcome: gateway.RetryLater, RetryAfter: time.Minute}
	if _, err := q.SendNext(t.Context(), l, chat, now); err != nil {
		t.Fatal(err)
	}
	if sent, err := q.SendNext(t.Context(), l, chat, now.Add(59*time.Second)); err != nil || sent {
		t.Fatal("retry too early", sent, err)
	}
	a.result = gateway.SendResult{Outcome: gateway.Delivered, MessageID: "remote-2"}
	if sent, err := q.SendNext(t.Context(), l, chat, now.Add(time.Minute)); err != nil || !sent {
		t.Fatal(sent, err)
	}
	m, err := s.Message(t.Context(), "one")
	if err != nil || m.State != channels.Sent || m.NextPart != 2 || a.calls != 3 {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestQueueSerializesTopicsAndRejectsStaleSettlement(t *testing.T) {
	db, s, _ := setup(t)
	enqueue(t, db, s, "one", "part")
	m, err := s.ClaimNext(t.Context(), chat, now)
	if err != nil {
		t.Fatal(err)
	}
	topic := chat
	topic.Topic = "topic"
	if _, err = s.ClaimNext(t.Context(), topic, now); !errors.Is(err, channels.ErrEmpty) {
		t.Fatal("parallel send", err)
	}
	if err = s.Settle(t.Context(), m, channels.Queued, "", now); err != nil {
		t.Fatal(err)
	}
	next, err := s.ClaimNext(t.Context(), topic, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Settle(t.Context(), m, channels.Sent, "stale", now); !errors.Is(err, channels.ErrConflict) {
		t.Fatal("stale attempt settled new attempt", err)
	}
	if err = s.Settle(t.Context(), next, channels.Unknown, "", now); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseRefusalAndPermanentBlock(t *testing.T) {
	db, s, _ := setup(t)
	enqueue(t, db, s, "one", "reply")
	enqueue(t, db, s, "two", "later")
	a := &sender{result: gateway.SendResult{Outcome: gateway.PermanentlyBlocked}}
	q := gateway.NewQueue(db, a)
	if _, err := q.SendNext(t.Context(), lease.Lease{}, chat, now); !errors.Is(err, lease.ErrInvalid) {
		t.Fatal(err)
	}
	m, err := s.Message(t.Context(), "one")
	if err != nil || m.State != channels.Queued || a.calls != 0 {
		t.Fatal(m, err)
	}
	l := lease.New(func() {})
	defer l.Release()
	if sent, err := q.SendNext(t.Context(), l, chat, now); err != nil || !sent {
		t.Fatal(sent, err)
	}
	b, err := s.Binding(t.Context(), chat)
	if err != nil || !b.Unreachable {
		t.Fatal(b, err)
	}
	if sent, err := q.SendNext(t.Context(), l, chat, now); err != nil || sent || a.calls != 1 {
		t.Fatal("sent after permanent block", sent, err)
	}
	m, err = s.Message(t.Context(), "two")
	if err != nil || m.State != channels.Blocked {
		t.Fatal(m, err)
	}
}

func TestDispatchFailureIsNotReplayAndRebindingDoesNotReadmit(t *testing.T) {
	db, s, id := setup(t)
	calls := 0
	g := gateway.New(db, func(context.Context, gateway.Delivery) error { calls++; return errors.New("dispatch failed") })
	e := event("1", "", "next")
	if err := g.Admit(t.Context(), e); err == nil {
		t.Fatal("expected dispatch error")
	}
	if err := s.Bind(t.Context(), channels.Binding{Address: chat, Agent: id, Person: "owner", Conversation: "new-conversation", Scope: channels.PrivateScope()}); err != nil {
		t.Fatal(err)
	}
	if err := g.Admit(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || scalar(t, db, "SELECT count(*) FROM inbox") != 1 {
		t.Fatal("duplicate dispatched after rebind")
	}
}

func TestDeniedConnectionAndCrossChatReply(t *testing.T) {
	db, s, id := setup(t)
	other := channels.Bot{Platform: "test", Account: "other"}
	if err := s.Connect(t.Context(), other, "owner"); err != nil {
		t.Fatal(err)
	}
	g := gateway.New(db, func(context.Context, gateway.Delivery) error { t.Fatal("unpaired connection dispatched"); return nil })
	e := event("1", "", "next")
	e.Bot = other
	if err := g.Admit(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if scalar(t, db, "SELECT count(*) FROM inbox") != 0 {
		t.Fatal("unpaired inbox")
	}
	g = gateway.New(db, func(context.Context, gateway.Delivery) error { return nil })
	if err := g.Admit(t.Context(), event("1", "", "next")); err != nil {
		t.Fatal(err)
	}
	otherChat := chat
	otherChat.Chat = "other-chat"
	if err := s.Bind(t.Context(), channels.Binding{Address: otherChat, Agent: id, Person: "owner", Conversation: "other", Scope: channels.Scope{Reads: channels.Addressed, Writes: channels.ReplyOnly}}); err != nil {
		t.Fatal(err)
	}
	m := channels.Message{ID: "reply", Address: otherChat, Agent: id, LedgerRef: "ledger", ReplyTo: "1", Parts: []string{"text"}}
	if err := s.Enqueue(t.Context(), m); !errors.Is(err, channels.ErrDenied) {
		t.Fatal("cross-chat reply accepted", err)
	}
	m.ReplyTo = ""
	if err := s.Enqueue(t.Context(), m); !errors.Is(err, channels.ErrDenied) {
		t.Fatal("unsolicited reply accepted", err)
	}
}

func TestCursorWriteFailureRollsBackInbox(t *testing.T) {
	db, _, _ := setup(t)
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER reject_cursor BEFORE UPDATE OF cursor ON channel_connections BEGIN SELECT RAISE(ABORT,'injected cursor failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	g := gateway.New(db, func(context.Context, gateway.Delivery) error { t.Fatal("failed transaction dispatched"); return nil })
	if err := g.Admit(t.Context(), event("1", "", "next")); err == nil {
		t.Fatal("expected cursor failure")
	}
	if scalar(t, db, "SELECT count(*) FROM inbox") != 0 || scalar(t, db, "SELECT count(*) FROM channel_receipts") != 0 {
		t.Fatal("partial transaction")
	}
}

func dbPath(t *testing.T, db *canon.Store) string {
	t.Helper()
	var seq int
	var name, path string
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&seq, &name, &path)
	}); err != nil {
		t.Fatal(err)
	}
	return path
}
