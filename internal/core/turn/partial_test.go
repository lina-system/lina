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
