// SPDX-License-Identifier: Apache-2.0
package turn

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

// EvidenceWriter binds the preparation callbacks to the native conversation
// ledger. Its input types have no request or ContextPacket body field.
// The caller supplies the conversation's trusted surface and prior event id.
type EvidenceWriter struct {
	Ledger                   *ledger.Store
	ConversationID, ParentID string
	Surface                  ledger.Surface
	Now                      func() time.Time
}

func (w EvidenceWriter) append(ctx context.Context, id, parent, agent, kind string, value any) error {
	if w.Ledger == nil || w.Now == nil {
		return ErrInvalid
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	now := w.Now()
	_, err = w.Ledger.Admit(ctx, guard.EntrySystem, ledger.Input{
		ID: id, ConversationID: w.ConversationID, ParentID: parent, AgentID: agent,
		Surface: w.Surface, ScaffoldKind: kind, OccurredAt: now, RecordedAt: now,
		TimeQuality: "observed", Body: string(body),
	})
	return err
}
func (w EvidenceWriter) Acknowledge(ctx context.Context, a Acknowledgement) error {
	if a.TurnID == "" || a.PolicyRevision == "" || a.Assembly.PersonaRevision == "" {
		return ErrInvalid
	}
	return w.append(ctx, "prepare:"+a.TurnID, w.ParentID, a.Assembly.AgentID, "prepare_ack", a)
}
func (w EvidenceWriter) RecordRequest(ctx context.Context, r RequestEvidence) error {
	if r.TurnID == "" || r.Round < 0 {
		return ErrInvalid
	}
	return w.append(ctx, "request:"+r.TurnID+":"+strconv.Itoa(r.Round), "prepare:"+r.TurnID, r.Assembly.AgentID, "request_assembly", r)
}
