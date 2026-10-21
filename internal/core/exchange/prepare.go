// SPDX-License-Identifier: Apache-2.0
package exchange

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/provider"
)

var ErrClaimed = errors.New("exchange_turn_already_accepted")

// Knowledge deliberately has no personal recall, person brief, conversation
// history, arbitrary material search or knowledge-write capability. Persona
// implements agents.Store.Prepare with the delivery id as its durable turn key.
// Common is the common layer only; Material resolves exactly the named revision.
type Knowledge interface {
	Persona(context.Context, string, string) (agents.Preparation, error)
	Common(context.Context) ([]string, error)
	Material(context.Context, Material) (string, error)
}
type Prepared struct {
	ExchangeID, DeliveryID, Agent string
	Persona                       agents.Preparation
	Request                       provider.Request
}

// Projection excludes secret spans even when a reader supplies raw bytes.
func project(text string) (string, error) {
	masked, err := secrets.Mask(text, bodySpans(text)...)
	return masked.DerivedText(), err
}

func message(role, text string) provider.Item {
	return encode(struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{role, text})
}

// Prepare is called inside the answering agent's background run. It durably
// claims the delivery before loading context. Accepted turns are never replayed
// after restart; the deadline sweep exposes an unanswered interrupted turn.
// Start itself only queues the exchange, so the requesting turn never waits.
// The returned request is fresh and has no tools: callers must not append a
// conversation context or offer personal-knowledge tools to the exchange run.
func (s *Store) Prepare(ctx context.Context, delivery, actor string, at time.Time, knowledge Knowledge) (Prepared, error) {
	if knowledge == nil || !timestamp(at) {
		return Prepared{}, ErrInvalid
	}
	var r Record
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		var err error
		r, err = forDelivery(ctx, tx, delivery)
		if err != nil {
			return err
		}
		if actor != r.To {
			return ErrInvalid
		}
		if r.Outcome != Pending {
			return ErrClosed
		}
		if r.State == Read {
			return ErrClaimed
		}
		r, err = deliver(ctx, tx, r, at)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE exch_msg SET state=? WHERE delivery_id=?", Read, delivery); err != nil {
			return err
		}
		return appendEvent(ctx, tx, r, r.To, "read", "", at)
	})
	if err != nil {
		return Prepared{}, err
	}
	persona, err := knowledge.Persona(ctx, delivery, r.To)
	if err != nil {
		return Prepared{}, err
	}
	if persona.TurnID != delivery || persona.AgentID != r.To || persona.Revision < 1 || persona.Commit == "" {
		return Prepared{}, ErrInvalid
	}
	common, err := knowledge.Common(ctx)
	if err != nil {
		return Prepared{}, err
	}
	layers := persona.Layers
	instructions, err := project(strings.Join([]string{layers.Core, layers.DefaultVoice, layers.UserVoice, layers.GrowthSnapshot}, "\n\n"))
	if err != nil {
		return Prepared{}, err
	}
	req := provider.Request{Purpose: provider.Background, Instructions: instructions}
	for _, text := range common {
		text, err = project(text)
		if err != nil {
			return Prepared{}, err
		}
		req.Items = append(req.Items, message("developer", text))
	}
	for _, ref := range r.HandOff.Materials {
		text, err := knowledge.Material(ctx, ref)
		if err != nil {
			return Prepared{}, err
		}
		text, err = project(text)
		if err != nil {
			return Prepared{}, err
		}
		admitted, err := guard.Admit(guard.EntryFile, ref.ID+"@"+ref.Revision, text)
		if err != nil {
			return Prepared{}, err
		}
		e, _ := admitted.Evidence()
		req.Items = append(req.Items, message(e.Role, e.Text))
	}
	events, err := s.Events(ctx, r.ID)
	if err != nil {
		return Prepared{}, err
	}
	for _, event := range events {
		if event.Kind != "request" && event.Kind != string(Checked) {
			continue
		}
		e, _ := event.Message.Evidence()
		req.Items = append(req.Items, message(e.Role, e.Text))
	}
	return Prepared{ExchangeID: r.ID, DeliveryID: delivery, Agent: r.To, Persona: persona, Request: req}, nil
}

// PendingDeliveries is the durable work list for Core's background scheduler.
// Delivered inputs remain eligible until their one Prepare claim commits. A
// scheduler can enumerate it after restart without any open person conversation.
func (s *Store) PendingDeliveries(ctx context.Context) ([]Receipt, error) {
	var result []Receipt
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT e.id,m.delivery_id FROM exchanges e JOIN exch_msg m ON m.delivery_id=e.delivery_id
   WHERE e.outcome='pending' AND m.state IN ('queued','delivered') ORDER BY e.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r Receipt
			if err = rows.Scan(&r.ID, &r.DeliveryID); err != nil {
				return err
			}
			result = append(result, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
