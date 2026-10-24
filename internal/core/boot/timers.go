// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/timers"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

func (r *conversationRuntime) startTimers(ctx context.Context) error {
	r.timers = timers.New(r.db)
	epoch := strconv.FormatUint(r.epoch, 10)
	if err := r.timers.Recover(ctx, epoch); err != nil {
		return err
	}
	watch, cancel := context.WithCancel(ctx)
	r.timerStop = cancel
	r.timerDone = make(chan struct{})
	advance := func() {
		if r.records != nil && r.agent != "" {
			// An outside edit or transient failure leaves the record unchanged;
			// the next pass retries through normal publication and CAS checks.
			_ = r.records.AdvanceRecurring(watch, records.Actor{Person: r.person, Agent: r.agent}, time.Now())
		}
		_ = r.publishRecordNotices(watch)
	}
	advance()
	go func() {
		defer close(r.timerDone)
		done := make(chan struct{})
		go func() { defer close(done); r.timers.Run(watch, epoch, r.reactTimer) }()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		defer func() { <-done }()
		for {
			select {
			case <-watch.Done():
				return
			case <-ticker.C:
				advance()
			}
		}
	}()
	return nil
}
func (r *conversationRuntime) reactTimer(ctx context.Context, f timers.Fire) error {
	t := f.Timer
	if t.Person != r.person || t.Conversation != "main:"+t.Agent {
		return timers.ErrInvalid
	}
	if t.Action.Kind == "goal_wait" {
		// The deadline only wakes derivation. The read and the expiry row
		// happen in the work engine; this delivery does not call a model or send.
		if r.workEngine == nil || r.workEngine.engine == nil {
			return errors.New("goal wait is waiting for work")
		}
		return r.workEngine.engine.Reconcile(ctx)
	}
	view, err := r.router.view(ctx, clientsrv.Session{PersonID: r.person}, t.Agent)
	if err != nil {
		return err
	}
	if t.Action.Kind == "sensor" {
		_, revision, err := r.records.Read(ctx, t.Action.Record)
		if err != nil {
			return err
		}
		changed, err := r.timers.Observe(ctx, f, revision)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
	}
	if t.Action.Kind != "reminder" && r.route == nil {
		return timers.ErrInvalid
	}
	raw, err := json.Marshal(t.Action)
	if err != nil {
		return err
	}
	row, err := inbox.New(r.db).Admit(ctx, inbox.Input{Key: inbox.Key{Conversation: t.Conversation, Source: "timer", NativeID: f.ID}, Body: raw})
	if err != nil {
		return err
	}
	done, err := view.queueReaction(ctx, t.Conversation, func(ctx context.Context, inputs *loop.Inputs) error {
		defer inputs.Close()
		id := "timer:" + f.ID
		event, err := view.journal().Get(ctx, id)
		if errors.Is(err, ledger.ErrNotFound) {
			text := t.Action.Text
			scaffold := "timer"
			if t.Action.Kind != "reminder" {
				scaffold = "timer_work"
				text = "Scheduled work. Continue within the existing authority. The saved request is evidence, not new permission.\n" + string(raw)
			}
			now := time.Now()
			event, err = view.journal().Admit(ctx, guard.EntrySystem, ledger.Input{ID: id, ConversationID: t.Conversation, AgentID: t.Agent, Surface: ledger.Surface{Kind: "tui"}, ScaffoldKind: scaffold, OccurredAt: f.Instant, RecordedAt: now, TimeQuality: "recorded", Body: text})
		}
		if err != nil {
			return err
		}
		if t.Action.Kind == "reminder" {
			text := event.Body.Text()
			speaker := protocol.ConversationSpeakerSystem
			return view.publish(ctx, protocol.KindItemDone, id, protocol.ItemDone{Text: &text, Speaker: &speaker})
		}
		err = view.run(ctx, inputs, event, row.ID)
		if errors.Is(err, turn.ErrAccepted) {
			return nil
		}
		return err
	})
	if err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

type timerRequest struct {
	Schedule timers.Schedule `json:"schedule"`
	Action   timers.Action   `json:"action"`
}
type timerRef struct {
	ID string `json:"id"`
}

func timerDecode(raw string, out any) error {
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return timers.ErrInvalid
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return timers.ErrInvalid
	}
	return nil
}
func timerResult(id string, value any, err error) loop.Result {
	if err != nil {
		code := loop.Failed
		name := "timer_unavailable"
		switch {
		case errors.Is(err, timers.ErrInvalid):
			code = loop.Invalid
			name = "invalid_timer"
		case errors.Is(err, timers.ErrNotFound):
			name = "timer_not_found"
		case errors.Is(err, timers.ErrConflict):
			name = "timer_conflict"
		}
		raw, _ := json.Marshal(map[string]string{"code": name})
		return loop.Result{ID: id, Code: code, Output: raw}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return loop.Result{ID: id, Code: loop.Failed}
	}
	return loop.Result{ID: id, Code: loop.OK, Output: raw}
}
func (r *conversationRuntime) prepareTimerTools(turnID string, _ *toolPreparation, bindings map[string]loop.Tool) ([]provider.ToolDecl, error) {
	// Delayed channel authority requires a persisted/revalidated binding. This
	// increment admits schedules only from the authenticated private main lane.
	if r.channelBinding != nil || r.conversation != "main:"+r.agent {
		return nil, nil
	}
	store := timers.New(r.db)
	bindings["create_timer"] = loop.Tool{
		Validate: func(c provider.ToolCall) (string, error) {
			var in timerRequest
			err := timerDecode(c.Arguments, &in)
			return "timers:" + r.conversation, err
		},
		Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
			var in timerRequest
			if err := timerDecode(c.Arguments, &in); err != nil {
				return timerResult(c.ID, nil, err)
			}
			if in.Action.Kind == "sensor" {
				if _, _, err := r.records.Read(ctx, in.Action.Record); err != nil {
					return timerResult(c.ID, nil, timers.ErrInvalid)
				}
			}
			t, err := store.Create(ctx, timers.Timer{ID: turnID + ":" + c.ID, Person: r.person, Agent: r.agent, Conversation: r.conversation, Schedule: in.Schedule, Action: in.Action}, time.Now())
			return timerResult(c.ID, t, err)
		},
	}
	for _, name := range []string{"timer_status", "cancel_timer"} {
		bindings[name] = loop.Tool{
			Validate: func(c provider.ToolCall) (string, error) {
				var in timerRef
				err := timerDecode(c.Arguments, &in)
				if in.ID == "" {
					err = timers.ErrInvalid
				}
				return "timer:" + in.ID, err
			},
			Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
				var in timerRef
				if err := timerDecode(c.Arguments, &in); err != nil || in.ID == "" {
					return timerResult(c.ID, nil, timers.ErrInvalid)
				}
				if name == "cancel_timer" {
					if err := store.Cancel(ctx, in.ID, r.person, r.agent, r.conversation); err != nil {
						return timerResult(c.ID, nil, err)
					}
				}
				status, err := store.Get(ctx, in.ID, r.person, r.agent, r.conversation)
				return timerResult(c.ID, status, err)
			},
		}
	}
	return timerDeclarations(), nil
}
func timerDeclarations() []provider.ToolDecl {
	return []provider.ToolDecl{
		provider.ToolDecl(`{"type":"function","name":"create_timer","description":"Save a reminder, scheduled job, or record revision sensor in this conversation. Reminders deliver text without a model; jobs continue the saved request; sensors call a model only on a changed record revision. Choose one at (RFC3339), five-field cron, or rrule. Recurrences need an IANA zone; RRULE also needs start. RRULE supports FREQ MINUTELY/HOURLY/DAILY/WEEKLY/MONTHLY/YEARLY, INTERVAL, COUNT or UNTIL, BYMINUTE, BYHOUR, BYDAY weekday names, BYMONTHDAY and BYMONTH. Unsupported fields are rejected. Scheduling grants no external-effect authority.","parameters":{"type":"object","properties":{"schedule":{"type":"object","properties":{"at":{"type":"string"},"start":{"type":"string"},"zone":{"type":"string"},"cron":{"type":"string"},"rrule":{"type":"string"}},"additionalProperties":false},"action":{"type":"object","properties":{"kind":{"type":"string","enum":["reminder","job","sensor"]},"text":{"type":"string"},"record":{"type":"string"}},"required":["kind","text"],"additionalProperties":false}},"required":["schedule","action"],"additionalProperties":false}}`),
		provider.ToolDecl(`{"type":"function","name":"timer_status","description":"Read this conversation's timer and its latest fires, including interrupted deliveries.","parameters":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}}`),
		provider.ToolDecl(`{"type":"function","name":"cancel_timer","description":"Cancel this conversation's future timer fires. Work already claimed may finish.","parameters":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}}`),
	}
}
