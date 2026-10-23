// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/binding"
	"os"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/intakereplay"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
)

// ReplayIntakeCase runs the real Core conversation runtime in a fresh private
// root. No existing install, provider credentials or worker process is used.
// Fixture work is a snapshot, not a request to execute that work.
func ReplayIntakeCase(ctx context.Context, c intakereplay.Case, router string) ([]intakereplay.Binding, error) {
	return replayIntakeCase(ctx, c, router, nil)
}

// ReplayIntakeModelCase uses an explicit model route and only the real binding
// tool. Private text goes only to that configured route; work never executes.
func ReplayIntakeModelCase(ctx context.Context, c intakereplay.Case, route *modelpath.Config) ([]intakereplay.Binding, error) {
	if route == nil {
		return nil, errors.New("intake model route is not configured")
	}
	return replayIntakeCase(ctx, c, "core-model", route)
}
func replayIntakeCase(ctx context.Context, c intakereplay.Case, router string, route *modelpath.Config) (result []intakereplay.Binding, err error) {
	if reason := intakereplay.Validate(c); reason != "" {
		return nil, errors.New(reason)
	}
	if router != "focus-only" && router != "rules-then-focus" && router != "core-rules" && router != "core-model" {
		return nil, errors.New("unknown router")
	}
	root, err := os.MkdirTemp("", "lina-ir-")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(root)) }()
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		return nil, err
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		return nil, err
	}
	runtimeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r, err := startConversation(runtimeCtx, root, "intake-replay", 1, ctl, nil, lifecycle.NewRegistry(nil), "replay-person", nil, nil, "")
	if err != nil {
		return nil, err
	}
	defer func() {
		cancel()
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		err = errors.Join(err, r.Close(cleanup))
	}()
	if err = r.seedIntakeReplay(ctx, *c.State, c.Input); err != nil {
		return nil, err
	}
	savedID := "replay-binding"
	r.intakeSnapshot = router == "core-rules" || router == "core-model"
	r.intakeReplay = func(ctx context.Context, event ledger.Event) error {
		if router == "core-model" {
			savedID = inputBindingID(event.ID)
			return r.replayModelBinding(ctx, event, route)
		}
		if router == "core-rules" {
			savedID = inputBindingID(event.ID)
			return nil
		}
		state, input, err := r.readIntakeReplay(ctx)
		if err != nil {
			return err
		}
		// The admitted, masked input is what the decider sees, not a copy of gold.
		input.Text = event.Body.Text()
		bindings := intakereplay.Decide(router, state, input)
		_, err = r.intakeEvent(ctx, "replay-binding", event.ID, "intake_replay_binding", bindings, state.Clock)
		return err
	}
	id := "replay-input"
	if _, err = r.Input(ctx, clientsrv.Session{PersonID: r.person, Epoch: r.epoch}, protocol.Input{InputID: &id, Text: &c.Input.Text}); err != nil {
		return nil, err
	}
	saved, err := r.journal().Get(ctx, savedID)
	if err != nil {
		return nil, err
	}
	parent, err := r.journal().Get(ctx, saved.ParentID)
	if err != nil {
		return nil, err
	}
	if saved.SpeakerKind != guard.System || parent.PersonID != r.person || parent.Origin.Locator != id {
		return nil, errors.New("invalid replay binding provenance")
	}
	if router == "core-model" {
		choices, e := r.currentInputChoices(ctx, parent)
		if e != nil {
			return nil, e
		}
		for _, choice := range choices {
			result = append(result, intakereplay.Binding{Target: choice.Target, Ask: choice.Question != "", Decider: choice.Decider})
		}
		return result, nil
	}
	if router == "core-rules" {
		var d binding.Decision
		if err = json.Unmarshal([]byte(saved.Body.Text()), &d); err != nil {
			return nil, err
		}
		ids := make([]string, len(d.Candidates))
		for n, i := range d.Candidates {
			ids[n] = i.ID
		}
		return []intakereplay.Binding{{Target: d.Target, Decider: d.Decider, Candidates: ids}}, nil
	}
	err = json.Unmarshal([]byte(saved.Body.Text()), &result)
	return result, err
}

func (r *conversationRuntime) intakeEvent(ctx context.Context, id, parent, kind string, value any, at time.Time) (ledger.Event, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return ledger.Event{}, err
	}
	return r.journal().Admit(ctx, guard.EntrySystem, ledger.Input{ID: id, ParentID: parent, ConversationID: r.conversation, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui"}, ScaffoldKind: kind, OccurredAt: at, RecordedAt: at, TimeQuality: "recorded", Body: string(raw)})
}

type intakeContext struct {
	State intakereplay.State `json:"state"`
	Input intakereplay.Input `json:"input"`
}

func (r *conversationRuntime) seedIntakeReplay(ctx context.Context, s intakereplay.State, in intakereplay.Input) error {
	for n, i := range s.Items {
		switch i.Kind {
		case "task":
			task := work.Task{ID: i.ID, PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: i.Title, Done: "Fixture snapshot"}
			if i.State == "needs_input" {
				parent := task
				parent.ID = "replay-parent:" + i.ID
				if err := r.work.Create(ctx, parent); err != nil {
					return err
				}
				task.GoalID = parent.ID
			}
			if err := r.work.Create(ctx, task); err != nil {
				return err
			}
			if i.State == "running" {
				if err := r.work.Start(ctx, work.Run{ID: "replay-run:" + i.ID, TaskID: i.ID, Generation: 1, Worker: "fixture", ThreadID: "fixture", TurnID: "fixture", NodeID: "fixture"}); err != nil {
					return err
				}
			}
			if i.State == "needs_input" {
				round := work.QuestionRound{ID: "replay-round:" + i.ID, Goal: task.GoalID, Person: r.person, Agent: r.agent, Conversation: r.conversation, Questions: []work.PersonQuestion{{ID: "input", Dependents: []string{i.ID}}}}
				if err := r.db.Write(ctx, func(tx *canon.Tx) error { return r.work.OpenQuestionRound(ctx, tx, round) }); err != nil {
					return err
				}
			}
		case "run":
			state := runs.Waiting
			if i.State == "running" {
				state = runs.Working
			}
			if i.State == "needs_input" {
				state = runs.NeedsInput
			}
			record := runs.Record{ID: i.ID, Origin: runs.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Epoch: r.epoch}, Brief: runs.Brief{Goal: i.Title}, State: state, Created: i.LastActivity.UnixNano(), Updated: i.LastActivity.UnixNano()}
			raw, err := json.Marshal(record)
			if err != nil {
				return err
			}
			if err = r.db.Write(ctx, func(tx *canon.Tx) error {
				_, e := tx.ExecContext(ctx, "INSERT INTO background_runs(id,record) VALUES(?,?)", i.ID, raw)
				return e
			}); err != nil {
				return err
			}
		default:
			data := fmt.Sprintf("---\nid: %q\nkind: %s\nstate: %s\nperson_id: %q\nagent_id: %q\n---\n\n# %s\n", i.ID, i.Kind, i.State, r.person, r.agent, i.Title)
			r.records.Now = func() time.Time { return i.LastActivity }
			if _, err := r.records.Commit(ctx, records.Change{ID: i.ID, Path: fmt.Sprintf("inbox/fixture-%d.md", n), Data: []byte(data)}, records.Actor{Person: r.person, Agent: r.agent}); err != nil {
				return err
			}
		}
	}
	// Questions and surface links have their own ledger identities. The replay
	// context preserves clock/focus and names, which have no production store yet.
	for n, q := range s.Questions {
		if _, err := r.intakeEvent(ctx, fmt.Sprintf("replay-question-%d", n), "", "intake_replay_question", q, s.Clock); err != nil {
			return err
		}
	}
	for n, turn := range in.Recent {
		entry := guard.EntryOwner
		person := r.person
		if turn.Role == "agent" {
			entry = guard.EntryAgent
			person = ""
		}
		if _, err := r.journal().Admit(ctx, entry, ledger.Input{ID: fmt.Sprintf("replay-history-%d", n), PersonID: person, ConversationID: r.conversation, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: s.Clock.Add(-time.Duration(in.PreviousTurnSeconds) * time.Second), RecordedAt: s.Clock, TimeQuality: "recorded", Body: turn.Text}); err != nil {
			return err
		}
	}
	_, err := r.intakeEvent(ctx, "replay-context", "", "intake_replay_context", intakeContext{s, in}, s.Clock)
	return err
}
func (r *conversationRuntime) readIntakeReplay(ctx context.Context) (intakereplay.State, intakereplay.Input, error) {
	event, err := r.journal().Get(ctx, "replay-context")
	if err != nil {
		return intakereplay.State{}, intakereplay.Input{}, err
	}
	var stored intakeContext
	if err = json.Unmarshal([]byte(event.Body.Text()), &stored); err != nil {
		return stored.State, stored.Input, err
	}
	s := &stored.State
	for n := range s.Items {
		i := &s.Items[n]
		switch i.Kind {
		case "task":
			card, e := r.work.Get(ctx, i.ID)
			if e != nil {
				return *s, stored.Input, e
			}
			i.Title = card.Task.Title
			i.State = "waiting"
			if card.Active {
				i.State = "running"
			}
			for _, w := range card.Waits {
				if w.Held {
					i.State = "needs_input"
				}
			}
		case "run":
			var row runs.ListItem
			err = r.db.Read(ctx, func(tx *canon.Tx) error { var e error; row, e = (runs.RunListSource{}).Read(ctx, tx, i.ID); return e })
			if err != nil {
				return *s, stored.Input, err
			}
			i.Title = row.Summary
			i.State = row.State
			if row.State == "working" {
				i.State = "running"
			}
		default:
			row, e := r.records.ViewRecord(ctx, r.person, i.ID)
			if e != nil {
				return *s, stored.Input, e
			}
			i.Title = row.Title
			i.Kind = row.Kind
			i.State = row.State
		}
	}
	for n := range s.Questions {
		e, err := r.journal().Get(ctx, fmt.Sprintf("replay-question-%d", n))
		if err != nil {
			return *s, stored.Input, err
		}
		if err = json.Unmarshal([]byte(e.Body.Text()), &s.Questions[n]); err != nil {
			return *s, stored.Input, err
		}
	}
	return *s, stored.Input, nil
}

// The replay's fixed clock, prior focus and native surface/question metadata
// are stored facts. Work titles and states still come from the actual stores.
func replayBindingMetadata(ctx context.Context, tx *canon.Tx, state *binding.State, input *binding.Input) error {
	var raw string
	if err := tx.QueryRowContext(ctx, "SELECT body FROM conv_event WHERE id='replay-context'").Scan(&raw); err != nil {
		return err
	}
	var stored intakeContext
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return err
	}
	state.Clock = stored.State.Clock
	state.Focus = stored.State.Focus
	state.FocusAgeSeconds = stored.State.FocusAgeSeconds
	state.Surfaces = stored.State.Surfaces
	input.ReplyTo, input.Card, input.Thread = stored.Input.ReplyTo, stored.Input.Card, stored.Input.Thread
	state.Questions = nil
	for n := range stored.State.Questions {
		if err := tx.QueryRowContext(ctx, "SELECT body FROM conv_event WHERE id=?", fmt.Sprintf("replay-question-%d", n)).Scan(&raw); err != nil {
			return err
		}
		var q binding.Question
		if err := json.Unmarshal([]byte(raw), &q); err != nil {
			return err
		}
		state.Questions = append(state.Questions, q)
	}
	visible := state.Items[:0]
	for _, item := range state.Items {
		for _, saved := range stored.State.Items {
			if item.ID == saved.ID {
				visible = append(visible, item)
				break
			}
		}
	}
	state.Items = visible
	for n := range state.Items {
		for _, saved := range stored.State.Items {
			if saved.ID == state.Items[n].ID {
				state.Items[n].Names = saved.Names
				state.Items[n].Links = saved.Links
			}
		}
	}
	return nil
}
