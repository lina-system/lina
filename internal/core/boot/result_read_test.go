// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func TestTypedResultReadPagesAndRefusals(t *testing.T) {
	r, session, agent, _ := managementFixture(t)
	store, err := r.resultStore(ledger.Surface{Kind: "tui"})
	if err != nil {
		t.Fatal(err)
	}
	scope := result.Scope{PersonID: r.person, AgentID: agent, ConversationID: "main:" + agent, TurnID: "turn"}
	body, _ := json.Marshal(map[string]any{"blocks": []any{map[string]any{"kind": "paragraph", "text": strings.Repeat("한글 <>&\n", 20000) + "END"}}, "from_summary": nil})
	request := result.PresentRequest{Type: result.DraftType, Title: "Long", Summary: "Ready.", Body: body}
	card, err := store.Present(t.Context(), scope, request)
	if err != nil {
		t.Fatal(err)
	}
	value, err := store.Read(t.Context(), scope, result.ReadRequest{ID: card.ID})
	if err != nil {
		t.Fatal(err)
	}
	p := protocol.ResultGet{ResultID: card.ID}
	var full strings.Builder
	pages := 0
	for {
		page, err := r.readResult(t.Context(), session, p)
		if err != nil {
			t.Fatal(err)
		}
		if page.Text == nil || !utf8.ValidString(*page.Text) || len(*page.Text) > 64<<10 {
			t.Fatal("invalid or unbounded page")
		}
		raw, err := json.Marshal(page)
		if err != nil || len(raw) > 1<<20 {
			t.Fatal("page exceeds transport bound", err)
		}
		full.WriteString(*page.Text)
		pages++
		if page.Next == nil {
			break
		}
		p.Offset, p.Revision = page.Next, &page.Revision
	}
	if pages < 3 || full.String() != value.Summary+"\n"+value.Text {
		t.Fatalf("incomplete result: %d pages", pages)
	}
	check := func(s clientsrv.Session, p protocol.ResultGet, want protocol.ErrorCode) {
		t.Helper()
		_, err := r.readResult(t.Context(), s, p)
		var refusal *clientsrv.OperationError
		if !errors.As(err, &refusal) || refusal.Code != want {
			t.Fatalf("want %s: %v", want, err)
		}
	}
	one, huge, negative, split := json.Number("1"), json.Number("9223372036854775807"), json.Number("-1"), json.Number("8")
	check(clientsrv.Session{PersonID: "other"}, p, protocol.ErrorNotAuthorized)
	check(session, protocol.ResultGet{ResultID: "missing"}, protocol.ErrorUnavailable)
	for _, offset := range []*json.Number{&huge, &negative, &split} {
		check(session, protocol.ResultGet{ResultID: card.ID, Revision: &one, Offset: offset}, protocol.ErrorMalformed)
	}
	check(session, protocol.ResultGet{ResultID: card.ID, Offset: &one}, protocol.ErrorMalformed)
	request.ID, request.BaseRevision = card.ID, 1
	if _, err := store.Present(t.Context(), scope, request); err != nil {
		t.Fatal(err)
	}
	check(session, p, protocol.ErrorStaleRevision)
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE result_heads SET person='other' WHERE id=?", card.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check(session, protocol.ResultGet{ResultID: card.ID}, protocol.ErrorUnavailable)
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DELETE FROM result_heads WHERE id=?", card.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	check(session, protocol.ResultGet{ResultID: card.ID}, protocol.ErrorUnavailable)
}
