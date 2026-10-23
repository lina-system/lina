// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	workeracp "github.com/thisisjun786/lina/internal/core/worker/acp"
	"github.com/thisisjun786/lina/internal/core/worker/codex"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

type steeringCallKey struct{}

func steeringID(person, session, key string) string {
	return fmt.Sprintf("steering:%x", sha256.Sum256([]byte(person+"\x00"+session+"\x00"+key)))
}
func (e *workerSession) mailboxKey() string { return e.adapter.Name() + ":" + e.thread.ID }
func (e *workerSession) storeSteering(ctx context.Context, key, text string) (work.SteeringMessage, error) {
	return e.storeSteeringFrom(ctx, key, text, e.origin.agent, "agent")
}
func (e *workerSession) storeSteeringFrom(ctx context.Context, key, text, sender, kind string) (work.SteeringMessage, error) {
	p := worker.Delivery(e.adapter)
	return e.origin.work.StoreSteering(ctx, work.SteeringMessage{Sender: sender, SpeakerKind: kind, ID: steeringID(e.origin.person, e.mailboxKey(), key), Session: e.mailboxKey(), TaskID: e.thread.TaskID, Person: e.origin.person, Agent: e.origin.agent, Conversation: e.origin.conversation, Text: text, Tier: p.Tier, Mechanism: p.Mechanism, SentAt: time.Now().UTC()})
}
func (e *workerSession) steeringState(ctx context.Context, m work.SteeringMessage, state, reason string) error {
	if err := e.origin.work.SteeringState(ctx, m.Session, m.ID, state, reason, time.Now().UTC()); err != nil {
		return err
	}
	return e.steeringNotice(ctx, m.ID, state, reason)
}
func (e *workerSession) steeringNotice(ctx context.Context, id, state, reason string) error {
	text := "Worker message " + id + ": " + state
	if reason != "" {
		text += " (" + reason + ")"
	}
	parent := e.input
	if parent.Surface.Kind == "" {
		parent.Surface = ledger.Surface{Kind: "tui"}
	}
	return e.origin.mailboxNotice(ctx, parent, id+":"+state+":"+reason, text+".")
}

// Called with control held. The durable claim prevents an uncertain push from
// being replayed after a restart. A held unsent message can resume normally.
func (e *workerSession) deliverSteering(ctx context.Context, m work.SteeringMessage, snap worker.ThreadSnapshot, activity lease.Lease) (tools.State, error) {
	if m.State == "read" || m.State == "delivered" {
		activity.Release()
		return tools.Applied, nil
	}
	if m.State == "refused" {
		activity.Release()
		return tools.Failed, worker.ErrInputRefused
	}
	if m.Reason == "delivery_uncertain" {
		activity.Release()
		return tools.Unknown, errors.New("steering delivery requires reconciliation")
	}
	profile := worker.Delivery(e.adapter)
	reason := ""
	e.recoveryMu.Lock()
	rec := e.recovery
	e.recoveryMu.Unlock()
	switch {
	case e.taskControlled() && snap.State == worker.Idle:
		activity.Release()
		return tools.Applied, e.steeringState(ctx, m, "refused", "session_gone")
	case !profile.Available:
		reason = "inbox_unavailable"
	case snap.State == worker.Dead || snap.State == worker.Unknown || rec.State == "unknown" || rec.State == "retrying":
		reason = "session_down"
	case snap.State == worker.Waiting || len(snap.Pending) > 0:
		reason = "waiting_for_answer"
	case rec.State == "decision" || rec.State == "parked":
		reason = "recovery_waiting"
	case e.taskControlled() && snap.State != worker.Active:
		reason = "task_not_active"
	}
	if reason != "" {
		activity.Release()
		err := e.steeringState(ctx, m, "held", reason)
		return tools.Applied, err
	}
	// Turn and pull use the authenticated tool-result boundary while busy.
	if profile.Tier != "push" && snap.State != worker.Idle {
		activity.Release()
		return tools.Applied, nil
	}
	if err := e.origin.work.SteeringState(ctx, m.Session, m.ID, "held", "delivery_uncertain", time.Now().UTC()); err != nil {
		activity.Release()
		return tools.Failed, err
	}
	// The durable record keeps original text for the UI. Only the worker-bound
	// projection is framed; old records with no source stay system evidence.
	text := worker.FrameMessage(m.Sender, m.SpeakerKind, m.TaskID, m.ID, m.Text)
	state, err := e.inputWorker(ctx, snap, text, activity)
	if err != nil {
		if state == tools.Failed {
			if save := e.steeringState(context.WithoutCancel(ctx), m, "refused", "input_refused"); save != nil {
				return tools.Unknown, save
			}
		}
		return state, err
	}
	if err = e.steeringState(context.WithoutCancel(ctx), m, "delivered", ""); err != nil {
		return tools.Unknown, err
	}
	return tools.Applied, nil
}
func (e *workerSession) ownerSteering(ctx context.Context, key, text string) error {
	e.control.Lock()
	defer e.control.Unlock()
	m, err := e.storeSteeringFrom(ctx, key, text, e.origin.person, "person")
	if err != nil {
		return err
	}
	if m.TaskID != "" {
		if err := worker.ResetMessages(ctx, e.origin.db, m.TaskID, "person-steering:"+m.ID); err != nil {
			return err
		}
	}
	if m.State == "delivered" || m.State == "read" {
		return nil
	}
	if m.State == "refused" {
		return worker.ErrInputRefused
	}
	if m.Reason == "delivery_uncertain" {
		return errors.New("steering delivery requires reconciliation")
	}
	snap, err := e.adapter.Snapshot(ctx, e.thread)
	if err != nil {
		return e.steeringState(ctx, m, "held", "session_down")
	}
	var activity lease.Lease
	if snap.State == worker.Idle && !e.taskControlled() {
		activity, err = e.origin.activities.Acquire(e.origin.ctx, lease.Activity{Class: lease.Hard, Reason: "Worker follow-up"})
		if err != nil {
			return err
		}
	}
	state, err := e.deliverSteering(ctx, m, snap, activity)
	if err != nil {
		return err
	}
	if state != tools.Applied {
		return worker.ErrInputRefused
	}
	if m.Tier != "push" {
		current, err := e.origin.work.SteeringMessages(ctx, m.Session)
		if err != nil {
			return err
		}
		for _, message := range current {
			if message.ID == m.ID && message.State == "stored" {
				return e.steeringNotice(ctx, m.ID, "stored", "seen at the next step")
			}
		}
	}
	return nil
}

// Retry only known-unsent mail when a recovered binding emits its first event.
func (e *workerSession) resumeSteering(ctx context.Context) error {
	if e.origin.work == nil || worker.Delivery(e.adapter).Tier != "push" {
		return nil
	}
	e.control.Lock()
	defer e.control.Unlock()
	pending, err := e.origin.work.PendingSteering(ctx, e.mailboxKey())
	if err != nil {
		return err
	}
	for _, m := range pending {
		snap, err := e.adapter.Snapshot(ctx, e.thread)
		if err != nil {
			return err
		}
		// Recovery/task owners retain sole authority to start continuation turns.
		if snap.State != worker.Active {
			return nil
		}
		if _, err = e.deliverSteering(ctx, m, snap, lease.Lease{}); err != nil {
			return err
		}
	}
	return nil
}
func (e *workerSession) inboxTool(ctx context.Context, event worker.Event) (any, error) {
	e.control.Lock()
	defer e.control.Unlock()
	var args struct {
		Read  []string `json:"read"`
		Acted []string `json:"acted"`
	}
	if err := decodeWorkerArgs(event.ToolCall.Arguments, &args); err != nil {
		return nil, err
	}
	if err := e.origin.work.AcknowledgeSteering(ctx, e.mailboxKey(), args.Read, args.Acted, time.Now().UTC()); err != nil {
		return nil, err
	}
	for _, id := range append(args.Read, args.Acted...) {
		if err := e.steeringNotice(ctx, id, "read", ""); err != nil {
			return nil, err
		}
	}
	return map[string]bool{"accepted": true}, nil
}

// Append a separate content item so existing tool result shapes remain intact.
// Each claimed message is marked delivered only after Answer succeeds.
func (e *workerSession) mailboxReply(ctx context.Context, event worker.Event, raw json.RawMessage) (json.RawMessage, []work.SteeringMessage, error) {
	if e.origin.work == nil {
		return raw, nil, nil
	}
	messages, err := e.origin.work.SteeringMessages(ctx, e.mailboxKey())
	if err != nil {
		return nil, nil, err
	}
	profile := worker.Delivery(e.adapter)
	var selected []work.SteeringMessage
	unread := 0
	for _, m := range messages {
		if m.State == "read" || m.State == "refused" {
			continue
		}
		unread++
		if (profile.Tier == "turn" || event.ToolCall.Name == "check_inbox") && (m.State == "stored" || m.State == "held" && m.Reason != "delivery_uncertain") {
			if err = e.origin.work.SteeringState(ctx, m.Session, m.ID, "held", "delivery_uncertain", time.Now().UTC()); err != nil {
				return nil, nil, err
			}
			selected = append(selected, m)
		}
	}
	if unread == 0 {
		return raw, nil, nil
	}
	framed := append([]work.SteeringMessage(nil), selected...)
	for i := range framed {
		m := &framed[i]
		m.Text = worker.FrameMessage(m.Sender, m.SpeakerKind, m.TaskID, m.ID, m.Text)
	}
	content, err := json.Marshal(map[string]any{"delivery": profile, "unread": unread, "steering": framed, "notice": "Read steering at this step. Call lina.check_inbox to fetch mail or acknowledge read IDs; report acted IDs after acting."})
	if err != nil {
		return nil, nil, err
	}
	var reply wire.DynamicToolCallResponse
	if err = json.Unmarshal(raw, &reply); err != nil {
		return nil, nil, err
	}
	reply.ContentItems = append(reply.ContentItems, wire.V2DynamicToolCallOutputContentItem{InputText: &wire.V2DynamicToolCallOutputContentItemInputText{Text: worker.FrameMessage("core", "tool", event.Thread.TaskID, e.requestKey(event.ToolCall.ID)+":mailbox", string(content))}})
	raw, err = json.Marshal(reply)
	return raw, selected, err
}

// A disconnected session retains its address. Only authoritative owner records
// can resolve it; knowing a session id never authorizes a mailbox write.
func (r *conversationRuntime) offlineSteering(ctx context.Context, person, session string, card *work.Card, conversation *string, key, text string) error {
	var message work.SteeringMessage
	reason := "session_down"
	if card != nil && card.Run != nil {
		if card.Task.PersonID != person || conversation != nil && *conversation != card.Task.ConversationID {
			return outsideManagement()
		}
		message = work.SteeringMessage{Session: card.Run.Worker + ":" + session, TaskID: card.Task.ID, Person: person, Agent: card.Task.AgentID, Conversation: card.Task.ConversationID, Tier: offlineDelivery(card.Run.Worker).Tier, Mechanism: offlineDelivery(card.Run.Worker).Mechanism}
		if card.State == "stopped" || card.State == "done" || !card.Active && card.Receipt != nil && (card.TurnStatus == "completed" || card.TurnStatus == "interrupted") {
			reason = "session_gone"
		}
	} else {
		records, err := (recovery.Recovery{DB: r.db}).Records(ctx)
		if err != nil {
			return err
		}
		for _, rec := range records {
			if rec.Thread.ID != session || rec.Person != person || conversation != nil && *conversation != rec.Conversation {
				continue
			}
			name := rec.Worker
			if name == "" {
				name = "codex"
			}
			message = work.SteeringMessage{Session: name + ":" + session, TaskID: rec.Thread.TaskID, Person: person, Agent: rec.Agent, Conversation: rec.Conversation, Tier: offlineDelivery(name).Tier, Mechanism: offlineDelivery(name).Mechanism}
			if rec.Canceled {
				reason = "session_gone"
			}
			break
		}
	}
	if message.Session == "" {
		return outsideManagement()
	}
	message.ID = steeringID(person, message.Session, key)
	message.Text = text
	message.Sender, message.SpeakerKind = person, "person"
	message.SentAt = time.Now().UTC()
	saved, err := r.work.StoreSteering(ctx, message)
	if err != nil {
		return err
	}
	if saved.TaskID != "" {
		if err := worker.ResetMessages(ctx, r.db, saved.TaskID, "person-steering:"+saved.ID); err != nil {
			return err
		}
	}
	if saved.Reason == "delivery_uncertain" || saved.State != "stored" && saved.State != "held" {
		return nil
	}
	state := "held"
	if reason == "session_gone" {
		state = "refused"
	}
	if err = r.work.SteeringState(ctx, message.Session, message.ID, state, reason, time.Now().UTC()); err != nil {
		return err
	}
	view := r
	if r.router != nil {
		view = newConversationView(r.router.root, message.Agent, message.Conversation)
	}
	return view.mailboxNotice(ctx, ledger.Event{Surface: ledger.Surface{Kind: "tui"}}, message.ID+":"+state+":"+reason, "Worker message "+message.ID+": "+state+" ("+reason+").")
}
func (e *workerSession) finishSteering(ctx context.Context, event worker.Event) error {
	if e.origin.work == nil || !e.taskControlled() || event.Finish == nil || (event.Finish.Status != worker.Done && event.Finish.Status != worker.Interrupted) || event.Thread != e.thread {
		return nil
	}
	pending, err := e.origin.work.PendingSteering(ctx, e.mailboxKey())
	if err != nil {
		return err
	}
	for _, m := range pending {
		if err = e.steeringState(ctx, m, "refused", "session_gone"); err != nil {
			return err
		}
	}
	return nil
}

func (r *conversationRuntime) mailboxNotice(ctx context.Context, parent ledger.Event, id, text string) error {
	_, err := r.workerDelivery().journal().Get(ctx, id)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ledger.ErrNotFound) {
		return err
	}
	return r.workerNotice(ctx, parent, id, text)
}

func offlineDelivery(name string) worker.DeliveryProfile {
	if name == "codex" {
		return (*codex.Session)(nil).DeliveryProfile()
	}
	for _, profile := range workeracp.BuiltinProfiles() {
		if profile.Name == name {
			return profile.Delivery
		}
	}
	return worker.DeliveryProfile{Tier: "pull", Mechanism: "waiting for session binding"}
}
