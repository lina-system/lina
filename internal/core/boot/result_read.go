// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"strconv"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func (r *conversationRuntime) resultOperations() map[protocol.Kind]clientsrv.Operation {
	return map[protocol.Kind]clientsrv.Operation{
		protocol.KindResultGet: {Capability: protocol.Capability{Name: "results", Version: "1.0"}, Handle: func(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
			p, ok := e.Payload.(protocol.ResultGet)
			if !ok {
				return clientsrv.Result{}, malformedManagement()
			}
			value, err := r.readResult(ctx, s, p)
			return clientsrv.Result{Kind: protocol.KindResult, Payload: value}, err
		}},
	}
}

// Resolve identity from the owner's head, never from client-supplied provenance.
// Each page rechecks ownership, agent availability and the current revision.
func (r *conversationRuntime) readResult(ctx context.Context, s clientsrv.Session, p protocol.ResultGet) (protocol.Result, error) {
	if err := r.authorize(s, nil); err != nil {
		return protocol.Result{}, err
	}
	if p.ResultID == "" {
		return protocol.Result{}, malformedManagement()
	}
	var offset int64
	if p.Offset != nil {
		var err error
		offset, err = p.Offset.Int64()
		if err != nil || offset < 0 || offset > 0 && p.Revision == nil {
			return protocol.Result{}, malformedManagement()
		}
	}
	var revision int64
	if p.Revision != nil {
		var err error
		revision, err = p.Revision.Int64()
		if err != nil || revision < 1 {
			return protocol.Result{}, malformedManagement()
		}
	}
	scope := result.Scope{PersonID: s.PersonID, TurnID: "result-read"}
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT agent,conversation FROM result_heads WHERE id=? AND person=?", p.ResultID, s.PersonID).Scan(&scope.AgentID, &scope.ConversationID)
	})
	if err != nil {
		return protocol.Result{}, managementError(err)
	}
	if _, err := r.agents.Get(ctx, scope.AgentID); err != nil {
		return protocol.Result{}, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	}
	store, err := r.resultStore(ledger.Surface{Kind: "tui"})
	if err != nil {
		return protocol.Result{}, managementError(err)
	}
	value, err := store.Read(ctx, scope, result.ReadRequest{ID: p.ResultID})
	if err != nil {
		return protocol.Result{}, managementError(err)
	}
	if revision != 0 && revision != int64(value.Revision) {
		return protocol.Result{}, &clientsrv.OperationError{Code: protocol.ErrorStaleRevision}
	}
	text := value.Summary + "\n" + value.Text
	if offset > int64(len(text)) || offset < int64(len(text)) && !utf8.RuneStart(text[offset]) {
		return protocol.Result{}, malformedManagement()
	}
	end := int(offset) + min(conversationPreviewBytes, len(text)-int(offset))
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	page := text[offset:end]
	response := typedResultWire(value)
	response.Text = &page
	if end < len(text) {
		next := json.Number(strconv.Itoa(end))
		response.Next = &next
	}
	return response, nil
}

func typedResultWire(value result.Result) protocol.Result {
	p := protocol.Result{
		ResultID: value.ID, Revision: json.Number(strconv.Itoa(value.Revision)), Type: protocol.ResultType(value.Type), Status: protocol.ResultStatus(value.Status),
		Title: value.Title, Summary: value.Summary, RevisedBy: value.RevisedBy,
		Provenance: protocol.ResultProvenance{AgentID: value.Provenance.AgentID, ConversationID: value.Provenance.ConversationID, TurnID: value.Provenance.TurnID, TaskID: value.Provenance.TaskID},
	}
	if value.SavedAs != nil {
		p.SavedAs = &protocol.SavedReference{}
	}
	if value.Completion != nil {
		p.Completion = &protocol.CompletionStatus{State: protocol.CompletionState(value.Completion.State), Label: value.Completion.Label}
	}
	return p
}
