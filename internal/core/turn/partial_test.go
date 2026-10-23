// SPDX-License-Identifier: Apache-2.0

package turn_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestStoppedPartialAndOutcomeAreAtomic(t *testing.T) {
	t.Parallel()
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[reject], func(t *testing.T) {
			db, cfg := database(t)
			input := inputEvent(t, db)
			if reject {
				if err := db.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER reject_notice BEFORE INSERT ON conv_event
      WHEN NEW.scaffold_kind='interruption' BEGIN SELECT RAISE(ABORT,'notice refused'); END;`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			store := turn.New(db)
			_, err := store.Run(ctx, turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), InputEvent: input,
				Loop: loop.Config{
					Stream: func(context.Context, provider.Request) (provider.Stream, error) {
						return &stream{events: []provider.Event{{Kind: provider.Delta, Index: 2, Delta: "PARTIAL password=synthetic-private"}}}, nil
					},
					Observe: func(_ context.Context, e provider.Event) error {
						if e.Kind == provider.Delta {
							cancel(turn.Interrupt{Issuer: turn.Person})
						}
						return nil
					},
				},
			})
			var interrupted turn.Interrupt
			if !errors.As(err, &interrupted) {
				t.Fatalf("stop cause: %v", err)
			}
			row, err := store.Read(t.Context(), "turn")
			if err != nil {
				t.Fatal(err)
			}
			var partials, notices int
			var text string
			if err := db.Read(t.Context(), func(tx *canon.Tx) error {
				if err := tx.QueryRowContext(t.Context(), `SELECT count(*),coalesce(max(body),'') FROM turn_partials WHERE turn_id='turn'`).Scan(&partials, &text); err != nil {
					return err
				}
				return tx.QueryRowContext(t.Context(), `SELECT count(*) FROM turn_control WHERE turn_id='turn' AND issuer='person' AND notice_event IS NOT NULL`).Scan(&notices)
			}); err != nil {
				t.Fatal(err)
			}
			if reject {
				if row.State != turn.Active || partials != 0 || notices != 0 {
					t.Fatalf("rollback: state=%s partials=%d notices=%d; want active,0,0", row.State, partials, notices)
				}
			} else {
				if row.State != turn.Aborted || partials != 1 || notices != 1 {
					t.Fatalf("commit: state=%s partials=%d notices=%d", row.State, partials, notices)
				}
				if strings.Contains(text, "synthetic-private") {
					t.Fatal("stored partial contains unmasked credential")
				}
			}
			stopped, err := store.Stopped(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			if reject {
				if stopped.NoticeID != "" || len(stopped.Partials) != 0 {
					t.Fatalf("uncommitted stop exposed: %+v", stopped)
				}
				return
			}
			if stopped.TurnID != "turn" || stopped.NoticeID == "" || len(stopped.Partials) != 1 || stopped.Partials[0].Round != 0 || stopped.Partials[0].Index != 2 || stopped.Partials[0].Text != text {
				t.Fatalf("stored stop: %+v", stopped)
			}
			foreign, err := store.Stopped(t.Context(), "unrelated-input")
			if err != nil || foreign.NoticeID != "" || len(foreign.Partials) != 0 {
				t.Fatalf("unrelated input read stop: %+v %v", foreign, err)
			}
			if err := db.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			cfg.Epoch++
			db = open(t, cfg)
			store = turn.New(db)
			for range 2 {
				if err := store.Recover(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			restored, err := store.Stopped(t.Context(), input)
			if err != nil || !reflect.DeepEqual(stopped, restored) {
				t.Fatalf("restart changed stop: %+v %v", restored, err)
			}
		})
	}
}

func TestVisibleCheckpointsSurviveFailedTerminalWrite(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"failure-write-fault", "completion-write-fault", "completed"} {
		t.Run(outcome, func(t *testing.T) {
			db, cfg := database(t)
			input := inputEvent(t, db)
			store := turn.New(db)
			if outcome == "failure-write-fault" {
				if err := db.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER reject_notice BEFORE INSERT ON conv_event
      WHEN NEW.speaker_kind='system' BEGIN SELECT RAISE(ABORT,'notice refused'); END;`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			events := []provider.Event{
				{Kind: provider.Delta, Index: 9, Delta: "first"},
				{Kind: provider.Delta, Index: 3, Delta: "second password=synthetic-private"},
				{Kind: provider.Delta, Index: 9, Delta: " extended"},
			}
			if outcome != "failure-write-fault" {
				events = append(events, provider.Event{Kind: provider.Terminal})
			}
			prefixes := map[int64]string{}
			_, runErr := store.Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), InputEvent: input,
				Complete: func(ctx context.Context, db turn.Canon) error {
					return db.Write(ctx, func(*canon.Tx) error {
						if outcome == "completion-write-fault" {
							return errors.New("reply write refused")
						}
						return nil
					})
				},
				Loop: loop.Config{
					Stream: func(context.Context, provider.Request) (provider.Stream, error) {
						return &stream{events: events}, nil
					},
					Observe: func(ctx context.Context, e provider.Event) error {
						if e.Kind != provider.Delta {
							return nil
						}
						prefixes[e.Index] += e.Delta
						if err := store.CheckpointPartial(ctx, "turn", turn.Partial{Index: e.Index, Text: prefixes[e.Index]}); err != nil {
							return err
						}
						// A checkpoint does not fabricate a terminal outcome.
						stopped, err := store.Stopped(ctx, input)
						if err != nil || stopped.NoticeID != "" || len(stopped.Partials) != 0 {
							t.Fatalf("active checkpoint exposed as a stopped turn: %+v %v", stopped, err)
						}
						return nil
					},
				},
			})
			if (runErr == nil) != (outcome == "completed") {
				t.Fatalf("unexpected terminal result: %v", runErr)
			}
			var count int
			if err := db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM turn_partials WHERE turn_id='turn'").Scan(&count)
			}); err != nil {
				t.Fatal(err)
			}
			if outcome == "completed" {
				if count != 0 {
					t.Fatal("successful completion retained provisional checkpoints")
				}
				if err := store.CheckpointPartial(t.Context(), "turn", turn.Partial{Text: "late"}); !errors.Is(err, turn.ErrAccepted) {
					t.Fatalf("terminal turn accepted a late checkpoint: %v", err)
				}
				return
			}
			if count != 2 {
				t.Fatalf("failed completion lost or duplicated checkpoints: count=%d", count)
			}
			if outcome == "failure-write-fault" {
				if err := db.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), "DROP TRIGGER reject_notice")
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			cfg.Epoch++
			db = open(t, cfg)
			store = turn.New(db)
			for range 2 {
				if err := store.Recover(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			stopped, err := store.Stopped(t.Context(), input)
			if err != nil || stopped.NoticeID == "" || len(stopped.Partials) != 2 {
				t.Fatalf("recovery lost visible checkpoints: %+v %v", stopped, err)
			}
			first, second := stopped.Partials[0], stopped.Partials[1]
			if first.Index != 9 || first.Text != "first extended" || second.Index != 3 || !strings.HasPrefix(second.Text, "second ") || strings.Contains(second.Text, "synthetic-private") {
				t.Fatalf("checkpoint order, content or masking changed: %+v", stopped.Partials)
			}
		})
	}
}
