// SPDX-License-Identifier: Apache-2.0
package channels

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
)

type State string

const (
	Queued  State = "queued"
	Sending State = "sending"
	Sent    State = "sent"
	Unknown State = "unknown"
	Blocked State = "blocked"
	Refused State = "refused"
)

// Message refers to a reply already committed to the ledger. Parts are shaped
// once by the adapter and retained verbatim, including on an uncertain send.
type Message struct {
	Attempt int64
	ID      string
	Address
	Agent, LedgerRef, ReplyTo string
	Parts                     []string
	NextPart                  int
	State                     State
	RetryAt                   time.Time
	MessageIDs                []string
}

func (s *Store) Enqueue(ctx context.Context, m Message) error {
	if !present(m.ID) || !m.Address.Valid() || !present(m.Agent) || !present(m.LedgerRef) || len(m.Parts) == 0 {
		return ErrInvalid
	}
	for _, p := range m.Parts {
		if p == "" {
			return ErrInvalid
		}
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		// Idempotency covers the entire reply, not only its most recent part.
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM channel_outbound WHERE id=?", m.ID).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return nil
		}
		b, err := Resolve(ctx, tx, m.Address)
		if err != nil {
			return err
		}
		if b.Agent != m.Agent || b.Unreachable {
			return ErrDenied
		}
		// The queue cannot invent a reply: its immutable ledger event must
		// already exist and belong to this dispatch's agent and conversation.
		// Background results retain native notice provenance; a system label or
		// copied scaffold alone never authorizes outbound content.
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM conv_event WHERE id=? AND agent_id=? AND conversation_id=? AND (speaker_kind='agent' OR (speaker_kind='system' AND scaffold_kind='background_notice' AND json_extract(origin,'$.kind')='native' AND json_extract(origin,'$.entry')=?))`, m.LedgerRef, m.Agent, b.Conversation, guard.EntryBackgroundNotice).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return ErrDenied
		}
		if m.ReplyTo != "" {
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM channel_receipts WHERE bot=? AND native_id=? AND address=? AND agent=? AND inbox_id IS NOT NULL`, m.Bot.Key(), m.ReplyTo, m.Address.Key(), m.Agent).Scan(&n); err != nil {
				return err
			}
			if n != 1 {
				return ErrDenied
			}
		} else {
			if b.Scope.Writes != MayStart {
				return ErrDenied
			}
			var private string
			if err = tx.QueryRowContext(ctx, "SELECT private_chat FROM channel_connections WHERE bot=?", m.Bot.Key()).Scan(&private); err != nil {
				return err
			}
			// May-start never initiates a private chat without the person's action.
			if b.Scope.Reads == DMOnly && private != m.Chat {
				return ErrDenied
			}
		}
		parts, _ := json.Marshal(m.Parts)
		_, err = tx.ExecContext(ctx, `INSERT INTO channel_outbound(id,address,bot,chat,topic,agent,ledger_ref,reply_to,parts) VALUES(?,?,?,?,?,?,?,?,?)`, m.ID, m.Address.Key(), m.Bot.Key(), m.Chat, m.Topic, m.Agent, m.LedgerRef, m.ReplyTo, parts)
		return err
	})
}
func readMessage(ctx context.Context, tx *canon.Tx, id string) (Message, error) {
	m := Message{ID: id}
	var parts, ids []byte
	var address string
	var retry int64
	err := tx.QueryRowContext(ctx, `SELECT address,agent,ledger_ref,reply_to,parts,next_part,attempt,state,retry_at,message_ids FROM channel_outbound WHERE id=?`, id).Scan(&address, &m.Agent, &m.LedgerRef, &m.ReplyTo, &parts, &m.NextPart, &m.Attempt, &m.State, &retry, &ids)
	if err != nil {
		return Message{}, err
	}
	var a []string
	if err = json.Unmarshal([]byte(address), &a); err != nil {
		return Message{}, err
	}
	if len(a) != 4 {
		return Message{}, ErrInvalid
	}
	m.Address = Address{Bot: Bot{Platform: a[0], Account: a[1]}, Chat: a[2], Topic: a[3]}
	if err = json.Unmarshal(parts, &m.Parts); err != nil {
		return Message{}, err
	}
	if err = json.Unmarshal(ids, &m.MessageIDs); err != nil {
		return Message{}, err
	}
	m.RetryAt = time.Unix(0, retry)
	return m, nil
}
func (s *Store) Message(ctx context.Context, id string) (Message, error) {
	var m Message
	err := s.db.Read(ctx, func(tx *canon.Tx) error { var err error; m, err = readMessage(ctx, tx, id); return err })
	return m, err
}

// ClaimNext durably records intent before the network call. A refused part
// retains its place at the front; a sending part excludes all other dispatchers
// for this bot/chat, even if its topic differs.
func (s *Store) ClaimNext(ctx context.Context, a Address, now time.Time) (Message, error) {
	if !a.Valid() || now.UnixNano() <= 0 {
		return Message{}, ErrInvalid
	}
	var m Message
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		var id string
		err := tx.QueryRowContext(ctx, `SELECT id FROM channel_outbound WHERE bot=? AND chat=? AND state IN ('queued','sending') ORDER BY sequence LIMIT 1`, a.Bot.Key(), a.Chat).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrEmpty
		}
		if err != nil {
			return err
		}
		m, err = readMessage(ctx, tx, id)
		if err != nil {
			return err
		}
		if m.State == Sending || now.Before(m.RetryAt) {
			return ErrEmpty
		}
		b, err := Resolve(ctx, tx, m.Address)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (b.Agent != m.Agent || b.Unreachable || (m.ReplyTo == "" && b.Scope.Writes != MayStart))) {
			_, err = tx.ExecContext(ctx, "UPDATE channel_outbound SET state='refused' WHERE id=?", m.ID)
			m = Message{}
			return err
		}
		if err != nil {
			return err
		}
		if m.NextPart < 0 || m.NextPart >= len(m.Parts) {
			return ErrInvalid
		}
		_, err = tx.ExecContext(ctx, "UPDATE channel_outbound SET state='sending',attempt=attempt+1 WHERE id=?", m.ID)
		m.State = Sending
		m.Attempt++
		return err
	})
	if err != nil {
		return Message{}, err
	}
	if m.ID == "" {
		return Message{}, ErrEmpty
	}
	return m, nil
}

// Settle only accepts the currently claimed part; a stale completion cannot
// overwrite recovery or the receipt of a later attempt.
func (s *Store) Settle(ctx context.Context, m Message, state State, remoteID string, retryAt time.Time) error {
	switch state {
	case Sent, Queued, Unknown, Blocked, Refused:
	case Sending:
		return ErrInvalid
	default:
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		current, err := readMessage(ctx, tx, m.ID)
		if err != nil {
			return err
		}
		if current.State != Sending || current.NextPart != m.NextPart || current.Attempt != m.Attempt {
			return ErrConflict
		}
		next := current.NextPart
		if state == Sent {
			if remoteID == "" {
				return ErrInvalid
			}
			next++
			current.MessageIDs = append(current.MessageIDs, remoteID)
			if next < len(current.Parts) {
				state = Queued
			}
		}
		ids, _ := json.Marshal(current.MessageIDs)
		if state == Blocked {
			if _, err = tx.ExecContext(ctx, "UPDATE channel_bindings SET unreachable=1 WHERE bot=? AND chat=?", m.Bot.Key(), m.Chat); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE channel_outbound SET state='blocked' WHERE bot=? AND chat=? AND state='queued'", m.Bot.Key(), m.Chat); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE channel_outbound SET state=?,next_part=?,retry_at=?,message_ids=? WHERE id=?", state, next, retryAt.UnixNano(), ids, m.ID)
		return err
	})
}

// Recover runs once at startup after the previous epoch is fenced, before any
// adapter starts. In-flight sends have an unknown outcome and are never retried.
func (s *Store) Recover(ctx context.Context) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE channel_outbound SET state='unknown' WHERE state='sending'")
		return err
	})
}
