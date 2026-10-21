// SPDX-License-Identifier: Apache-2.0
package exchange

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

// This adapter lets the L3 ledger compose inside the mailbox's existing canon
// transaction. Its Admit result is provisional until the outer Write commits.
type transaction struct{ tx *canon.Tx }

func (t transaction) Write(_ context.Context, fn func(*canon.Tx) error) error { return fn(t.tx) }
func (t transaction) Read(_ context.Context, fn func(*canon.Tx) error) error  { return fn(t.tx) }

// Bytes are prepared before the mailbox writer; metadata is inserted in the
// same transaction as the event. The nested ledger borrows the prepared write:
// only the outer flow may close it after its canon transaction returns.
type stagedMaterial struct{ write ledger.MaterialWrite }
type borrowedMaterial struct{ ledger.MaterialWrite }

func (borrowedMaterial) Close() error { return nil }
func (s stagedMaterial) Prepare(context.Context, secrets.Masked) (ledger.MaterialWrite, error) {
	if s.write == nil {
		return nil, ledger.ErrBytesUnavailable
	}
	return borrowedMaterial{s.write}, nil
}
func (stagedMaterial) Read(context.Context, ledger.Material) (string, error) {
	return "", ledger.ErrBytesUnavailable
}
func closeMaterial(write ledger.MaterialWrite) {
	if write != nil {
		_ = write.Close()
	}
	// Cleanup cannot revoke a committed receipt. The writer's recovery marker
	// survives cleanup errors and is reconciled on the next open.
}
func bodySpans(body string) []secrets.Span {
	var spans []secrets.Span
	for start := 0; start < len(body); {
		offset := strings.Index(body[start:], secret.Placeholder)
		if offset < 0 {
			break
		}
		start += offset
		spans = append(spans, secrets.Span{Start: start, End: start + len(secret.Placeholder), Sensitivity: "secret"})
		start += len(secret.Placeholder)
	}
	return spans
}
func (s *Store) stageBody(ctx context.Context, body string) (ledger.MaterialWrite, error) {
	if len(body) <= ledger.InlineBytes {
		return nil, nil
	}
	if s.materials == nil {
		return nil, ledger.ErrBytesUnavailable
	}
	masked, err := secrets.Mask(body, bodySpans(body)...)
	if err != nil {
		return nil, err
	}
	return s.materials.Prepare(ctx, masked)
}
func (s *Store) prepareInput(ctx context.Context, input Request, at time.Time, id string) (Request, ledger.MaterialWrite, *Receipt, error) {
	if !identifier(input.DeliveryID) || !identifier(input.From) || !identifier(input.To) || !timestamp(at) {
		return Request{}, nil, nil, ErrInvalid
	}
	var receipt *Receipt
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var existing, from, to string
		err := tx.QueryRowContext(ctx, `SELECT e.id,e.from_agent,e.to_agent FROM exch_msg m JOIN exchanges e ON e.id=m.exchange_id WHERE m.delivery_id=?`, input.DeliveryID).Scan(&existing, &from, &to)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if existing != id || from != input.From || to != input.To {
			return ErrConflict
		}
		receipt = &Receipt{ID: id, DeliveryID: input.DeliveryID}
		return nil
	})
	if err != nil || receipt != nil {
		return Request{}, nil, receipt, err
	}
	input, err = normalize(input, at)
	if err != nil {
		return Request{}, nil, nil, err
	}
	material, err := s.stageBody(ctx, string(encode(input.HandOff)))
	return input, material, nil, err
}
func eventID(delivery, kind string) string { return exchangeID(delivery) + "-" + kind }
func appendEvent(ctx context.Context, tx *canon.Tx, r Record, agent, kind, body string, at time.Time, material ...ledger.MaterialWrite) error {
	if !timestamp(at) {
		return ErrInvalid
	}
	var ref ledger.MaterialWrite
	if len(material) > 0 {
		ref = material[0]
	}
	parent := ""
	if kind != "request" {
		parent = eventID(r.DeliveryID, "request")
	}
	e, err := ledger.New(transaction{tx}, stagedMaterial{ref}).Admit(ctx, guard.EntryAgentExchange, ledger.Input{
		ID: eventID(r.DeliveryID, kind), ConversationID: r.ID, ParentID: parent, AgentID: agent,
		Surface: ledger.Surface{Kind: "agent_exchange"}, OccurredAt: at, RecordedAt: at, TimeQuality: "observed",
		Locator: r.ID + "/" + r.DeliveryID, Body: body, SecretSpans: bodySpans(body),
	})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO exch_event(exchange_id,delivery_id,event_id,kind) VALUES(?,?,?,?)", r.ID, r.DeliveryID, e.ID, kind)
	return err
}
