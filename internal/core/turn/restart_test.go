// SPDX-License-Identifier: Apache-2.0
package turn_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func restartDB(t *testing.T, root string, epoch uint64) *canon.Store {
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
	for _, schema := range []string{turn.Schema, turn.ControlSchema, turn.OutcomeSchema} {
		ms = append(ms, canon.Migration{Version: len(ms) + 1, SQL: schema})
	}
	return open(t, canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: ms})
}

func inputEvent(t *testing.T, db *canon.Store) string {
	t.Helper()
	now := time.Now().UTC()
	e, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "input-event", ConversationID: "conversation", PersonID: "person", AgentID: "agent", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "original input"})
	if err != nil {
		t.Fatal(err)
	}
	return e.ID
}

// The parent waits for a committed output item, then sends SIGKILL. No cleanup
// from Store.Run or a graceful process exit can manufacture the recovery proof.
func TestKillDuringTurnRecordsOneLineWithoutReplay(t *testing.T) {
	if root := os.Getenv("LINA_STEERING_KILL_ROOT"); root != "" {
		db := restartDB(t, root, 1)
		event := inputEvent(t, db)
		_, err := turn.New(db).Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), InputEvent: event, Loop: loop.Config{
			Stream: func(context.Context, provider.Request) (provider.Stream, error) {
				return reviewResponse(provider.ToolCall{ID: "open", Name: "read", Arguments: `{}`}), nil
			},
			Observe: func(_ context.Context, e provider.Event) error {
				if e.Kind == provider.ItemDone {
					os.Stdout.WriteString("committed\n")
					// A pipe, rather than time, holds the process at the exact boundary.
					var b [1]byte
					_, err := os.Stdin.Read(b[:])
					return err
				}
				return nil
			},
		}})
		t.Fatalf("child resumed: %v", err)
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
	child := exec.CommandContext(ctx, binary, "-test.run=^TestKillDuringTurnRecordsOneLineWithoutReplay$")
	child.Env = []string{"LINA_STEERING_KILL_ROOT=" + root}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	var stderr bytes.Buffer
	child.Stderr = &stderr
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			child.Process.Kill()
			child.Wait()
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "committed\n" {
		t.Fatalf("readiness=%q err=%v stderr=%s", line, err, stderr.String())
	}
	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = child.Wait()
	waited = true
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != -1 {
		t.Fatalf("expected kill: %v", err)
	}
	db := restartDB(t, root, 2)
	store := turn.New(db)
	for range 2 {
		if err = store.Recover(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	row, err := store.Read(t.Context(), "turn")
	if err != nil || row.State != turn.Aborted || len(row.Results) != 1 || row.Results[0].Code != loop.Aborted {
		t.Fatalf("row=%+v err=%v", row, err)
	}
	calls := 0
	_, err = store.Run(t.Context(), turn.Options{ID: "replacement", Conversation: "conversation", InboxID: admit(t, db), Loop: loop.Config{Stream: func(context.Context, provider.Request) (provider.Stream, error) {
		calls++
		return reviewResponse(), nil
	}}})
	if !errors.Is(err, turn.ErrAccepted) || calls != 0 {
		t.Fatalf("replayed: calls=%d err=%v", calls, err)
	}
	var noticeID string
	var notices int
	err = db.Read(t.Context(), func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM conv_event WHERE parent_id='input-event' AND speaker_kind='system'").Scan(&notices); err != nil {
			return err
		}
		return tx.QueryRowContext(t.Context(), "SELECT id FROM conv_event WHERE parent_id='input-event' AND speaker_kind='system'").Scan(&noticeID)
	})
	if err != nil || notices != 1 {
		t.Fatalf("notices=%d err=%v", notices, err)
	}
	event, err := ledger.New(db, nil).Get(t.Context(), noticeID)
	if err != nil || strings.TrimSpace(event.Body.Text()) == "" || strings.ContainsAny(event.Body.Text(), "\r\n") || event.ConversationID != "conversation" {
		t.Fatalf("notice=%+v err=%v", event, err)
	}
}

func TestRecoveryNoticeAndTerminalStateCommitTogether(t *testing.T) {
	db, _ := database(t)
	eventID := inputEvent(t, db)
	store := turn.New(db)
	// Preparation has committed when the simulated process disappears.
	_, err := store.Run(t.Context(), turn.Options{ID: "completed", Conversation: "conversation", InboxID: admit(t, db), InputEvent: eventID, Loop: loop.Config{Stream: func(context.Context, provider.Request) (provider.Stream, error) { return reviewResponse(), nil }}})
	if err != nil {
		t.Fatal(err)
	}
	// Create an independent accepted turn to recover. This is a storage fixture,
	// not a second admission of the completed input.
	err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO inbox(conversation,source,native_id,body) VALUES('conversation','client','interrupted','input');
INSERT INTO turn_runs(id,conversation,inbox_id,state) VALUES('interrupted','conversation',last_insert_rowid(),'active');
INSERT INTO turn_control(turn_id,input_event) VALUES('interrupted','input-event');
CREATE TRIGGER reject_notice BEFORE INSERT ON conv_event WHEN NEW.speaker_kind='system' BEGIN SELECT RAISE(ABORT,'notice refused'); END;`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Recover(t.Context()); err == nil {
		t.Fatal("recovery succeeded without its notice")
	}
	row, err := store.Read(t.Context(), "interrupted")
	if err != nil || row.State != turn.Active {
		t.Fatalf("state committed without notice: %+v %v", row, err)
	}
	err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DROP TRIGGER reject_notice")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = store.Recover(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	var total int
	err = db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM conv_event WHERE speaker_kind='system'").Scan(&total)
	})
	if err != nil || total != 1 {
		t.Fatalf("notice total=%d err=%v", total, err)
	}
	row, err = store.Read(t.Context(), "completed")
	if err != nil || row.State != turn.Completed {
		t.Fatalf("completed turn changed: %+v %v", row, err)
	}
}
