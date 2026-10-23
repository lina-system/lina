// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func (r *conversationRuntime) surfaceAction(ctx context.Context, session clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
	if err := r.authorize(session, nil); err != nil {
		return clientsrv.Result{}, err
	}
	if !clientsrv.SurfaceResultVisible(session) {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorCapabilityNotNegotiated}
	}
	p := e.Payload.(protocol.SurfaceAction)
	revision, err := p.Revision.Int64()
	if err != nil || revision < 1 {
		return clientsrv.Result{}, malformedManagement()
	}
	var agent, conversation, sourceEvent string
	var observedRevision int64
	err = r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT agent,conversation,event_id,revision FROM result_heads WHERE id=? AND person=?`, p.ResultID, session.PersonID).Scan(&agent, &conversation, &sourceEvent, &observedRevision)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return clientsrv.Result{}, outsideManagement()
	}
	if err != nil {
		return clientsrv.Result{}, err
	}
	if observedRevision != revision {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorStaleRevision}
	}
	view := r
	if r.router != nil {
		view, err = r.router.view(ctx, session, agent)
		if err != nil {
			return clientsrv.Result{}, err
		}
	}
	if view.conversation != conversation {
		return clientsrv.Result{}, outsideManagement()
	}
	store, err := view.resultStore(ledger.Surface{Kind: "tui"})
	if err != nil {
		return clientsrv.Result{}, err
	}
	value, err := store.Read(ctx, result.Scope{PersonID: session.PersonID, AgentID: agent, ConversationID: conversation, TurnID: "surface-action"}, result.ReadRequest{ID: p.ResultID})
	if err != nil {
		return clientsrv.Result{}, err
	}
	if int64(value.Revision) != revision {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorStaleRevision}
	}
	if value.Type != result.SurfaceType {
		return clientsrv.Result{}, outsideManagement()
	}
	body, err := result.Validate(value.Type, value.Body, result.Limits{})
	if err != nil {
		return clientsrv.Result{}, err
	}
	values, err := body.(*result.Surface).ValidateAction(p.Action, p.Values)
	if err != nil {
		return clientsrv.Result{}, malformedManagement()
	}
	raw, _ := json.Marshal(values)
	text := value.Title + "\n" + p.Action + ": " + string(raw)
	mode := protocol.InputModeQueue
	inputID := "surface:" + p.InputID
	// The inbox, owner ledger event and revision check commit together. Queue mode
	// deliberately waits for the next turn boundary instead of steering a turn.
	answer, err := view.admitInput(ctx, session, protocol.Input{ConversationID: &conversation, InputID: &inputID, Text: &text, Mode: &mode, Card: &p.ResultID, ReplyTo: &sourceEvent}, func(ctx context.Context, tx *canon.Tx) error {
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM result_heads WHERE id=? AND person=? AND agent=? AND conversation=?`, p.ResultID, session.PersonID, agent, conversation).Scan(&current); err != nil {
			return outsideManagement()
		}
		if current != revision {
			return &clientsrv.OperationError{Code: protocol.ErrorStaleRevision}
		}
		return nil
	})
	return clientsrv.Result{Kind: protocol.KindItemDone, Payload: answer, Effect: protocol.EffectApplied}, err
}
