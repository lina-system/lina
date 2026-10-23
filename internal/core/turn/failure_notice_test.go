// SPDX-License-Identifier: Apache-2.0

package turn_test

import (
	"context"
	"errors"
	"io"
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
	t.Parallel()
	for _, tc := range []struct {
		name                  string
		refuseNotice, partial bool
	}{
		{"unavailable", false, false}, {"unavailable-write-fault", true, false},
		{"truncated", false, true}, {"truncated-write-fault", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, cfg := database(t)
			event := inputEvent(t, db)
			if tc.refuseNotice {
				if err := db.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER reject_notice BEFORE INSERT ON conv_event WHEN NEW.speaker_kind='system' BEGIN SELECT RAISE(ABORT,'notice refused'); END`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			failure := errors.New("synthetic provider diagnostic must stay private")
			store := turn.New(db)
			wantErr := failure
			if tc.partial {
				wantErr = io.EOF
			}
			_, err := store.Run(t.Context(), turn.Options{ID: "failed", Conversation: "conversation", InboxID: admit(t, db), InputEvent: event, Loop: loop.Config{
				Stream: func(context.Context, provider.Request) (provider.Stream, error) {
					if tc.partial {
						return &stream{events: []provider.Event{{Kind: provider.Delta, Index: 2, Delta: "PARTIAL password=synthetic-private"}}}, nil
					}
					return nil, failure
				},
			}})
			if !errors.Is(err, wantErr) {
				t.Fatal(err)
			}
			row, err := store.Read(t.Context(), "failed")
			want := turn.Failed
			if tc.refuseNotice {
				want = turn.Active
			}
			if err != nil || row.State != want {
				t.Fatalf("terminal state=%s want=%s err=%v", row.State, want, err)
			}
			var partialCount int
			if err := db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM turn_partials WHERE turn_id='failed'").Scan(&partialCount)
			}); err != nil {
				t.Fatal(err)
			}
			wantPartials := 0
			if tc.partial && !tc.refuseNotice {
				wantPartials = 1
			}
			if partialCount != wantPartials {
				t.Fatalf("partial count=%d want=%d; outcome must commit atomically", partialCount, wantPartials)
			}
			if tc.refuseNotice {
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
			stopped, err := turn.New(db).Stopped(t.Context(), event)
			if err != nil || stopped.NoticeID != noticeID || len(stopped.Partials) != wantPartials {
				t.Fatalf("stored failure outcome=%+v err=%v", stopped, err)
			}
			if wantPartials == 1 {
				p := stopped.Partials[0]
				if p.Round != 0 || p.Index != 2 || !strings.Contains(p.Text, "PARTIAL") || strings.Contains(p.Text, "synthetic-private") {
					t.Fatalf("partial identity or masking lost: %+v", p)
				}
			}
		})
	}
}

// A provider authentication refusal mid-turn is a state of the account: the
// durable notice asks the person to sign in instead of reporting a lost reply.
func TestSignInRefusalNotice(t *testing.T) {
	t.Parallel()
	db, _ := database(t)
	event := inputEvent(t, db)
	store := turn.New(db)
	_, err := store.Run(t.Context(), turn.Options{ID: "signed-out", Conversation: "conversation", InboxID: admit(t, db), InputEvent: event, Loop: loop.Config{
		Stream: func(context.Context, provider.Request) (provider.Stream, error) {
			return nil, &provider.Failure{Kind: provider.NeedsSignIn}
		},
	}})
	var failure *provider.Failure
	if !errors.As(err, &failure) || failure.Kind != provider.NeedsSignIn {
		t.Fatalf("failure %v", err)
	}
	var noticeID string
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT notice_event FROM turn_control WHERE turn_id='signed-out'").Scan(&noticeID)
	}); err != nil {
		t.Fatal(err)
	}
	notice, err := ledger.New(db, nil).Get(t.Context(), noticeID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice.Body.Text(), "Sign in") {
		t.Fatalf("notice does not ask for sign-in: %q", notice.Body.Text())
	}
}
