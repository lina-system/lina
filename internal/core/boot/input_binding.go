// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/work"
)

const inputBindingKind = "input_binding"

func inputBindingID(input string) string { return "binding:" + input }

// bindInput runs after the person's masked event is inserted and before its
// admission commits. No model, filesystem or platform operation runs here.
func (r *conversationRuntime) bindInput(ctx context.Context, tx *canon.Tx, input ledger.Event) error {
	if input.SpeakerKind != guard.Person {
		return nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM conv_event WHERE id=?", inputBindingID(input.ID)).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	state, err := r.bindingState(ctx, tx, input)
	if err != nil {
		return err
	}
	facts := binding.Input{Text: input.Body.DerivedText(), ReplyTo: input.Surface.ReplyTo, Card: input.Surface.Card, Thread: input.Surface.Thread}
	if r.intakeSnapshot {
		if err = replayBindingMetadata(ctx, tx, &state, &facts); err != nil {
			return err
		}
	}
	decision := binding.Resolve(state, facts)
	raw, err := json.Marshal(decision)
	if err != nil {
		return err
	}
	_, err = ledger.New(questionTransaction{tx}, nil).Admit(ctx, guard.EntrySystem, ledger.Input{
		ID: inputBindingID(input.ID), ParentID: input.ID, ConversationID: input.ConversationID, AgentID: input.AgentID,
		Surface: input.Surface, ScaffoldKind: inputBindingKind, OccurredAt: input.OccurredAt, RecordedAt: input.RecordedAt, TimeQuality: "recorded", Body: string(raw),
		Relations: []ledger.Relation{{TargetID: input.ID, Kind: "view_of"}},
	})
	return err
}

func (r *conversationRuntime) bindingState(ctx context.Context, tx *canon.Tx, input ledger.Event) (binding.State, error) {
	s := binding.State{Clock: input.RecordedAt, Focus: binding.Target{Class: "none"}, Surfaces: map[string]string{}}
	// A channel without personal-context access must not receive the app's work
	// queue or focus merely because it shares the main conversation identity.
	if r.channelBinding != nil && (!r.channelBinding.Scope.Memory || !r.channelBinding.Scope.Tools) {
		return s, nil
	}
	// Read the latest binding and exchange before this input, using ledger order.
	var raw, at string
	err := tx.QueryRowContext(ctx, `SELECT b.body,p.recorded_at FROM conv_event b JOIN conv_event p ON p.id=b.parent_id WHERE b.conversation_id=? AND b.agent_id=? AND p.person_id=? AND b.scaffold_kind=? AND p.rowid<(SELECT rowid FROM conv_event WHERE id=?) ORDER BY b.rowid DESC LIMIT 1`, input.ConversationID, input.AgentID, input.PersonID, inputBindingKind, input.ID).Scan(&raw, &at)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}
	if err == nil {
		var previous binding.Decision
		if err = json.Unmarshal([]byte(raw), &previous); err != nil {
			return s, err
		}
		s.Focus = previous.Target
		// Agent replies also keep an exchange fresh; tool/system traffic does not.
		var latest string
		err = tx.QueryRowContext(ctx, `SELECT recorded_at FROM conv_event WHERE conversation_id=? AND agent_id=? AND speaker_kind IN ('person','agent') AND rowid<(SELECT rowid FROM conv_event WHERE id=?) AND coalesce(scaffold_kind,'')<>'provider_item' ORDER BY rowid DESC LIMIT 1`, input.ConversationID, input.AgentID, input.ID).Scan(&latest)
		if err != nil {
			return s, err
		}
		at = latest
		last, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return s, err
		}
		s.FocusAgeSeconds = max(0, int64(input.RecordedAt.Sub(last)/time.Second))
	}
	// The person's work queue is shared by their agents. Record projections are
	// refreshed by the records writer before publication becomes visible.
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.title,t.kind,t.state,coalesce(a.last_activity,''),coalesce(v.fields,'{}') FROM records_text t LEFT JOIN records_activity a ON a.id=t.id LEFT JOIN records_todo v ON v.id=t.id WHERE t.person=? AND t.kind IN ('initiative','project','todo')`, input.PersonID)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var i binding.Item
		var stamp, fields string
		if err = rows.Scan(&i.ID, &i.Title, &i.Kind, &i.State, &stamp, &fields); err != nil {
			rows.Close()
			return s, err
		}
		if stamp != "" {
			i.LastActivity, err = time.Parse(time.RFC3339Nano, stamp)
			if err != nil {
				rows.Close()
				return s, err
			}
		}
		var todo records.Todo
		if err = json.Unmarshal([]byte(fields), &todo); err != nil {
			rows.Close()
			return s, err
		}
		for _, link := range []string{todo.Source, todo.Issue, todo.PullRequest} {
			if link != "" {
				i.Links = append(i.Links, link)
			}
		}
		s.Items = append(s.Items, i)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return s, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT c.record,coalesce((SELECT json_extract(e.record,'$.recorded_at') FROM work_ledger e WHERE e.task_id=c.task_id ORDER BY e.seq DESC LIMIT 1),'') FROM work_cards c WHERE json_extract(c.record,'$.task.person_id')=?`, input.PersonID)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var body []byte
		var activity string
		var c work.Card
		if err = rows.Scan(&body, &activity); err != nil {
			break
		}
		if err = json.Unmarshal(body, &c); err != nil {
			break
		}
		i := binding.Item{ID: c.Task.ID, Title: c.Task.Title, Kind: "task", State: c.State, LastActivity: c.StartedAt, StartedHere: c.Task.ConversationID == input.ConversationID}
		if c.Active {
			i.State = "running"
		}
		for _, w := range c.Waits {
			if w.Held {
				i.State = "needs_input"
			}
		}
		if i.State == "" || i.State == "pending" {
			i.State = "waiting"
		}
		if c.FinishedAt.After(i.LastActivity) {
			i.LastActivity = c.FinishedAt
		}
		if activity != "" {
			at, e := time.Parse(time.RFC3339Nano, activity)
			if e != nil {
				err = e
				break
			}
			if at.After(i.LastActivity) {
				i.LastActivity = at
			}
		}
		s.Items = append(s.Items, i)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return s, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT record FROM background_runs WHERE json_extract(record,'$.Origin.Person')=?`, input.PersonID)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var body []byte
		var c runs.Record
		if err = rows.Scan(&body); err != nil {
			break
		}
		if err = json.Unmarshal(body, &c); err != nil {
			break
		}
		s.Items = append(s.Items, binding.Item{ID: c.ID, Title: c.Brief.Goal, Kind: "run", State: string(c.State), LastActivity: time.Unix(0, c.Updated).UTC(), StartedHere: c.Origin.Conversation == input.ConversationID})
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return s, err
	}
	// Names use the same visibility, validity and retraction checks as recall.
	names, err := memory.New(questionTransaction{tx}).Names(ctx, memory.RecallScope{Person: input.PersonID, Agent: input.AgentID, ExternalModel: true}, input.RecordedAt.Unix())
	if err != nil {
		return s, err
	}
	for n := range s.Items {
		for _, hit := range names {
			if hit.Item.Subject.ID == s.Items[n].ID && hit.Item.Name != nil {
				s.Items[n].Names = append(s.Items[n].Names, hit.Item.Name.Current)
				s.Items[n].Names = append(s.Items[n].Names, hit.Item.Name.RetiredAliases...)
			}
		}
	}
	rounds, err := waiting.NewRounds(questionTransaction{tx}, waiting.RoundHooks{}).List(ctx, input.PersonID)
	if err != nil {
		return s, err
	}
	for _, round := range rounds {
		if round.Conversation != input.ConversationID || len(round.Decisions) > 0 {
			continue
		}
		for _, q := range round.Questions {
			s.Questions = append(s.Questions, binding.Question{ID: round.ID + ":" + q.ID, Target: round.Goal, Options: q.Choices})
		}
		s.Surfaces[round.ID] = round.Goal
	}
	pending, err := waiting.NewConversationQuestions(questionTransaction{tx}).Pending(ctx, input.PersonID, input.AgentID)
	if err != nil {
		return s, err
	}
	for _, q := range pending {
		if q.Conversation != input.ConversationID {
			continue
		}
		target, err := bindingTarget(ctx, tx, input, q.Input)
		if err != nil {
			return s, err
		}

		s.Questions = append(s.Questions, binding.Question{ID: q.ID, Target: target.ID, Options: q.Choices, YesNo: q.YesNo})
		if target.ID != "" {
			s.Surfaces[q.ID] = target.ID
		}
	}
	for _, ref := range []string{input.Surface.ReplyTo, input.Surface.Card, input.Surface.Thread} {
		if ref == "" {
			continue
		}
		if s.Surfaces[ref] != "" {
			continue
		}
		q, e := waiting.NewConversationQuestions(questionTransaction{tx}).Get(ctx, ref)
		if e == nil && q.Person == input.PersonID && q.Agent == input.AgentID && q.Conversation == input.ConversationID {
			target, e := bindingTarget(ctx, tx, input, q.Input)
			if e != nil {
				return s, e
			}
			if target.ID != "" {
				s.Surfaces[ref] = target.ID
				continue
			}
		} else if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return s, e
		}
		target, err := bindingTarget(ctx, tx, input, ref)
		if err != nil {
			return s, err
		}
		if target.ID != "" {
			s.Surfaces[ref] = target.ID
			continue
		}
		// Work cards and artifact threads use the authorized item's own identity.
		if ref == input.Surface.Card || ref == input.Surface.Thread {
			for _, i := range s.Items {
				if i.ID == ref {
					s.Surfaces[ref] = i.ID
				}
			}
		}
	}
	return s, nil
}

// Resolve an event or its reply's parent only inside the current conversation.
func bindingTarget(ctx context.Context, tx *canon.Tx, input ledger.Event, ref string) (binding.Target, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT b.body FROM conv_event e JOIN conv_event b ON b.parent_id=CASE WHEN e.speaker_kind='person' THEN e.id ELSE e.parent_id END AND b.scaffold_kind=? WHERE e.id=? AND e.conversation_id=? AND e.agent_id=?`, inputBindingKind, ref, input.ConversationID, input.AgentID).Scan(&raw)
	if err == nil {
		var d binding.Decision
		err = json.Unmarshal([]byte(raw), &d)
		return d.Target, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return binding.Target{}, err
	}
	// A notice about one background run owns that run, rather than whichever
	// input happened to precede its delivery. Bundled notices stay ambiguous.
	err = tx.QueryRowContext(ctx, `SELECT body FROM conv_event WHERE id=? AND conversation_id=? AND agent_id=? AND scaffold_kind='background_notice'`, ref, input.ConversationID, input.AgentID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return binding.Target{}, nil
	}
	if err != nil {
		return binding.Target{}, err
	}
	var notices []struct{ Run, Person, Agent, Conversation string }
	if err = json.Unmarshal([]byte(raw), &notices); err != nil {
		return binding.Target{}, err
	}
	id := ""
	for _, n := range notices {
		if n.Run == "" || n.Person != input.PersonID || n.Agent != input.AgentID || n.Conversation != input.ConversationID {
			return binding.Target{}, nil
		}
		if id != "" && id != n.Run {
			return binding.Target{}, nil
		}
		id = n.Run
	}
	if id == "" {
		return binding.Target{}, nil
	}
	return binding.Target{Class: "existing", ID: id, Kind: "run"}, nil
}

func (r *conversationRuntime) inputBindingPacket(ctx context.Context, input ledger.Event) (assemble.ContextPacket, error) {
	if input.SpeakerKind != guard.Person {
		return nil, nil
	}
	event, err := r.journal().Get(ctx, inputBindingID(input.ID))
	if errors.Is(err, sql.ErrNoRows) {
		// Recover a still-pending input admitted by an earlier Core version. New
		// admissions always take the atomic path above.
		err = r.db.Write(ctx, func(tx *canon.Tx) error { return r.bindInput(ctx, tx, input) })
		if err == nil {
			event, err = r.journal().Get(ctx, inputBindingID(input.ID))
		}
	}
	if err != nil {
		return nil, err
	}
	return assemble.ContextPacket{{ID: event.ID, AgentID: input.AgentID, Kind: assemble.Waiting, Origin: "input-binding:" + input.ID, Text: event.Body.Text(), Evidence: []assemble.Ref{{ID: input.ID}}}}, nil
}
