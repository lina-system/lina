// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/reactor"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (r *conversationRuntime) startCompletions(ctx context.Context) error {
	flow := &reactor.Reactor{DB: r.db, Work: r.work, Epoch: strconv.FormatUint(r.epoch, 10), On: r.reactCompletion}
	// Repair before READY, even when no worker or model route is installed.
	if err := flow.Sweep(ctx); err != nil {
		return err
	}
	watch, cancel := context.WithCancel(ctx)
	r.completionStop, r.completionDone = cancel, make(chan struct{})
	go func() { defer close(r.completionDone); flow.Run(watch) }()
	return nil
}

// queueReaction is the one execution path for committed chat and work input.
// The inbox is durable; the lane serializes turns for the bound conversation.
func (r *conversationRuntime) queueReaction(ctx context.Context, conversation string, run func(context.Context, *loop.Inputs) error) (<-chan error, error) {
	return r.lanes.SubmitLoop(ctx, conversation, run)
}

func (r *conversationRuntime) reactCompletion(ctx context.Context, e work.Completion, card work.Card) error {
	if r.workerDispatch != nil {
		found, err := r.workerDispatch.React(ctx, e, card)
		if found || err != nil {
			return err
		}
	}
	// A missing installed adapter/model route must not consume its manager delivery.
	var dispatched int
	if err := r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM worker_dispatch WHERE id=?", e.TaskID).Scan(&dispatched)
	}); err != nil {
		return err
	}
	if dispatched != 0 {
		return work.ErrState
	}
	if card.Task.PersonID != "" && card.Task.PersonID != r.person {
		return work.ErrInvalid
	}
	if _, err := r.agents.Get(ctx, card.Task.AgentID); err != nil {
		return err
	}
	view := newConversationView(r.router.root, card.Task.AgentID, card.Task.ConversationID)
	if err := view.seedHistory(ctx); err != nil {
		return err
	}
	body, _ := json.Marshal(e)
	if _, err := inbox.New(r.db).Admit(ctx, inbox.Input{Key: inbox.Key{Conversation: view.conversation, Source: "work", NativeID: e.ID}, Body: body}); err != nil {
		return err
	}
	done, err := view.queueReaction(ctx, view.conversation, func(ctx context.Context, inputs *loop.Inputs) error {
		defer inputs.Close()
		current, err := r.work.Get(ctx, e.TaskID)
		if err != nil {
			return err
		}
		if current.Run == nil || current.Run.ID != e.Attempt || current.Head != e.Head {
			return nil
		}
		id := "work-completion:" + e.ID
		event, err := view.journal().Get(ctx, id)
		if errors.Is(err, ledger.ErrNotFound) {
			now := time.Now()
			text := "Work result received: " + current.Task.Title
			event, err = view.journal().Admit(ctx, guard.EntrySystem, ledger.Input{ID: id, ConversationID: view.conversation, AgentID: view.agent, Surface: ledger.Surface{Kind: "manager"}, ScaffoldKind: "worker_result", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text})
		}
		if err != nil {
			return err
		}
		text, speaker := event.Body.Text(), protocol.ConversationSpeakerSystem
		return view.publish(ctx, protocol.KindItemDone, event.ID, protocol.ItemDone{Text: &text, Speaker: &speaker})
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
