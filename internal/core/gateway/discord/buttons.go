// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/kit/lease"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var buttonID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,62}$`)

func (a *Adapter) PresentApproval(ctx context.Context, l lease.Lease, addr channels.Address, id string) (result gateway.SendResult, err error) {
	channel, err := a.destination(addr)
	if err != nil {
		return result, err
	}
	if a.approvals == nil {
		return result, authority.ErrNoSurface
	}
	request, err := a.approvals.Approval(ctx, id)
	if err != nil {
		return result, err
	}
	b, err := a.store.Binding(ctx, addr)
	if err != nil || b.Unreachable || !b.Scope.Tools || request.Person != b.Person || request.Agent != b.Agent {
		return result, channels.ErrDenied
	}
	if request.Epoch != a.epoch || request.State != authority.Pending || !buttonID.MatchString(id) {
		return result, authority.ErrStale
	}
	text := fmt.Sprintf("%s\nWhere: %s\nCan be undone: %t\nArguments: %s", request.What, request.Where, request.Undoable, request.Arguments)
	if units(text) > 2000 {
		return gateway.SendResult{Outcome: gateway.TooLong}, nil
	}
	components := []any{map[string]any{"type": 1, "components": []any{map[string]any{"type": 2, "style": 3, "label": "Allow once", "custom_id": id + ":o"}, map[string]any{"type": 2, "style": 4, "label": "Deny", "custom_id": id + ":d"}}}}
	err = lease.Do(ctx, l, func(ctx context.Context) error {
		var err error
		result, err = a.send(ctx, channel, text, components)
		if err == nil && result.Outcome == gateway.Delivered {
			settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err = a.db.Write(settle, func(tx *canon.Tx) error {
				_, err := tx.ExecContext(settle, "INSERT INTO discord_presentations(bot,chat,topic,message_id,approval_id) VALUES(?,?,?,?,?)", a.bot.Key(), addr.Chat, addr.Topic, result.MessageID, id)
				return err
			}); err != nil {
				result.Outcome = gateway.Uncertain
				return errUnavailable
			}
		}
		return err
	})
	return
}
func (a *Adapter) resumeButtons(ctx context.Context) error {
	type pending struct {
		id    int64
		epoch string
		body  []byte
	}
	var rows []pending
	if err := a.db.Read(ctx, func(tx *canon.Tx) error {
		r, err := tx.QueryContext(ctx, "SELECT b.inbox_id,b.epoch,i.body FROM discord_buttons b JOIN inbox i ON i.id=b.inbox_id WHERE b.bot=? AND b.done=0 ORDER BY b.inbox_id", a.bot.Key())
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
	}); err != nil {
		return err
	}
	for _, p := range rows {
		var q interaction
		if json.Unmarshal(p.body, &q) != nil {
			return errResponse
		}
		accepted := false
		note := "This button is unavailable."
		if p.epoch == strconv.FormatUint(a.epoch, 10) {
			accepted, note = a.decide(ctx, q)
		}
		if err := a.db.Write(ctx, func(tx *canon.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE discord_buttons SET done=1 WHERE inbox_id=?", p.id)
			return err
		}); err != nil {
			return err
		}
		if accepted {
			_ = a.Answer(ctx, gateway.InteractionRef{Bot: a.bot, ID: q.ID}, note)
			_ = a.Edit(ctx, gateway.MsgRef{Address: q.Address, ID: q.Message.ID}, gateway.Outbound{Text: note})
		}
	}
	return nil
}
func (a *Adapter) decide(ctx context.Context, q interaction) (bool, string) {
	refused := "This button is unavailable."
	id, letter, ok := strings.Cut(q.Data.Custom, ":")
	if !ok || !buttonID.MatchString(id) || (letter != "o" && letter != "d") || a.approvals == nil || q.Message.Author.ID != a.bot.Account || !q.Message.Author.Bot || !snowflake.MatchString(q.Message.ID) {
		return false, refused
	}
	addr := q.Address
	var b channels.Binding
	err := a.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		b, err = channels.AdmitScope(ctx, tx, addr, q.User.ID, q.Guild == "", true)
		if err != nil {
			return err
		}
		if !b.Scope.Tools || b.Unreachable {
			return channels.ErrDenied
		}
		var n int
		err = tx.QueryRowContext(ctx, "SELECT count(*) FROM discord_presentations WHERE bot=? AND chat=? AND topic=? AND message_id=? AND approval_id=?", a.bot.Key(), addr.Chat, addr.Topic, q.Message.ID, id).Scan(&n)
		if err == nil && n != 1 {
			return channels.ErrDenied
		}
		return err
	})
	if err != nil {
		return false, refused
	}
	approval, err := a.approvals.Approval(ctx, id)
	if err != nil || approval.Person != b.Person || approval.Agent != b.Agent || approval.Epoch != a.epoch || approval.State != authority.Pending {
		return false, refused
	}
	choice := authority.Once
	note := "Allowed once."
	if letter == "d" {
		choice = authority.Deny
		note = "Denied."
	}
	if err = a.approvals.Answer(ctx, id, authority.Decider{Person: b.Person, Epoch: a.epoch, Surface: authority.Channel, Authenticated: true, ChatID: q.Channel, MessageID: q.Message.ID}, authority.Decision{Choice: choice}); err != nil {
		return false, refused
	}
	return true, note
}
