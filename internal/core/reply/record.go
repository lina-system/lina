// SPDX-License-Identifier: Apache-2.0
package reply

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/protocol"
)

var ErrClosed = errors.New("reply recording has started")

// Recording supplies trusted identity and card labels from the composition
// layer. Source.Body is replaced by the complete reply, never the visible cut.
// Source.SecretSpans still address that complete original reply.
type Recording struct {
	Source                                        ledger.Input
	CardEventID, ResultID, TurnID, Title, Summary string
}

// Card is the transient presentation of this generated draft. General
// result tools and wire schemas remain owned by the typed-results increment.
type Card struct {
	Preview
	ResultID   string                    `json:"result_id"`
	Revision   int                       `json:"revision"`
	Title      string                    `json:"title"`
	Summary    string                    `json:"summary"`
	SavedAs    *protocol.SavedReference  `json:"saved_as"`
	RevisedBy  string                    `json:"revised_by"`
	Provenance protocol.ResultProvenance `json:"provenance"`
}

// CardReference persists only presentation metadata and the canonical source ID.
// The source supplies masked text when the card is opened or restored.
type CardReference struct {
	Type        protocol.ResultType       `json:"type"`
	Status      protocol.ResultStatus     `json:"status"`
	SourceEvent string                    `json:"source_event"`
	ResultID    string                    `json:"result_id"`
	Revision    int                       `json:"revision"`
	Title       string                    `json:"title"`
	Provenance  protocol.ResultProvenance `json:"provenance"`
}

func (c Card) Reference(source string) CardReference {
	return CardReference{Type: c.Type, Status: c.Status, SourceEvent: source, ResultID: c.ResultID, Revision: c.Revision, Title: c.Title, Provenance: c.Provenance}
}

type Recorded struct {
	Reply ledger.Event
	Card  *ledger.Event
}

// Record freezes the stream and commits the full source and linked card in one
// ledger transaction. A crash leaves both events or neither. Failed writes can
// be retried with the same store and metadata; success never duplicates either
// event. Record does not save a preview on the person's behalf.
func (f *Filter) Record(ctx context.Context, journal *ledger.Store, r Recording) (Recorded, error) {
	if journal == nil {
		return Recorded{}, ErrConfig
	}
	r.Source.Body = f.FullText()
	r.Source.Relations = append([]ledger.Relation(nil), r.Source.Relations...)
	r.Source.SecretSpans = append([]secrets.Span(nil), r.Source.SecretSpans...)
	r.Source.Tags = append([]secrets.Span(nil), r.Source.Tags...)
	if f.recording != nil {
		if journal != f.journal || !reflect.DeepEqual(r, *f.recording) {
			return Recorded{}, ErrClosed
		}
	} else {
		if f.cut && (strings.TrimSpace(r.CardEventID) == "" || r.CardEventID == r.Source.ID || strings.TrimSpace(r.ResultID) == "" || strings.TrimSpace(r.TurnID) == "" || strings.TrimSpace(r.Title) == "" || strings.TrimSpace(r.Summary) == "") {
			return Recorded{}, ErrConfig
		}
		f.recording = &r
		f.journal = journal
		f.closed = true
	}
	if f.recorded.Reply.ID == "" {
		inputs := []ledger.Admission{{Entry: guard.EntryAgent, Input: r.Source}}
		if f.cut {
			source := r.Source
			card := Card{Preview: Preview{Type: protocol.ResultTypeDocumentDraft, Status: protocol.ResultStatusPreview},
				ResultID: r.ResultID, Revision: 1, Title: r.Title,
				Provenance: protocol.ResultProvenance{AgentID: source.AgentID, ConversationID: source.ConversationID, TurnID: r.TurnID}}
			body, err := json.Marshal(card.Reference(source.ID))
			if err != nil {
				return Recorded{}, err
			}
			in := ledger.Input{ID: r.CardEventID, ConversationID: source.ConversationID, ParentID: source.ID, AgentID: source.AgentID,
				Surface: source.Surface, OccurredAt: source.OccurredAt, RecordedAt: source.RecordedAt, TimeQuality: source.TimeQuality,
				ScaffoldKind: "result", Body: string(body), Relations: []ledger.Relation{{TargetID: source.ID, Kind: "view_of", Ordinal: 0}}}
			inputs = append(inputs, ledger.Admission{Entry: guard.EntrySystem, Input: in})
		}
		inputs = append(inputs, f.Check().Admission(r.Source))
		events, err := journal.AdmitBatch(ctx, inputs)
		if err != nil {
			return Recorded{}, err
		}
		f.recorded.Reply = events[0]
		if f.cut {
			f.recorded.Card = &events[1]
		}
	}

	result := Recorded{Reply: cloneEvent(f.recorded.Reply)}
	if f.recorded.Card != nil {
		card := cloneEvent(*f.recorded.Card)
		result.Card = &card
	}
	return result, nil
}

func cloneEvent(e ledger.Event) ledger.Event {
	e.Spans = append([]secrets.Span(nil), e.Spans...)
	e.Relations = append([]ledger.Relation(nil), e.Relations...)
	return e
}
