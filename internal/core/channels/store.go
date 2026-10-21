// SPDX-License-Identifier: Apache-2.0

// Package channels owns connection identity, dispatch scope and durable delivery
// state. It holds no bot credentials and performs no platform calls.
package channels

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var (
	ErrInvalid  = errors.New("invalid channel record")
	ErrDenied   = errors.New("channel access denied")
	ErrCursor   = errors.New("channel cursor changed")
	ErrEmpty    = errors.New("channel queue has no ready message")
	ErrConflict = errors.New("channel record changed")
)

type Canon interface {
	Write(context.Context, func(*canon.Tx) error) error
	Read(context.Context, func(*canon.Tx) error) error
}
type Store struct{ db Canon }

func New(db Canon) *Store { return &Store{db: db} }

type Bot struct{ Platform, Account string }

func (b Bot) Valid() bool { return present(b.Platform) && present(b.Account) }
func (b Bot) Key() string { v, _ := json.Marshal([]string{b.Platform, b.Account}); return string(v) }

type Address struct {
	Bot
	Chat, Topic string
}

func (a Address) Valid() bool { return a.Bot.Valid() && present(a.Chat) }
func (a Address) Key() string {
	v, _ := json.Marshal([]string{a.Platform, a.Account, a.Chat, a.Topic})
	return string(v)
}
func present(s string) bool { return strings.TrimSpace(s) != "" }

type Reads string

const (
	DMOnly      Reads = "dm_only"
	Addressed   Reads = "addressed"
	AllMessages Reads = "all_messages"
)

type Writes string

const (
	ReplyOnly Writes = "reply_only"
	MayStart  Writes = "may_start"
)

type Scope struct {
	Reads         Reads
	Writes        Writes
	Tools, Memory bool
}

func PrivateScope() Scope { return Scope{Reads: DMOnly, Writes: ReplyOnly, Tools: true, Memory: true} }
func (s Scope) Valid() bool {
	switch s.Reads {
	case DMOnly, Addressed, AllMessages:
	default:
		return false
	}
	switch s.Writes {
	case ReplyOnly, MayStart:
	default:
		return false
	}
	return true
}

type Binding struct {
	Address
	Agent, Conversation, Person string
	Scope                       Scope
	Unreachable                 bool
}

// Schema is installed once by the composition root in a numbered migration,
// after canon.Migrations (including the ledger). It extends agent_bindings rather than owning a second
// agent mapping, so agent trash/expiry atomically removes channel dispatches.
const Schema = `
CREATE TABLE channel_connections (
 bot TEXT PRIMARY KEY, person TEXT NOT NULL, sender TEXT NOT NULL DEFAULT '',
 private_chat TEXT NOT NULL DEFAULT '', cursor TEXT NOT NULL DEFAULT '',
 pairing_hash BLOB, pairing_expires INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE channel_bindings (
 address TEXT PRIMARY KEY REFERENCES agent_bindings(channel) ON DELETE CASCADE,
 bot TEXT NOT NULL REFERENCES channel_connections(bot), chat TEXT NOT NULL, topic TEXT NOT NULL,
 conversation TEXT NOT NULL, scope BLOB NOT NULL, unreachable INTEGER NOT NULL DEFAULT 0,
 UNIQUE(bot,chat,topic)
);
CREATE TABLE channel_actions (
 id INTEGER PRIMARY KEY, address TEXT NOT NULL, person TEXT NOT NULL,
 agent TEXT NOT NULL, action TEXT NOT NULL, scope BLOB NOT NULL
);
CREATE TABLE channel_receipts (
 bot TEXT NOT NULL REFERENCES channel_connections(bot), native_id TEXT NOT NULL,
 cursor TEXT NOT NULL, address TEXT NOT NULL, agent TEXT NOT NULL, inbox_id INTEGER REFERENCES inbox(id),
 PRIMARY KEY(bot,native_id)
);
CREATE TABLE channel_outbound (
 sequence INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE,
 address TEXT NOT NULL, bot TEXT NOT NULL, chat TEXT NOT NULL, topic TEXT NOT NULL,
 agent TEXT NOT NULL, ledger_ref TEXT NOT NULL REFERENCES conv_event(id), reply_to TEXT NOT NULL, parts BLOB NOT NULL,
 next_part INTEGER NOT NULL DEFAULT 0, attempt INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL DEFAULT 'queued'
 CHECK(state IN ('queued','sending','sent','unknown','blocked','refused')),
 retry_at INTEGER NOT NULL DEFAULT 0, message_ids BLOB NOT NULL DEFAULT '[]'
);
CREATE INDEX channel_outbound_queue ON channel_outbound(bot,chat,sequence);
`

// Connect is called by the authenticated control flow. Existing ownership cannot
// be reassigned by reconnecting a bot. Platform tokens stay in the secret store.
func (s *Store) Connect(ctx context.Context, b Bot, person string) error {
	if !b.Valid() || !present(person) {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO channel_connections(bot,person) VALUES(?,?) ON CONFLICT(bot) DO NOTHING", b.Key(), person)
		if err != nil {
			return err
		}
		return owner(ctx, tx, b, person)
	})
}
func owner(ctx context.Context, tx *canon.Tx, b Bot, person string) error {
	var stored string
	if err := tx.QueryRowContext(ctx, "SELECT person FROM channel_connections WHERE bot=?", b.Key()).Scan(&stored); err != nil {
		return err
	}
	if stored != person {
		return ErrDenied
	}
	return nil
}
func (s *Store) Bind(ctx context.Context, b Binding) error {
	if !b.Address.Valid() || !present(b.Agent) || !present(b.Person) || !present(b.Conversation) || !b.Scope.Valid() {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		if err := owner(ctx, tx, b.Bot, b.Person); err != nil {
			return err
		}
		var sender string
		if err := tx.QueryRowContext(ctx, "SELECT sender FROM channel_connections WHERE bot=?", b.Bot.Key()).Scan(&sender); err != nil {
			return err
		}
		if sender == "" {
			return ErrDenied
		}
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM agents WHERE id=? AND trashed_at=0", b.Agent).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return ErrDenied
		}
		data, _ := json.Marshal(b.Scope)
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_bindings(channel,agent_id,person_id) VALUES(?,?,?)
   ON CONFLICT(channel) DO UPDATE SET agent_id=excluded.agent_id,person_id=excluded.person_id`, b.Key(), b.Agent, b.Person); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO channel_bindings(address,bot,chat,topic,conversation,scope) VALUES(?,?,?,?,?,?)
   ON CONFLICT(address) DO UPDATE SET conversation=excluded.conversation,scope=excluded.scope`, b.Key(), b.Bot.Key(), b.Chat, b.Topic, b.Conversation, data); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO channel_actions(address,person,agent,action,scope) VALUES(?,?,?,'bind',?)", b.Key(), b.Person, b.Agent, data)
		return err
	})
}
func (s *Store) Unbind(ctx context.Context, a Address, person string) error {
	if !a.Valid() || !present(person) {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		if err := owner(ctx, tx, a.Bot, person); err != nil {
			return err
		}
		b, err := Resolve(ctx, tx, a)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if b.Address != a {
			return nil
		} // Never remove a parent binding for an absent topic.
		data, _ := json.Marshal(b.Scope)
		if _, err = tx.ExecContext(ctx, "INSERT INTO channel_actions(address,person,agent,action,scope) VALUES(?,?,?,'unbind',?)", a.Key(), person, b.Agent, data); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM agent_bindings WHERE channel=?", a.Key())
		return err
	})
}
func Resolve(ctx context.Context, tx *canon.Tx, a Address) (Binding, error) {
	b := Binding{Address: a}
	var data []byte
	err := tx.QueryRowContext(ctx, `SELECT c.topic,a.agent_id,a.person_id,c.conversation,c.scope,c.unreachable
 FROM channel_bindings c JOIN agent_bindings a ON a.channel=c.address JOIN agents g ON g.id=a.agent_id JOIN channel_connections owner ON owner.bot=c.bot
 WHERE owner.sender<>'' AND owner.person=a.person_id AND c.bot=? AND c.chat=? AND (c.topic=? OR c.topic='') AND g.trashed_at=0
 ORDER BY (c.topic=?) DESC LIMIT 1`, a.Bot.Key(), a.Chat, a.Topic, a.Topic).Scan(&b.Topic, &b.Agent, &b.Person, &b.Conversation, &data, &b.Unreachable)
	if err != nil {
		return Binding{}, err
	}
	if err = json.Unmarshal(data, &b.Scope); err != nil {
		return Binding{}, err
	}
	return b, nil
}
func (s *Store) Binding(ctx context.Context, a Address) (Binding, error) {
	var b Binding
	err := s.db.Read(ctx, func(tx *canon.Tx) error { var err error; b, err = Resolve(ctx, tx, a); return err })
	return b, err
}
func (s *Store) Cursor(ctx context.Context, b Bot) (string, error) {
	var cursor string
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT cursor FROM channel_connections WHERE bot=?", b.Key()).Scan(&cursor)
	})
	return cursor, err
}

// AdmitScope rejects external participants by default. Platform identity and
// direct/addressed flags come from the adapter, never parsed message text.
func AdmitScope(ctx context.Context, tx *canon.Tx, a Address, sender string, direct, addressed bool) (Binding, error) {
	var paired, private string
	if err := tx.QueryRowContext(ctx, "SELECT sender,private_chat FROM channel_connections WHERE bot=?", a.Bot.Key()).Scan(&paired, &private); err != nil {
		return Binding{}, err
	}
	if paired == "" || paired != sender {
		return Binding{}, ErrDenied
	}
	b, err := Resolve(ctx, tx, a)
	if err != nil {
		return Binding{}, err
	}
	switch b.Scope.Reads {
	case DMOnly:
		if !direct || a.Chat != private {
			return Binding{}, ErrDenied
		}
	case Addressed:
		if !addressed {
			return Binding{}, ErrDenied
		}
	case AllMessages:
	default:
		return Binding{}, ErrDenied
	}
	return b, nil
}

// Seen and RecordReceipt are composed with inbox.Insert by the gateway. A
// native event remains deduplicated even when the dispatch is later rebound.
func Seen(ctx context.Context, tx *canon.Tx, b Bot, id string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, "SELECT count(*) FROM channel_receipts WHERE bot=? AND native_id=?", b.Key(), id).Scan(&n)
	return n != 0, err
}
func RecordReceipt(ctx context.Context, tx *canon.Tx, a Address, agent, id, previous, cursor string, inboxID int64) error {
	result, err := tx.ExecContext(ctx, "UPDATE channel_connections SET cursor=? WHERE bot=? AND cursor=?", cursor, a.Bot.Key(), previous)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrCursor
	}
	var row any
	if inboxID > 0 {
		row = inboxID
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO channel_receipts(bot,native_id,cursor,address,agent,inbox_id) VALUES(?,?,?,?,?,?)", a.Bot.Key(), id, cursor, a.Key(), agent, row)
	return err
}
