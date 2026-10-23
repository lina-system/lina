// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/kit/secret"
)

type user struct {
	ID  string `json:"id"`
	Bot bool   `json:"bot"`
}
type message struct {
	ID         string   `json:"id"`
	Channel    string   `json:"channel_id"`
	Guild      string   `json:"guild_id"`
	Content    string   `json:"content"`
	Author     user     `json:"author"`
	Mentions   []user   `json:"mentions"`
	Referenced *message `json:"referenced_message"`
}
type interaction struct {
	Address     channels.Address `json:"address"`
	ID          string           `json:"id"`
	Application string           `json:"application_id"`
	Type        int              `json:"type"`
	Token       string           `json:"token,omitempty"`
	Channel     string           `json:"channel_id"`
	Guild       string           `json:"guild_id"`
	User        user             `json:"user"`
	Member      struct {
		User user `json:"user"`
	} `json:"member"`
	Message message `json:"message"`
	Data    struct {
		Custom  string `json:"custom_id"`
		Name    string `json:"name"`
		Options []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"options"`
	} `json:"data"`
}

var pairCode = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (a *Adapter) human(u user) bool {
	return snowflake.MatchString(u.ID) && !u.Bot && u.ID != a.bot.Account
}
func (a *Adapter) ignore(ctx context.Context, addr channels.Address, id, previous, cursor string) error {
	return a.db.Write(ctx, func(tx *canon.Tx) error {
		seen, err := channels.Seen(ctx, tx, a.bot, id)
		if err != nil || seen {
			return err
		}
		return channels.RecordReceipt(ctx, tx, addr, "", id, previous, cursor, 0)
	})
}
func (a *Adapter) dispatch(ctx context.Context, in gateway.Admitter, kind string, data json.RawMessage, cursor string) error {
	previous, err := a.store.Cursor(ctx, a.bot)
	if err != nil {
		return err
	}
	if kind == "INTERACTION_CREATE" {
		var q interaction
		if json.Unmarshal(data, &q) != nil {
			return errResponse
		}
		if q.Application != a.bot.Account || !snowflake.MatchString(q.ID) || !snowflake.MatchString(q.Channel) {
			return errResponse
		}
		if q.Guild != "" {
			q.User = q.Member.User
		}
		// Tokens only live during this call. Durable inbox data cannot contain one.
		token := q.Token
		q.Token = ""
		if q.Type == 3 {
			q.Address, err = a.address(ctx, q.Channel, q.Guild)
			if err != nil {
				return err
			}
			return a.button(ctx, q, previous, cursor, token)
		}
		if q.Type != 2 {
			return a.ignore(ctx, channels.Address{Bot: a.bot, Chat: q.Channel}, "interaction:"+q.ID, previous, cursor)
		}

		addr, err := a.address(ctx, q.Channel, q.Guild)
		if err != nil {
			return err
		}
		var admitted, seen bool
		if err = a.db.Read(ctx, func(tx *canon.Tx) error {
			var err error
			seen, err = channels.Seen(ctx, tx, a.bot, "interaction:"+q.ID)
			if err != nil || seen {
				return err
			}
			_, err = channels.AdmitScope(ctx, tx, addr, q.User.ID, q.Guild == "", true)
			admitted = err == nil
			if errors.Is(err, channels.ErrDenied) || errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}); err != nil || seen {
			return err
		}
		if q.Data.Name != "lina" || !a.human(q.User) {
			return a.ignore(ctx, addr, "interaction:"+q.ID, previous, cursor)
		}
		text := ""
		for _, option := range q.Data.Options {
			if option.Name == "text" {
				text = option.Value
			}
		}
		err = a.admitMessage(ctx, in, message{ID: "interaction:" + q.ID, Channel: q.Channel, Guild: q.Guild, Content: text, Author: q.User}, previous, cursor, true)
		if err == nil && admitted {
			a.mu.Lock()
			a.interactions[q.ID] = token
			a.mu.Unlock()
			// Acknowledge once after durable admission; model work continues in Core.
			_ = a.Answer(ctx, gateway.InteractionRef{Bot: a.bot, ID: q.ID}, "Working…")
		}
		return err
	}
	if kind == "MESSAGE_CREATE" {
		var m message
		if json.Unmarshal(data, &m) != nil {
			return errResponse
		}
		return a.admitMessage(ctx, in, m, previous, cursor, false)
	}
	return a.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE channel_connections SET cursor=? WHERE bot=? AND cursor=?", cursor, a.bot.Key(), previous)
		return err
	})
}
func (a *Adapter) admitMessage(ctx context.Context, in gateway.Admitter, m message, previous, cursor string, command bool) error {
	addr, err := a.address(ctx, m.Channel, m.Guild)
	if err != nil {
		return err
	}
	id := m.ID
	if !snowflake.MatchString(m.Channel) || id == "" {
		return errResponse
	}
	if !a.human(m.Author) {
		return a.ignore(ctx, addr, id, previous, cursor)
	}
	var seen bool
	if err := a.db.Read(ctx, func(tx *canon.Tx) error { var err error; seen, err = channels.Seen(ctx, tx, a.bot, id); return err }); err != nil || seen {
		return err
	}
	if m.Guild == "" {
		code := strings.TrimPrefix(m.Content, "/start ")
		if pairCode.MatchString(code) && a.store.Pair(ctx, a.bot, code, m.Author.ID, m.Channel, time.Now()) == nil {
			return a.ignore(ctx, addr, id, previous, cursor)
		}
	}
	addressed := command || m.Guild == ""
	for _, u := range m.Mentions {
		addressed = addressed || u.ID == a.bot.Account
	}
	if m.Referenced != nil {
		addressed = addressed || m.Referenced.Author.ID == a.bot.Account
	}
	headers := make(http.Header)
	if secret.InjectHeader(headers, "Authorization", "", a.token) != nil {
		return errUnavailable
	}
	m.Content = strings.ReplaceAll(m.Content, headers.Get("Authorization"), "[redacted]")
	reply := ""
	if m.Referenced != nil {
		reply = m.Referenced.ID
	}
	body, _ := json.Marshal(struct {
		ReplyTo string `json:"reply_to,omitempty"`
		ID      string `json:"message_id"`
		Text    string `json:"text"`
	}{reply, m.ID, m.Content})
	return in.Admit(ctx, gateway.Inbound{Address: addr, NativeID: id, Sender: m.Author.ID, Direct: m.Guild == "", Addressed: addressed, PreviousCursor: previous, Cursor: cursor, Body: body})
}
func (a *Adapter) button(ctx context.Context, q interaction, previous, cursor, token string) error {
	addr := q.Address
	queued := false
	err := a.db.Write(ctx, func(tx *canon.Tx) error {
		seen, err := channels.Seen(ctx, tx, a.bot, "interaction:"+q.ID)
		if err != nil || seen {
			return err
		}
		var row int64
		agent := ""
		if a.human(q.User) {
			b, err := channels.AdmitScope(ctx, tx, addr, q.User.ID, q.Guild == "", true)
			if err != nil && !errors.Is(err, channels.ErrDenied) && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && b.Scope.Tools && !b.Unreachable {
				data, _ := json.Marshal(q)
				receipt, err := inbox.Insert(ctx, tx, inbox.Input{Key: inbox.Key{Conversation: b.Conversation, Source: a.bot.Key() + ":approval", NativeID: q.ID}, Body: data})
				if err != nil {
					return err
				}
				row, agent = receipt.ID, b.Agent
				if _, err = tx.ExecContext(ctx, "INSERT INTO discord_buttons(inbox_id,bot,epoch) VALUES(?,?,?)", row, a.bot.Key(), a.epoch); err != nil {
					return err
				}
				queued = true
			}
		}
		return channels.RecordReceipt(ctx, tx, addr, agent, "interaction:"+q.ID, previous, cursor, row)
	})
	if err != nil || !queued {
		return err
	}
	a.mu.Lock()
	a.interactions[q.ID] = token
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.interactions, q.ID); a.mu.Unlock() }()
	return a.resumeButtons(ctx)
}
