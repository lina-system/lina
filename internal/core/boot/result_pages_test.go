// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
	"github.com/thisisjun786/lina/kit/rpc"
)

func TestResultPagesPreserveUnicodeWithinFrameLimit(t *testing.T) {
	// Include worst-case JSON escaping and split across multibyte boundaries.
	want := strings.Repeat("\x00결과<&>", 100000)
	var got strings.Builder
	var offset int64
	for {
		page, next, err := resultTextPage(want, offset)
		if err != nil || !utf8.ValidString(page) {
			t.Fatalf("invalid page at %d: %v", offset, err)
		}
		encoded, err := json.Marshal(protocol.Result{Text: &page})
		if err != nil || len(encoded) >= rpc.MaxFrameBytes/2 {
			t.Fatalf("page leaves no envelope room: %d %v", len(encoded), err)
		}
		got.WriteString(page)
		if next == nil {
			break
		}
		offset, err = next.Int64()
		if err != nil || offset != int64(got.Len()) {
			t.Fatal("invalid continuation")
		}
	}
	if got.String() != want {
		t.Fatal("lost or repeated result content")
	}
	for _, offset := range []int64{-1, 2, int64(len(want)) + 1} {
		if _, _, err := resultTextPage(want, offset); err == nil {
			t.Fatalf("accepted invalid byte offset %d", offset)
		}
	}
}

func TestResultPageScopeAndRevision(t *testing.T) {
	r, session, agent, otherAgent := managementFixture(t)
	store, err := r.resultStore(ledger.Surface{Kind: "tui"})
	if err != nil {
		t.Fatal(err)
	}
	conversation := "main:" + agent
	scope := result.Scope{PersonID: r.person, AgentID: agent, ConversationID: conversation, TurnID: "turn"}
	proposal := result.PresentRequest{Type: result.DraftType, Title: "Result", Summary: "Ready.", Body: json.RawMessage(`{"blocks":[{"kind":"paragraph","text":"Content"}],"from_summary":null}`)}
	card, err := store.Present(t.Context(), scope, proposal)
	if err != nil {
		t.Fatal(err)
	}
	revision := json.Number("1")
	request := protocol.ResultGet{ResultID: card.ID, Revision: &revision, ConversationID: &conversation}
	read := func(s clientsrv.Session, p protocol.ResultGet, want protocol.ErrorCode) {
		t.Helper()
		got, err := r.readResultPage(t.Context(), s, protocol.Envelope{Payload: p})
		if want == "" {
			if err != nil || got.Kind != protocol.KindResult || *got.Payload.(protocol.Result).Text != "Ready.\nContent" {
				t.Fatalf("read=%+v err=%v", got, err)
			}
			return
		}
		var refusal *clientsrv.OperationError
		if !errors.As(err, &refusal) || refusal.Code != want {
			t.Fatalf("want %s, got %v", want, err)
		}
	}
	read(session, request, "")
	read(clientsrv.Session{PersonID: "other"}, request, protocol.ErrorNotAuthorized)
	foreign := request
	otherConversation := "main:" + otherAgent
	foreign.ConversationID = &otherConversation
	read(session, foreign, protocol.ErrorOutOfScope)
	missing := request
	missing.Revision = nil
	read(session, missing, protocol.ErrorMalformed)
	invalid := request
	offset := json.Number("9999")
	invalid.Offset = &offset
	read(session, invalid, protocol.ErrorMalformed)
	proposal.ID, proposal.BaseRevision = card.ID, 1
	if _, err := store.Present(t.Context(), scope, proposal); err != nil {
		t.Fatal(err)
	}
	read(session, request, protocol.ErrorStaleRevision)
}
