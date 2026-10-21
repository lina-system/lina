// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/provider"
)

func passDB(t *testing.T) (*canon.Store, canon.Config) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	cfg := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()}
	db, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db, cfg
}
func TestContinuationLedgerSurvivesOversizeWithoutCopyingInput(t *testing.T) {
	db, _ := passDB(t)
	events := ledger.New(db, nil)
	now := time.Unix(100, 0)
	input, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "input", ConversationID: "conversation", PersonID: "owner", AgentID: "agent", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "original question"})
	if err != nil {
		t.Fatal(err)
	}
	journal := ContinuationLedger{DB: db, Agent: "agent", Conversation: "conversation", EventID: "continuation", SizesID: "sizes", At: now, End: func(ctx context.Context, tx *canon.Tx, turn string) error {
		if turn != "ended" {
			return errors.New("wrong turn")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO ended_turn(id) VALUES(?)`, turn)
		return err
	}}
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TABLE ended_turn(id TEXT PRIMARY KEY)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p, v := fixturePass()
	v.Input.Item = message(strings.Repeat("q", 2200))
	p.Summarize = func(context.Context, Segment) (Text, error) {
		return Text{Requests: "open", Topics: "topic", Actions: "none", Values: "42", Omitted: "details"}, nil
	}
	p.Publish = func(context.Context, Segment, Text) (Published, error) {
		return Published{ID: "root", Item: message("summary")}, nil
	}
	_, err = p.Continue(t.Context(), v, Continuation{EndedTurn: "ended", InputID: input.ID, Record: journal.Record, Log: journal.Log, Packet: func(context.Context) ([]json.RawMessage, error) { return v.Packet, nil }, Send: func(context.Context, provider.Request) error { return errors.New("must not send") }})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	event, err := events.Get(t.Context(), "continuation")
	if err != nil {
		t.Fatal(err)
	}
	if event.SpeakerKind != guard.System || event.ParentID != "input" || strings.Contains(event.Body.Text(), input.Body.Text()) {
		t.Fatalf("copied input or lost origin: %+v", event)
	}
	sizes, err := events.Get(t.Context(), "sizes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sizes.Body.Text(), "Threshold") {
		t.Fatalf("sizes absent: %s", sizes.Body.Text())
	}
	var ended int
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), `SELECT count(*) FROM ended_turn`).Scan(&ended)
	}); err != nil || ended != 1 {
		t.Fatalf("ended=%d err=%v", ended, err)
	}
}
