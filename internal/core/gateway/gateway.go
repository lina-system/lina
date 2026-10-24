// SPDX-License-Identifier: Apache-2.0

// Package gateway composes channel identity, inbox admission and platform I/O.
package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/kit/lease"
)

type Adapter interface {
	Run(context.Context, Admitter) error
	Send(context.Context, lease.Lease, Outbound) (SendResult, error)
	Edit(context.Context, MsgRef, Outbound) error
	Answer(context.Context, InteractionRef, string) error
	Fetch(context.Context, AttachmentRef) (io.ReadCloser, error)
	Caps() Capabilities
}
type Admitter interface {
	Admit(context.Context, Inbound) error
}
type Capabilities struct {
	TextLength                               int
	Edit, Buttons, Files, Threads, Streaming bool
	ResultTypes                              []string
}
type MsgRef struct {
	channels.Address
	ID string
}
type InteractionRef struct {
	channels.Bot
	ID string
}
type AttachmentRef struct {
	channels.Bot
	ID string
}
type Inbound struct {
	channels.Address
	NativeID, Sender       string
	PreviousCursor, Cursor string
	Direct, Addressed      bool
	Body                   []byte
	// DeferCursor leaves the shared session checkpoint to the transport session
	// owner, which advances it only after every subscriber admits the dispatch.
	DeferCursor bool
}

// Delivery carries only committed input. The composition root routes it through
// the conversation's turn/steering coordinator, which owns durable turn claims.
// Admission is acknowledged by storage, not by successful turn execution.
type Delivery struct {
	Origin channels.Address
	channels.Binding
	InboxID  int64
	NativeID string
}
type Dispatch func(context.Context, Delivery) error
type Gateway struct {
	db       channels.Canon
	dispatch Dispatch
}

func New(db channels.Canon, dispatch Dispatch) *Gateway { return &Gateway{db: db, dispatch: dispatch} }
func (g *Gateway) Admit(ctx context.Context, e Inbound) error {
	if !e.Address.Valid() || e.NativeID == "" || e.Sender == "" || e.Cursor == "" || g.dispatch == nil {
		return channels.ErrInvalid
	}
	var d Delivery
	var inserted bool
	record := channels.RecordReceipt
	if e.DeferCursor {
		record = func(ctx context.Context, tx *canon.Tx, a channels.Address, agent, id, _, cursor string, inboxID int64) error {
			return channels.NoteReceipt(ctx, tx, a, agent, id, cursor, inboxID)
		}
	}
	err := g.db.Write(ctx, func(tx *canon.Tx) error {
		seen, err := channels.Seen(ctx, tx, e.Bot, e.NativeID)
		if err != nil || seen {
			return err
		}
		b, err := channels.AdmitScope(ctx, tx, e.Address, e.Sender, e.Direct, e.Addressed)
		if errors.Is(err, channels.ErrDenied) || errors.Is(err, sql.ErrNoRows) {
			return record(ctx, tx, e.Address, "", e.NativeID, e.PreviousCursor, e.Cursor, 0)
		}
		if err != nil {
			return err
		}
		body, err := json.Marshal(e)
		if err != nil {
			return err
		}
		r, err := inbox.Insert(ctx, tx, inbox.Input{Key: inbox.Key{Conversation: b.Conversation, Source: e.Bot.Key(), NativeID: e.NativeID}, Body: body})
		if err != nil {
			return err
		}
		if err = record(ctx, tx, e.Address, b.Agent, e.NativeID, e.PreviousCursor, e.Cursor, r.ID); err != nil {
			return err
		}
		inserted = r.Inserted
		d = Delivery{Origin: e.Address, Binding: b, InboxID: r.ID, NativeID: e.NativeID}
		return nil
	})
	if err != nil {
		return err
	}
	if inserted {
		return g.dispatch(ctx, d)
	}
	return nil
}

type Outcome string

const (
	Delivered          Outcome = "delivered"
	RetryLater         Outcome = "retry_later"
	PermanentlyBlocked Outcome = "blocked"
	TooLong            Outcome = "too_long"
	Unauthorized       Outcome = "unauthorized"
	Uncertain          Outcome = "unknown"
)

type Outbound struct {
	channels.Address
	ID, LedgerRef, ReplyTo, Text string
	Part                         int
}
type SendResult struct {
	Outcome    Outcome
	MessageID  string
	RetryAfter time.Duration
}
type Sender interface {
	Send(context.Context, lease.Lease, Outbound) (SendResult, error)
}
type Queue struct {
	store  *channels.Store
	sender Sender
}

func NewQueue(db channels.Canon, s Sender) *Queue  { return &Queue{store: channels.New(db), sender: s} }
func (q *Queue) Recover(ctx context.Context) error { return q.store.Recover(ctx) }

// SendNext sends at most one part and never sleeps or retries. The scheduler
// supplies time and a hard lease; the persisted retry time controls the next call.
func (q *Queue) SendNext(ctx context.Context, l lease.Lease, a channels.Address, now time.Time) (bool, error) {
	if q.sender == nil {
		return false, channels.ErrInvalid
	}
	attempted := false
	err := lease.Do(ctx, l, func(ctx context.Context) error {
		m, err := q.store.ClaimNext(ctx, a, now)
		if errors.Is(err, channels.ErrEmpty) {
			return nil
		}
		if err != nil {
			return err
		}
		attempted = true
		result, sendErr := q.sender.Send(ctx, l, Outbound{Address: m.Address, ID: m.ID, LedgerRef: m.LedgerRef, ReplyTo: m.ReplyTo, Text: m.Parts[m.NextPart], Part: m.NextPart})
		state := channels.Unknown
		retry := now
		if sendErr == nil {
			switch result.Outcome {
			case Delivered:
				if result.MessageID != "" {
					state = channels.Sent
				}
			case RetryLater:
				if result.RetryAfter >= 0 {
					state = channels.Queued
					retry = now.Add(result.RetryAfter)
				}
			case PermanentlyBlocked:
				state = channels.Blocked
			case TooLong, Unauthorized:
				state = channels.Refused
			case Uncertain:
			default:
			}
		}
		// A cancelled HTTP request is still an uncertain effect. Persist that outcome
		// independently of request cancellation; failed persistence leaves sending,
		// which excludes concurrent sends and becomes unknown at recovery.
		settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = q.store.Settle(settleCtx, m, state, result.MessageID, retry)
		return errors.Join(sendErr, err)
	})
	return attempted, err
}
