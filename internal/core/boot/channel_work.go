// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/gateway/discord"
	"github.com/thisisjun786/lina/internal/core/gateway/telegram"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
)

type channelInput struct {
	id                          int64
	native, agent, conversation string
	body                        []byte
}

// The inbox is authoritative. Scanning recovers the gap between admission and
// lane submission; turn.Run's durable claim prevents re-executing a started turn.
func (c *channelRuntime) runChannelWork(ctx context.Context, bot channels.Bot, adapter channelAdapter) {
	for ctx.Err() == nil {
		if c.r.route != nil {
			_ = c.channelWork(ctx, bot, adapter)
		}
		if channelPause(ctx, 100*time.Millisecond) != nil {
			return
		}
	}
}
func (c *channelRuntime) channelWork(ctx context.Context, bot channels.Bot, adapter channelAdapter) error {
	var inputs []channelInput
	err := c.r.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT i.id,r.native_id,r.agent,i.conversation,i.body FROM channel_receipts r JOIN inbox i ON i.id=r.inbox_id WHERE r.bot=? AND i.source=? ORDER BY i.id`, bot.Key(), bot.Key())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var in channelInput
			if err := rows.Scan(&in.id, &in.native, &in.agent, &in.conversation, &in.body); err != nil {
				return err
			}
			inputs = append(inputs, in)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, in := range inputs {
		var event gateway.Inbound
		if json.Unmarshal(in.body, &event) != nil {
			return channels.ErrInvalid
		}
		binding, err := c.store.Binding(ctx, event.Address)
		if err != nil || binding.Agent != in.agent || binding.Conversation != in.conversation || binding.Unreachable {
			continue
		}
		var current channels.Binding
		if err = c.r.db.Read(ctx, func(tx *canon.Tx) error {
			var err error
			current, err = channels.AdmitScope(ctx, tx, event.Address, event.Sender, event.Direct, event.Addressed)
			return err
		}); err != nil {
			continue
		}
		view := newConversationView(c.r, current.Agent, current.Conversation)
		view.channelBinding = &current
		id := "channel-input:" + strconv.FormatInt(in.id, 10)
		var started int
		if err = c.r.db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT count(*) FROM turn_runs WHERE inbox_id=?", in.id).Scan(&started)
		}); err != nil {
			return err
		}
		if started == 0 {
			var body struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(event.Body, &body) != nil {
				return channels.ErrInvalid
			}
			input, err := view.journal().Get(ctx, id)
			if errors.Is(err, sql.ErrNoRows) {
				now := time.Now()
				input, err = view.journal().Admit(ctx, guard.EntryOwner, ledger.Input{ID: id, ConversationID: current.Conversation, PersonID: current.Person, AgentID: current.Agent, Surface: ledger.Surface{Kind: "channel", Platform: bot.Platform, ChatID: event.Chat}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Locator: in.native, Body: body.Text})
			}
			if err != nil {
				return err
			}
			done, err := view.lanes.SubmitLoop(ctx, current.Conversation, func(ctx context.Context, inputs *loop.Inputs) error { return view.run(ctx, inputs, input, in.id) })
			if err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-done:
			}
		}
		if err = c.channelRunResults(ctx, view, id); err != nil {
			return err
		}
		if err = c.channelReplies(ctx, view, event, in, id, adapter); err != nil {
			return err
		}
	}
	return nil
}
func (c *channelRuntime) channelReplies(ctx context.Context, view *conversationRuntime, event gateway.Inbound, in channelInput, parent string, adapter channelAdapter) error {
	var ids []string
	if err := c.r.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM conv_event WHERE parent_id=? AND speaker_kind='agent' AND scaffold_kind IS NULL ORDER BY rowid", parent)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	}); err != nil {
		return err
	}
	for _, id := range ids {
		item, err := view.journal().Get(ctx, id)
		if err != nil {
			return err
		}
		shape := telegram.Shape
		if event.Platform == "discord" {
			shape = discord.Shape
		}
		parts, err := shape(item.Body.Text(), "The full reply is available in LINA.", "lina://conversation/"+view.conversation)
		if err != nil {
			return err
		}
		if err = c.store.Enqueue(ctx, channels.Message{ID: "channel-reply:" + id, Address: event.Address, Agent: in.agent, LedgerRef: id, ReplyTo: in.native, Parts: parts}); err != nil {
			return err
		}
	}
	l, err := c.r.activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "channel delivery"})
	if err != nil {
		return err
	}
	defer l.Release()
	queue := gateway.NewQueue(c.r.db, adapter)
	for {
		sent, err := queue.SendNext(ctx, l, event.Address, time.Now())
		if err != nil {
			return err
		}
		if !sent {
			return nil
		}
	}
}
func (r *conversationRuntime) validateChannel(ctx context.Context) error {
	if r.channelBinding == nil {
		return nil
	}
	b, err := channels.New(r.db).Binding(ctx, r.channelBinding.Address)
	if err != nil {
		return err
	}
	if b != *r.channelBinding || b.Unreachable {
		return channels.ErrDenied
	}
	return nil
}

// Delivered notices establish the turn boundary. Project each landed artifact
// once into the agent's channel ledger, then let the same durable queue send it
// as a new message. An unknown send never repeats on restart.
func (c *channelRuntime) channelRunResults(ctx context.Context, view *conversationRuntime, parent string) error {
	deliveries, err := c.r.background.notices.Delivered(ctx, view.conversation)
	if err != nil {
		return err
	}
	for _, d := range deliveries {
		for _, n := range d.Notices {
			record, err := c.r.background.service.Get(ctx, n.Run)
			if err != nil {
				return err
			}
			if record.Origin.Turn != "turn:"+parent || record.Origin.Agent != view.agent || record.Origin.Person != view.person {
				continue
			}
			id := "channel-result:" + n.ID
			if _, err = view.journal().Get(ctx, id); err == nil {
				continue
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			text := n.Summary
			if n.Result != "" {
				body, next, err := c.r.background.service.ResultPage(ctx, view.person, n.Run, 0)
				if err != nil {
					return err
				}
				if next != 0 {
					body = "The full result is available in LINA.\nlina://result/" + n.Result
				}
				text += "\n\n" + body
			}
			b := view.channelBinding
			_, err = view.journal().Admit(ctx, guard.EntryAgent, ledger.Input{ID: id, ParentID: parent, ConversationID: view.conversation, AgentID: view.agent, Surface: ledger.Surface{Kind: "channel", Platform: b.Platform, ChatID: b.Chat}, OccurredAt: n.Landed, RecordedAt: n.Landed, TimeQuality: "recorded", Locator: n.ID, Body: text})
			if err != nil {
				return err
			}
		}
	}
	return nil
}
