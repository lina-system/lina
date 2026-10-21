// SPDX-License-Identifier: Apache-2.0

package turn_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestFailedTurnNoticeCommitsWithState(t *testing.T) {
	for _, refuseNotice := range []bool{false, true} {
		name := "committed"
		if refuseNotice {
			name = "write-fault"
		}
		t.Run(name, func(t *testing.T) {
			db, cfg := database(t)
			event := inputEvent(t, db)
			if refuseNotice {
				if err := db.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER reject_notice BEFORE INSERT ON conv_event WHEN NEW.speaker_kind='system' BEGIN SELECT RAISE(ABORT,'notice refused'); END`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			failure := errors.New("synthetic provider diagnostic must stay private")
			store := turn.New(db)
			_, err := store.Run(t.Context(), turn.Options{ID: "failed", Conversation: "conversation", InboxID: admit(t, db), InputEvent: event, Loop: loop.Config{
				Stream: func(context.Context, provider.Request) (provider.Stream, error) { return nil, failure },
			}})
			if !errors.Is(err, failure) {
				t.Fatal(err)
			}
			row, err := store.Read(t.Context(), "failed")
			want := turn.Failed
			if refuseNotice {
				want = turn.Active
			}
			if err != nil || row.State != want {
				t.Fatalf("terminal state=%s want=%s err=%v", row.State, want, err)
			}
			if refuseNotice {
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
			for range 2 {
				if err := turn.New(db).Recover(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			var count int
			var noticeID string
			if err := db.Read(t.Context(), func(tx *canon.Tx) error {
				if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM conv_event WHERE speaker_kind='system' AND parent_id=?", event).Scan(&count); err != nil {
					return err
				}
				return tx.QueryRowContext(t.Context(), "SELECT notice_event FROM turn_control WHERE turn_id='failed'").Scan(&noticeID)
			}); err != nil {
				t.Fatalf("durable notice unavailable: count=%d err=%v", count, err)
			}
			notice, err := ledger.New(db, nil).Get(t.Context(), noticeID)
			if err != nil || count != 1 || notice.SpeakerKind != guard.System || notice.ParentID != event || notice.Body.Text() == "" || strings.Contains(notice.Body.Text(), failure.Error()) {
				t.Fatalf("invalid durable failure outcome: count=%d notice=%+v err=%v", count, notice, err)
			}
		})
	}
}
