// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/results"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func authoredResultWire(value result.Result, summary string) protocol.Result {
	text := value.Summary + "\n" + value.Text
	p := protocol.Result{
		ResultID: value.ID, Revision: json.Number(strconv.Itoa(value.Revision)), Type: protocol.ResultType(value.Type), Status: protocol.ResultStatus(value.Status),
		Title: value.Title, Summary: summary, Text: &text, RevisedBy: value.RevisedBy,
		Provenance: protocol.ResultProvenance{AgentID: value.Provenance.AgentID, ConversationID: value.Provenance.ConversationID, TurnID: value.Provenance.TurnID, TaskID: value.Provenance.TaskID},
	}
	if value.Completion != nil {
		p.Completion = &protocol.CompletionStatus{State: protocol.CompletionState(value.Completion.State), Label: value.Completion.Label}
	}
	return p
}

// Bound each page even under JSON escaping; never split a UTF-8 code point.
func resultTextPage(text string, offset int64) (string, *json.Number, error) {
	if offset < 0 || offset > int64(len(text)) || offset < int64(len(text)) && !utf8.RuneStart(text[offset]) {
		return "", nil, malformedManagement()
	}
	end := min(len(text), int(offset)+conversationPreviewBytes)
	if end == len(text) {
		return text[offset:], nil, nil
	}
	for !utf8.RuneStart(text[end]) {
		end--
	}
	next := json.Number(strconv.Itoa(end))
	return text[offset:end], &next, nil
}

func (r *conversationRuntime) resultOperations() map[protocol.Kind]clientsrv.Operation {
	return map[protocol.Kind]clientsrv.Operation{
		protocol.KindResultGet: {Capability: clientsrv.ConversationCapability, Handle: r.readResultPage},
	}
}

func (r *conversationRuntime) readResultPage(ctx context.Context, session clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
	if err := r.authorize(session, nil); err != nil {
		return clientsrv.Result{}, err
	}
	p := e.Payload.(protocol.ResultGet)
	if p.ConversationID == nil || p.Revision == nil || p.ResultID == "" {
		return clientsrv.Result{}, malformedManagement()
	}
	agent, ok := strings.CutPrefix(*p.ConversationID, "main:")
	if !ok || agent == "" {
		return clientsrv.Result{}, outsideManagement()
	}
	if _, err := r.agents.Get(ctx, agent); err != nil {
		return clientsrv.Result{}, outsideManagement()
	}
	revision, err := p.Revision.Int64()
	if err != nil || revision < 1 {
		return clientsrv.Result{}, malformedManagement()
	}
	var offset int64
	if p.Offset != nil {
		offset, err = p.Offset.Int64()
		if err != nil || offset < 0 {
			return clientsrv.Result{}, malformedManagement()
		}
	}
	store, err := r.resultStore(ledger.Surface{Kind: "tui"})
	if err != nil {
		return clientsrv.Result{}, err
	}
	value, err := store.Read(ctx, result.Scope{PersonID: session.PersonID, AgentID: agent, ConversationID: *p.ConversationID, TurnID: "result-read"}, result.ReadRequest{ID: p.ResultID})
	if errors.Is(err, results.ErrUnavailable) || errors.Is(err, ledger.ErrNotFound) {
		return clientsrv.Result{}, outsideManagement()
	}
	if err != nil {
		return clientsrv.Result{}, err
	}
	if int64(value.Revision) != revision {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorStaleRevision}
	}
	wire := authoredResultWire(value, value.Summary)
	text, next, err := resultTextPage(*wire.Text, offset)
	if err != nil {
		return clientsrv.Result{}, err
	}
	wire.Text, wire.Next = &text, next
	return clientsrv.Result{Kind: protocol.KindResult, Payload: wire}, nil
}
