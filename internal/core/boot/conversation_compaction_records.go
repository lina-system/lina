// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (s *turnState) recordCompaction(ctx context.Context, kind string, value any) error {
	body, err := json.Marshal(struct {
		Kind  string `json:"kind"`
		Sizes any    `json:"sizes"`
	}{kind, value})
	if err != nil {
		return err
	}
	r := s.runtime
	now := time.Now()
	_, err = r.journal().Admit(ctx, guard.EntrySystem, ledger.Input{ID: "compaction:" + rand.Text(), ParentID: s.input.ID, AgentID: r.agent, ConversationID: r.conversation, Surface: s.input.Surface, ScaffoldKind: "compaction_recorded", OccurredAt: now, RecordedAt: now, TimeQuality: "recorded", Body: string(body)})
	return err
}

func (s *turnState) recordCompactionContinuation(ctx context.Context, db ledger.Canon) error {
	r := s.runtime
	journal := compaction.ContinuationLedger{DB: db, Agent: r.agent, Conversation: r.conversation, EventID: "continuation:" + s.turnID, SizesID: "continuation-sizes:" + s.turnID, At: time.Now(), End: func(ctx context.Context, tx *canon.Tx, id string) error {
		var conversation, input string
		var state turn.State
		if err := tx.QueryRowContext(ctx, `SELECT r.conversation,r.state,c.input_event FROM turn_runs r JOIN turn_control c ON c.turn_id=r.id WHERE r.id=?`, id).Scan(&conversation, &state, &input); err != nil {
			return err
		}
		if conversation != r.conversation || input != s.input.ID || state != turn.Active {
			return turn.ErrInvalid
		}
		_, err := tx.ExecContext(ctx, "UPDATE turn_runs SET state=? WHERE id=?", turn.Failed, id)
		return err
	}}
	if err := journal.Record(ctx, compaction.ContinuationRecord{EndedTurn: s.turnID, InputID: s.input.ID}); err != nil {
		return err
	}
	sizes, err := s.compaction.session.Sizes()
	if err != nil {
		return err
	}
	// The ordered-prefix contract cannot cover current-turn items after a
	// retained input. Keep the ended turn's journal and lineage; never drop its
	// input or send an altered/incomplete reasoning round trip to force a fit.
	return journal.Log(ctx, sizes)
}

// Live delivery and history restoration project the same durable diagnostic.
// Its summary stays one line; Details contains only measured sizes, no content.
func (r *conversationRuntime) publishCompactionFailure(ctx context.Context, notice ledger.Event, turnID string) (bool, error) {
	e, err := r.journal().Get(ctx, "continuation-sizes:"+turnID)
	if errors.Is(err, ledger.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var record struct {
		Sizes compaction.Sizes `json:"sizes"`
	}
	if err := json.Unmarshal([]byte(e.Body.Text()), &record); err != nil {
		return false, err
	}
	s := record.Sizes
	text := fmt.Sprintf("Request: %d\nThreshold: %d\nContext window: %d\nFixed: %d\nSummary: %d\nHistory: %d\nContext: %d\nInput: %d\nCurrent turn: %d", s.Total, s.Threshold, s.Window, s.Fixed, s.Summary, s.History, s.Packet, s.Input, s.Active)
	return true, r.publish(ctx, protocol.KindResult, notice.ID, protocol.Result{ResultID: "result:" + notice.ID, Revision: "1", Type: protocol.ResultTypeDocumentDraft, Status: protocol.ResultStatusPreview, Title: design.Text(eventLanguage(notice), "reply.request_limit"), Summary: notice.Body.Text(), Text: &text, RevisedBy: r.agent, Provenance: protocol.ResultProvenance{AgentID: r.agent, ConversationID: r.conversation, TurnID: turnID}})
}
