// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/inbox"
)

// Schema is installed in a numbered canon migration after channels.Schema.
// An approval input is durable before it is answered, and never starts a turn.
// Recovery only rechecks authority; Answer itself is one-shot and epoch-fenced.
const Schema = `CREATE TABLE telegram_buttons (
 inbox_id INTEGER PRIMARY KEY REFERENCES inbox(id), bot TEXT NOT NULL,
 epoch TEXT NOT NULL, done INTEGER NOT NULL DEFAULT 0 CHECK(done IN (0,1))
);`

var pairCode = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Message is the token-free body admitted by the adapter. Usernames are never
// identities. File IDs are opaque references; importing them requires the
// already-admitted owner's binding and the usual import boundary.
type Message struct {
	ID        string   `json:"message_id"`
	Text      string   `json:"text"`
	Files     []string `json:"files,omitempty"`
	Forwarded bool     `json:"forwarded,omitempty"`
}

func (a *Adapter) address(chat, thread int64) channels.Address {
	address := channels.Address{Bot: a.bot, Chat: strconv.FormatInt(chat, 10)}
	if thread != 0 {
		address.Topic = strconv.FormatInt(thread, 10)
	}
	return address
}

func (a *Adapter) runUpdates(ctx context.Context, in gateway.Admitter, previous string, offset int64) error {
	return a.client.poll(ctx, offset, func(ctx context.Context, u gotgbot.Update, next int64) error {
		cursor := strconv.FormatInt(next, 10)
		id := strconv.FormatInt(u.UpdateId, 10)
		if q := u.CallbackQuery; q != nil {
			if err := a.admitButton(ctx, *q, id, previous, cursor); err != nil {
				return err
			}
		} else if m := u.Message; m != nil && m.From != nil && !m.From.IsBot && m.From.Id > 0 && m.Chat.Id != 0 && m.MessageId > 0 {
			address := a.address(m.Chat.Id, m.MessageThreadId)
			paired := false
			if m.Chat.Type == "private" {
				code := strings.TrimPrefix(m.Text, "/start ")
				if len(code) == 32 && pairCode.MatchString(code) {
					paired = a.store.Pair(ctx, a.bot, code, strconv.FormatInt(m.From.Id, 10), address.Chat, time.Now()) == nil
				}
			}
			if paired {
				if err := a.ignore(ctx, address, id, previous, cursor); err != nil {
					return err
				}
			} else {
				body := Message{ID: strconv.FormatInt(m.MessageId, 10), Text: m.Text, Forwarded: m.ForwardOrigin != nil}
				if body.Text == "" {
					body.Text = m.Caption
				}
				if m.Document != nil {
					body.Files = append(body.Files, m.Document.FileId)
				}
				if len(m.Photo) > 0 {
					body.Files = append(body.Files, m.Photo[len(m.Photo)-1].FileId)
				}
				data, _ := json.Marshal(body)
				if err := in.Admit(ctx, gateway.Inbound{Address: address, NativeID: id, Sender: strconv.FormatInt(m.From.Id, 10), PreviousCursor: previous, Cursor: cursor, Direct: m.Chat.Type == "private", Addressed: a.addressed(*m), Body: data}); err != nil {
					return err
				}
			}
		} else if err := a.ignore(ctx, channels.Address{Bot: a.bot}, id, previous, cursor); err != nil {
			return err
		}
		previous = cursor
		return nil
	})
}

func (a *Adapter) addressed(m gotgbot.Message) bool {
	if m.ReplyToMessage != nil && m.ReplyToMessage.From != nil && strconv.FormatInt(m.ReplyToMessage.From.Id, 10) == a.bot.Account {
		return true
	}
	units := utf16.Encode([]rune(m.Text))
	for _, e := range m.Entities {
		if e.Offset < 0 || e.Length < 0 || e.Offset > int64(len(units)) || e.Length > int64(len(units))-e.Offset {
			continue
		}
		value := string(utf16.Decode(units[e.Offset : e.Offset+e.Length]))
		if e.Type == "mention" && a.username != "" && strings.EqualFold(value, "@"+a.username) {
			return true
		}
		if e.Type == "bot_command" && e.Offset == 0 {
			_, target, hasTarget := strings.Cut(value, "@")
			if !hasTarget || a.username != "" && strings.EqualFold(target, a.username) {
				return true
			}
		}
	}
	return false
}

func (a *Adapter) ignore(ctx context.Context, address channels.Address, id, previous, cursor string) error {
	return a.db.Write(ctx, func(tx *canon.Tx) error {
		seen, err := channels.Seen(ctx, tx, a.bot, id)
		if err != nil || seen {
			return err
		}
		return channels.RecordReceipt(ctx, tx, address, "", id, previous, cursor, 0)
	})
}

func (a *Adapter) admitButton(ctx context.Context, q gotgbot.CallbackQuery, id, previous, cursor string) error {
	queued := false
	err := a.db.Write(ctx, func(tx *canon.Tx) error {
		seen, err := channels.Seen(ctx, tx, a.bot, id)
		if err != nil || seen {
			return err
		}
		address := channels.Address{Bot: a.bot}
		var row int64
		agent := ""
		if m, ok := q.Message.(gotgbot.Message); ok && q.From.Id > 0 && !q.From.IsBot {
			address = a.address(m.Chat.Id, m.MessageThreadId)
			b, err := channels.AdmitScope(ctx, tx, address, strconv.FormatInt(q.From.Id, 10), m.Chat.Type == "private", true)
			if err != nil && !errors.Is(err, channels.ErrDenied) && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && b.Scope.Tools {
				data, _ := json.Marshal(q)
				r, err := inbox.Insert(ctx, tx, inbox.Input{Key: inbox.Key{Conversation: b.Conversation, Source: a.bot.Key() + ":approval", NativeID: id}, Body: data})
				if err != nil {
					return err
				}
				row, agent = r.ID, b.Agent
				if _, err := tx.ExecContext(ctx, "INSERT INTO telegram_buttons(inbox_id,bot,epoch) VALUES(?,?,?)", row, a.bot.Key(), strconv.FormatUint(a.epoch, 10)); err != nil {
					return err
				}
				queued = true
			}
		}
		return channels.RecordReceipt(ctx, tx, address, agent, id, previous, cursor, row)
	})
	if err != nil {
		return err
	}
	if queued {
		return a.resumeButtons(ctx)
	}
	// Rejected presses may acknowledge the interaction, but never edit the
	// owner's message, answer an approval, create a turn or import a file.
	if q.Id != "" {
		_ = a.client.answerCallback(ctx, q.Id, "This button is unavailable.")
	}
	return nil
}

func (a *Adapter) resumeButtons(ctx context.Context) error {
	type pending struct {
		id    int64
		epoch string
		body  []byte
	}
	var rows []pending
	err := a.db.Read(ctx, func(tx *canon.Tx) error {
		r, err := tx.QueryContext(ctx, "SELECT b.inbox_id,b.epoch,i.body FROM telegram_buttons b JOIN inbox i ON i.id=b.inbox_id WHERE b.bot=? AND b.done=0 ORDER BY b.inbox_id", a.bot.Key())
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var p pending
			if err := r.Scan(&p.id, &p.epoch, &p.body); err != nil {
				return err
			}
			rows = append(rows, p)
		}
		return r.Err()
	})
	if err != nil {
		return errAdmission
	}
	for _, p := range rows {
		var q gotgbot.CallbackQuery
		if json.Unmarshal(p.body, &q) != nil {
			return errResponse
		}
		decision := buttonDecision{note: "This button is unavailable."}
		if p.epoch == strconv.FormatUint(a.epoch, 10) && a.approvals != nil {
			decision = decideButton(ctx, a.epoch, q, func(ctx context.Context, user, chat, thread int64) (string, string, error) {
				var b channels.Binding
				err := a.db.Read(ctx, func(tx *canon.Tx) error {
					var err error
					b, err = channels.AdmitScope(ctx, tx, a.address(chat, thread), strconv.FormatInt(user, 10), q.Message.GetChat().Type == "private", true)
					if err == nil && !b.Scope.Tools {
						return channels.ErrDenied
					}
					return err
				})
				return b.Person, b.Agent, err
			}, a.approvals)
		}
		if err := a.db.Write(ctx, func(tx *canon.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE telegram_buttons SET done=1 WHERE inbox_id=?", p.id)
			return err
		}); err != nil {
			return errAdmission
		}
		_ = a.client.answerCallback(ctx, q.Id, decision.note)
		if decision.remove {
			_ = a.client.editMessage(ctx, q.Message.GetChat().Id, q.Message.GetMessageId(), decision.note, nil)
		}
	}
	return nil
}
