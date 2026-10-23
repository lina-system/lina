// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/guard"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type acceptedSteering struct {
	event   ledger.Event
	inboxID int64
	runner  *conversationRuntime
}

// submitInput runs under the shared conversation's admission mutex. Channel
// adapters reuse this coordinator, including steering during a live turn.
func (r *conversationRuntime) submitInput(event ledger.Event, inboxID int64, steer bool, runner *conversationRuntime) error {
	items, err := assemble.History([]ledger.Event{event}, r.conversation, r.agent)
	if err != nil {
		return err
	}
	if len(items) != 1 {
		return assemble.ErrHistory
	}
	body := items[0]
	if steer && r.lanes.Steer(r.conversation, body) {
		r.steering[string(body)] = append(r.steering[string(body)], acceptedSteering{event: event, inboxID: inboxID, runner: runner})
		return nil
	}
	_, err = r.queueReaction(r.ctx, r.conversation, func(ctx context.Context, inputs *loop.Inputs) error { return runner.run(ctx, inputs, event, inboxID) })
	return err
}

// Drained steering belongs to this turn; only the queue's final undrained
// items are resubmitted as follow-ups, with their original durable identity.
type conversationInputs struct {
	bindingTurn *turnState
	runtime     *conversationRuntime
	inputs      *loop.Inputs
	failure     error
	questions   *questionTurn
}

func (q *conversationInputs) Drain() []provider.Item {
	items := q.inputs.Drain()
	var out []provider.Item
	for _, body := range items {
		out = append(out, body)
		accepted, ok := q.runtime.takeSteering(body)
		if !ok {
			continue
		}
		if q.bindingTurn != nil {
			if q.bindingTurn.bindingInputs == nil {
				q.bindingTurn.bindingInputs = map[string]acceptedSteering{}
			}
			q.bindingTurn.bindingInputs[accepted.event.ID] = accepted
		}
		runner := accepted.runner
		if runner == nil {
			runner = q.runtime
		}
		packet, err := runner.inputBindingPacket(q.runtime.ctx, accepted.event)
		if err != nil {
			q.failure = err
		} else {
			for _, item := range packet {
				message, err := guard.Admit(guard.EntryTool, item.Origin, item.Text)
				if err != nil {
					q.failure = err
					continue
				}
				evidence, _ := message.Evidence()
				raw, _ := json.Marshal(struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				}{evidence.Role, evidence.Text})
				out = append(out, raw)
			}
		}
		if accepted.event.Surface.Kind == "channel" {
			if err := q.runtime.db.Write(q.runtime.ctx, func(tx *canon.Tx) error {
				_, err := tx.ExecContext(q.runtime.ctx, "INSERT INTO channel_steering(inbox_id) VALUES(?) ON CONFLICT DO NOTHING", accepted.inboxID)
				return err
			}); err != nil {
				q.failure = err
			}
		}
		if q.questions != nil {
			if err := q.questions.consume(q.runtime.ctx, accepted); err != nil {
				q.failure = err
			}
		}
	}
	return out
}

func (q *conversationInputs) Close() []provider.Item { return q.inputs.Close() }

func (r *conversationRuntime) takeSteering(body provider.Item) (acceptedSteering, bool) {
	if r.coordinator != nil {
		return r.coordinator.takeSteering(body)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pending := r.steering[string(body)]
	if len(pending) == 0 {
		return acceptedSteering{}, false
	}
	first := pending[0]
	if len(pending) == 1 {
		delete(r.steering, string(body))
	} else {
		r.steering[string(body)] = pending[1:]
	}
	return first, true
}
