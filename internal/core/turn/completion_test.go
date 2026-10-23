// SPDX-License-Identifier: Apache-2.0

package turn_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// Stop at the first durable completed state, before Run can return to its
// caller. os.Exit bypasses all deferred cleanup, including reply publication.
type completionCrash struct{ *canon.Store }

func (db completionCrash) Write(ctx context.Context, fn func(*canon.Tx) error) error {
	if err := db.Store.Write(ctx, fn); err != nil {
		return err
	}
	var completed int
	if err := db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM turn_runs WHERE state='completed'").Scan(&completed)
	}); err != nil {
		return err
	}
	if completed != 0 {
		os.Exit(73)
	}
	return nil
}

func completionReply() ledger.Admission {
	now := time.Now().UTC()
	return ledger.Admission{Entry: guard.EntryAgent, Input: ledger.Input{
		ID: "turn:1:0", ConversationID: "conversation", ParentID: "input-event", AgentID: "agent",
		Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "durable reply",
	}}
}

func TestCrashAfterTurnCompletionPreservesReply(t *testing.T) {
	t.Parallel()
	if root := os.Getenv("LINA_COMPLETION_CRASH_ROOT"); root != "" {
		db := completionCrash{restartDB(t, root, 1)}
		input := inputEvent(t, db.Store)
		_, err := turn.New(db).Run(t.Context(), turn.Options{
			ID: "turn", Conversation: "conversation", InboxID: admit(t, db.Store), InputEvent: input,
			Complete: func(ctx context.Context, db turn.Canon) error {
				_, err := ledger.New(db, nil).AdmitBatch(ctx, []ledger.Admission{completionReply()})
				return err
			},
			Loop: loop.Config{Stream: func(context.Context, provider.Request) (provider.Stream, error) {
				return &stream{events: []provider.Event{
					{Kind: provider.ItemDone, Item: provider.Item(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"durable reply"}]}`)},
					{Kind: provider.Terminal},
				}}, nil
			}},
		})
		t.Fatalf("completion did not stop process: %v", err)
	}
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, "-test.run=^TestCrashAfterTurnCompletionPreservesReply$")
	child.Env = []string{"LINA_COMPLETION_CRASH_ROOT=" + root}
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("child: %v %s", err, output)
	}
	db := restartDB(t, root, 2)
	store := turn.New(db)
	for range 2 {
		if err = store.Recover(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	row, err := store.Read(t.Context(), "turn")
	if err != nil || row.State != turn.Completed || len(row.Items) != 1 {
		t.Fatalf("completed turn evidence: %+v %v", row, err)
	}
	var replies int
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM conv_event WHERE parent_id='input-event' AND speaker_kind='agent'").Scan(&replies)
	}); err != nil {
		t.Fatal(err)
	}
	if replies != 1 {
		t.Fatalf("after restart: completed turn has %d replies in conversation history; want exactly 1", replies)
	}
	event, err := ledger.New(db, nil).Get(t.Context(), "turn:1:0")
	if err != nil || event.Body.Text() != "durable reply" {
		t.Fatalf("reply: %+v %v", event, err)
	}
	_, err = store.Run(t.Context(), turn.Options{ID: "retry", Conversation: "conversation", InboxID: row.InboxID,
		Loop: loop.Config{Stream: func(context.Context, provider.Request) (provider.Stream, error) {
			t.Fatal("replayed completed turn")
			return nil, nil
		}},
	})
	if !errors.Is(err, turn.ErrAccepted) {
		t.Fatalf("retry: %v", err)
	}
}

func TestLateCancellationPreservesCompletedReply(t *testing.T) {
	t.Parallel()
	db, cfg := database(t)
	input := inputEvent(t, db)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	complete := func(save context.Context, db turn.Canon) error {
		_, err := ledger.New(db, nil).AdmitBatch(save, []ledger.Admission{completionReply()})
		return err
	}
	calls := 0
	opts := turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), InputEvent: input,
		Complete: func(save context.Context, db turn.Canon) error {
			// This boundary runs only after the loop succeeds. Inject the late
			// cancellation before invoking completion or admitting any reply.
			cancel()
			return complete(save, db)
		},
		Loop: loop.Config{Stream: func(context.Context, provider.Request) (provider.Stream, error) {
			calls++
			return &stream{events: []provider.Event{
				{Kind: provider.ItemDone, Item: provider.Item(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"durable reply"}]}`)},
				{Kind: provider.Terminal},
			}}, nil
		}},
	}
	if _, err := turn.New(db).Run(ctx, opts); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		row, err := turn.New(db).Read(t.Context(), "turn")
		if err != nil {
			t.Fatal(err)
		}
		var replies, interruptions int
		if err = db.Read(t.Context(), func(tx *canon.Tx) error {
			return tx.QueryRowContext(t.Context(), `SELECT
 (SELECT count(*) FROM conv_event WHERE parent_id='input-event' AND speaker_kind='agent'),
 (SELECT count(*) FROM conv_event WHERE parent_id='input-event' AND scaffold_kind='interruption')`).Scan(&replies, &interruptions)
		}); err != nil {
			t.Fatal(err)
		}
		if row.State != turn.Completed || len(row.Items) != 1 || replies != 1 || interruptions != 0 {
			t.Fatalf("late cancellation: state=%s items=%d replies=%d interruptions=%d; want completed, 1, 1, 0", row.State, len(row.Items), replies, interruptions)
		}
		event, err := ledger.New(db, nil).Get(t.Context(), "turn:1:0")
		if err != nil || event.Body.Text() != "durable reply" {
			t.Fatalf("reply: %+v %v", event, err)
		}
	}
	check()
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	db = open(t, cfg)
	for range 2 {
		if err := turn.New(db).Recover(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := turn.New(db).Run(t.Context(), opts); !errors.Is(err, turn.ErrAccepted) || calls != 1 {
		t.Fatalf("replayed completed turn: calls=%d err=%v", calls, err)
	}
	check()
}

func TestCompletionAndRepliesRollbackTogether(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"completion", "second_reply"} {
		t.Run(failure, func(t *testing.T) {
			db, _ := database(t)
			input := inputEvent(t, db)
			if failure == "second_reply" {
				if err := db.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER reject_reply BEFORE INSERT ON conv_event
 WHEN NEW.id='turn:1:1' BEGIN SELECT RAISE(ABORT,'reply refused'); END;`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			store := turn.New(db)
			first, second := completionReply(), completionReply()
			second.Input.ID = "turn:1:1"
			var saved []ledger.Event
			calls := 0
			opts := turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), InputEvent: input,
				Complete: func(ctx context.Context, db turn.Canon) error {
					if failure == "completion" {
						return db.Write(ctx, func(*canon.Tx) error { return errors.New("completion refused") })
					}
					var err error
					saved, err = ledger.New(db, nil).AdmitBatch(ctx, []ledger.Admission{first, second})
					return err
				},
				Loop: loop.Config{Stream: func(context.Context, provider.Request) (provider.Stream, error) {
					calls++
					return &stream{events: []provider.Event{{Kind: provider.Terminal}}}, nil
				}},
			}
			if _, err := store.Run(t.Context(), opts); err == nil || len(saved) != 0 {
				t.Fatalf("failed transaction acknowledged replies: %v %v", saved, err)
			}
			row, err := store.Read(t.Context(), "turn")
			if err != nil || row.State != turn.Active {
				t.Fatalf("completion escaped rollback: %+v %v", row, err)
			}
			for _, reply := range []ledger.Admission{first, second} {
				if _, err := ledger.New(db, nil).Get(t.Context(), reply.Input.ID); !errors.Is(err, ledger.ErrNotFound) {
					t.Fatalf("reply escaped rollback: %v", err)
				}
			}
			if _, err := store.Run(t.Context(), opts); !errors.Is(err, turn.ErrAccepted) || calls != 1 {
				t.Fatalf("failed completion replayed: calls=%d err=%v", calls, err)
			}
			if err := store.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			row, err = store.Read(t.Context(), "turn")
			if err != nil || row.State != turn.Aborted {
				t.Fatalf("failed completion recovery: %+v %v", row, err)
			}
		})
	}
}

func TestUnsuccessfulTurnsSkipCompletion(t *testing.T) {
	t.Parallel()
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "interrupted"}[interrupted], func(t *testing.T) {
			db, _ := database(t)
			store := turn.New(db)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			_, err := store.Run(ctx, turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db),
				Complete: func(context.Context, turn.Canon) error {
					t.Fatal("unsuccessful turn admitted a reply")
					return nil
				},
				Loop: loop.Config{Stream: func(context.Context, provider.Request) (provider.Stream, error) {
					if interrupted {
						cancel()
					}
					return nil, errors.New("provider failed")
				}},
			})
			if err == nil {
				t.Fatal("unsuccessful turn returned success")
			}
			want := turn.Failed
			if interrupted {
				want = turn.Aborted
			}
			row, err := store.Read(t.Context(), "turn")
			if err != nil || row.State != want {
				t.Fatalf("state=%s want=%s err=%v", row.State, want, err)
			}
		})
	}
}

func TestCompletionPreparationHasNoDeadline(t *testing.T) {
	t.Parallel()
	db, _ := database(t)
	input := inputEvent(t, db)
	// Assert the deadline boundary directly, without waiting for a slow disk or
	// relying on a scheduler to finish before a timeout.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	_, err := turn.New(db).Run(ctx, turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), InputEvent: input,
		Complete: func(preparation context.Context, db turn.Canon) error {
			if _, ok := preparation.Deadline(); ok {
				t.Error("reply preparation inherited a deadline")
			}
			_, err := ledger.New(db, nil).AdmitBatch(preparation, []ledger.Admission{completionReply()})
			return err
		},
		Loop: loop.Config{Stream: func(context.Context, provider.Request) (provider.Stream, error) {
			return &stream{events: []provider.Event{{Kind: provider.Terminal}}}, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
}
