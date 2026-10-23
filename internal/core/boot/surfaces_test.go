// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func TestSurfaceActionDurableOwnerInputAndRevision(t *testing.T) {
	r, session, agent, _ := managementFixture(t)
	r.agent = agent
	r.conversation = "main:" + agent
	r.ctx = t.Context()
	var err error
	r.topics, err = clientsrv.NewTopics("1", clientsrv.TopicOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.topics.Close() })
	var delivered []ledger.Event
	r.intakeReplay = func(_ context.Context, event ledger.Event) error { delivered = append(delivered, event); return nil }
	session.ProtocolVersion = "1.1"
	session.Capabilities = []protocol.Capability{protocol.SurfaceResultsCapability()}
	raw, err := os.ReadFile("../../../kit/result/testdata/choice.json")
	if err != nil {
		t.Fatal(err)
	}
	store, err := r.resultStore(ledger.Surface{Kind: "tui"})
	if err != nil {
		t.Fatal(err)
	}
	scope := result.Scope{PersonID: r.person, AgentID: agent, ConversationID: r.conversation, TurnID: "surface-turn"}
	card, err := store.Present(t.Context(), scope, result.PresentRequest{Type: result.SurfaceType, Title: "Choose time", Summary: "Choose one.", Body: raw})
	if err != nil {
		t.Fatal(err)
	}
	action := protocol.SurfaceAction{ResultID: card.ID, Revision: "1", Action: "choose", Values: map[string]json.RawMessage{"slot": json.RawMessage(`["wed-1500"]`)}, InputID: "press"}
	call := func(s clientsrv.Session, a protocol.SurfaceAction) error {
		_, err := r.surfaceAction(t.Context(), s, protocol.Envelope{Kind: protocol.KindSurfaceAction, Payload: a})
		return err
	}
	if err := call(session, action); err != nil {
		t.Fatal(err)
	}
	if err := call(session, action); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 || delivered[0].SpeakerKind != guard.Person || delivered[0].Surface.Card != card.ID || !strings.Contains(delivered[0].Body.Text(), `"slot":"wed-1500"`) {
		t.Fatalf("input=%+v", delivered)
	}
	source, err := r.journal().Get(t.Context(), delivered[0].Surface.ReplyTo)
	if err != nil {
		t.Fatal(err)
	}
	var revision result.Result
	if err := json.Unmarshal([]byte(source.Body.Text()), &revision); err != nil || revision.Revision != 1 || revision.ID != card.ID {
		t.Fatalf("action lost its source revision: %+v %v", revision, err)
	}
	if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		var events, inbox int
		if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM conv_event WHERE speaker_kind='person' AND conversation_id=?`, r.conversation).Scan(&events); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM inbox WHERE conversation=?`, r.conversation).Scan(&inbox); err != nil {
			return err
		}
		if events != 1 || inbox != 1 {
			t.Errorf("duplicate admission: events=%d inbox=%d", events, inbox)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]clientsrv.Session{"other person": {PersonID: "other", ProtocolVersion: "1.1", Capabilities: session.Capabilities}, "legacy": {PersonID: r.person, ProtocolVersion: "1.0", Capabilities: session.Capabilities}, "unnegotiated": {PersonID: r.person, ProtocolVersion: "1.1"}} {
		t.Run(name, func(t *testing.T) {
			if err := call(s, action); err == nil {
				t.Fatal("unauthorized action accepted")
			}
		})
	}
	if _, err := store.Present(t.Context(), scope, result.PresentRequest{ID: card.ID, BaseRevision: 1, Type: result.SurfaceType, Title: "Choose time", Summary: "Updated.", Body: raw}); err != nil {
		t.Fatal(err)
	}
	action.InputID = "stale"
	var failure *clientsrv.OperationError
	if err := call(session, action); !errors.As(err, &failure) || failure.Code != protocol.ErrorStaleRevision {
		t.Fatalf("stale=%v", err)
	}
	if len(delivered) != 1 {
		t.Fatal("refused action delivered")
	}
}
