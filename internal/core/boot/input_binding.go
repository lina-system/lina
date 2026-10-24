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
	"github.com/thisisjun786/lina/internal/core/lane"
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
	_, err = ledger.AppendSystemSnapshot(ctx, tx, ledger.Input{
		ID: inputBindingID(input.ID), ParentID: input.ID, ConversationID: input.ConversationID, AgentID: input.AgentID,
		Surface: input.Surface, ScaffoldKind: inputBindingKind, OccurredAt: input.OccurredAt, RecordedAt: input.RecordedAt, TimeQuality: "recorded", Body: string(raw),
		Relations: []ledger.Relation{{TargetID: input.ID, Kind: "view_of"}},
	})
	return err
}

func (r *conversationRuntime) bindingState(ctx context.Context, tx *canon.Tx, input ledger.Event) (binding.State, error) {
	s := binding.State{Clock: input.RecordedAt, Focus: binding.Target{Class: "none"}, Surfaces: map[string]string{}}
	// Work follows the tool scope; personal names and shared conversation
	// context independently require the memory scope.
	if r.channelBinding != nil && !r.channelBinding.Scope.Tools {
		return s, nil
	}
	address := ""
	if r.channelBinding != nil && !r.channelBinding.Scope.Memory {
		address = r.channelAddress.Key()
	}
	// Read the latest binding and exchange before this input, using ledger order.
	// A withdrawn input must not restore its focus or refresh its age.
	var raw, at, previousID string
	err := tx.QueryRowContext(ctx, `SELECT b.body,p.recorded_at,p.id FROM conv_event b JOIN retract_visible_event vb ON vb.id=b.id JOIN conv_event p ON p.id=b.parent_id JOIN retract_visible_event vp ON vp.id=p.id WHERE b.conversation_id=? AND b.agent_id=? AND p.person_id=? AND b.scaffold_kind=? AND p.rowid<(SELECT rowid FROM conv_event WHERE id=?) AND (?='' OR EXISTS(SELECT 1 FROM channel_receipts receipt WHERE receipt.address=? AND p.id='channel-input:'||receipt.inbox_id)) ORDER BY p.rowid DESC LIMIT 1`, input.ConversationID, input.AgentID, input.PersonID, inputBindingKind, input.ID, address, address).Scan(&raw, &at, &previousID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}
	if err == nil {
		var previous binding.Decision
		if err = json.Unmarshal([]byte(raw), &previous); err != nil {
			return s, err
		}
		s.Focus = previous.Target
		// Focus reads binding metadata only; a previous input body may be
		// externalized and must not be opened under admission's transaction.
		choices, e := readInputChoices(ctx, tx, ledger.Event{ID: previousID})
		if e != nil {
			return s, e
		}
		for _, c := range choices {
			if c.Question == "" && !c.Cancelled {
				s.Focus = c.Target
				if c.CreatedID != "" {
					s.Focus = binding.Target{Class: "existing", ID: c.CreatedID, Kind: c.Target.Kind}
				}
			}
		}
		// Agent replies also keep an exchange fresh; tool/system traffic does not.
		var latest string
		err = tx.QueryRowContext(ctx, `SELECT e.recorded_at FROM conv_event e JOIN retract_visible_event ve ON ve.id=e.id WHERE e.conversation_id=? AND e.agent_id=? AND e.speaker_kind IN ('person','agent') AND e.rowid<(SELECT rowid FROM conv_event WHERE id=?) AND coalesce(e.scaffold_kind,'')<>'provider_item' AND (?='' OR EXISTS(SELECT 1 FROM channel_receipts receipt WHERE receipt.address=? AND ('channel-input:'||receipt.inbox_id=e.id OR 'channel-input:'||receipt.inbox_id=e.parent_id))) ORDER BY e.rowid DESC LIMIT 1`, input.ConversationID, input.AgentID, input.ID, address, address).Scan(&latest)
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
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.title,t.kind,t.state,coalesce(a.last_activity,''),coalesce(v.fields,'{}') FROM records_text t LEFT JOIN records_activity a ON a.id=t.id LEFT JOIN records_todo v ON v.id=t.id WHERE t.person=? AND t.kind IN ('initiative','project','todo','goal')`, input.PersonID)
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
		// The definition file keeps kind goal. A new code goal is selected as a
		// project, so the binding identity uses that kind and the created record
		// stays usable.
		if i.Kind == "goal" {
			i.Kind = "project"
		}
		s.Items = append(s.Items, i)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return s, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT c.record,coalesce((SELECT json_extract(e.record,'$.recorded_at') FROM work_ledger e WHERE e.task_id=c.task_id ORDER BY e.seq DESC LIMIT 1),'') FROM work_cards c WHERE json_extract(c.record,'$.task.person_id')=? OR (coalesce(json_extract(c.record,'$.task.person_id'),'')='' AND EXISTS (SELECT 1 FROM conv_event e WHERE e.speaker_kind='person' AND e.person_id=? AND e.agent_id=json_extract(c.record,'$.task.agent_id') AND e.conversation_id=json_extract(c.record,'$.task.conversation_id')))`, input.PersonID, input.PersonID)
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
	var names []memory.Hit
	if address == "" {
		names, err = memory.New(questionTransaction{tx}).Names(ctx, memory.RecallScope{Person: input.PersonID, Agent: input.AgentID, ExternalModel: true}, input.RecordedAt.Unix())
	}
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
		if round.Conversation != input.ConversationID {
			continue
		}
		s.Surfaces["work:"+round.ID] = round.Goal
		s.Surfaces[round.ID] = round.Goal
		if len(round.Decisions) > 0 {
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
		visible, err := waiting.ConversationQuestionVisible(ctx, tx, q)
		if err != nil {
			return s, err
		}
		if !visible {
			continue
		}
		if address != "" {
			var count int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM channel_receipts WHERE address=? AND 'channel-input:'||inbox_id=?", address, q.Input).Scan(&count); err != nil {
				return s, err
			}
			if count == 0 {
				continue
			}
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
			visible, e := waiting.ConversationQuestionVisible(ctx, tx, q)
			if e != nil {
				return s, e
			}
			if !visible {
				continue
			}
			if address != "" {
				var count int
				if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM channel_receipts WHERE address=? AND 'channel-input:'||inbox_id=?", address, q.Input).Scan(&count); e != nil {
					return s, e
				}
				if count == 0 {
					continue
				}
			}
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
	// A notice's own run identity takes precedence over the input that started
	// it. The durable delivery record supplies it, so a notice body stored as a
	// material is never read during admission.
	var locator string
	err := tx.QueryRowContext(ctx, `SELECT coalesce(json_extract(origin,'$.locator'),'') FROM conv_event WHERE id=? AND conversation_id=? AND agent_id=? AND scaffold_kind='background_notice'`, ref, input.ConversationID, input.AgentID).Scan(&locator)
	if err == nil {
		return noticeTarget(ctx, tx, input, locator)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return binding.Target{}, err
	}
	var raw, personEvent string
	err = tx.QueryRowContext(ctx, `SELECT b.body, CASE WHEN e.speaker_kind='person' THEN e.id ELSE e.parent_id END FROM conv_event e JOIN conv_event b ON b.parent_id=CASE WHEN e.speaker_kind='person' THEN e.id ELSE e.parent_id END AND b.scaffold_kind=? WHERE e.id=? AND e.conversation_id=? AND e.agent_id=?`, inputBindingKind, ref, input.ConversationID, input.AgentID).Scan(&raw, &personEvent)
	if errors.Is(err, sql.ErrNoRows) {
		return binding.Target{}, nil
	}
	if err != nil {
		return binding.Target{}, err
	}
	var d binding.Decision
	if err = json.Unmarshal([]byte(raw), &d); err != nil {
		return binding.Target{}, err
	}
	choices, err := readInputChoices(ctx, tx, ledger.Event{ID: personEvent})
	if err != nil {
		return binding.Target{}, err
	}
	return overlayChoiceTarget(d.Target, choices), nil
}

// A later choice replaces the admission target. One created identity is that
// target. Several targets, or a target still waiting on the person, do not
// settle a reply.
func overlayChoiceTarget(base binding.Target, choices []binding.Choice) binding.Target {
	if len(choices) == 0 {
		return base
	}
	var settled binding.Target
	have := false
	for _, c := range choices {
		if c.Cancelled || c.Question != "" {
			return binding.Target{}
		}
		target := c.Target
		if c.CreatedID != "" {
			target = binding.Target{Class: "existing", ID: c.CreatedID, Kind: c.Target.Kind}
		}
		if target.Class == "" {
			return binding.Target{}
		}
		if have && target != settled {
			return binding.Target{}
		}
		settled, have = target, true
	}
	if !have {
		return binding.Target{}
	}
	return settled
}

// Several bundled runs remain unresolved, even when they share a delivery.
func noticeTarget(ctx context.Context, tx *canon.Tx, input ledger.Event, locator string) (binding.Target, error) {
	notices, err := lane.DeliveredNotices(ctx, tx, input.ConversationID, locator)
	if errors.Is(err, sql.ErrNoRows) {
		return binding.Target{}, nil
	}
	if err != nil {
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
	packet := assemble.ContextPacket{{ID: event.ID, AgentID: input.AgentID, Kind: assemble.Waiting, Origin: "input-binding:" + input.ID, Text: event.Body.Text(), Evidence: []assemble.Ref{{ID: input.ID}}}}
	err = r.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id,body FROM conv_event WHERE parent_id=? AND scaffold_kind=? ORDER BY rowid", input.ID, modelBindingKind)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, body string
			if err = rows.Scan(&id, &body); err != nil {
				return err
			}
			packet = append(packet, assemble.ContextItem{ID: id, AgentID: input.AgentID, Kind: assemble.Waiting, Origin: "input-choice:" + input.ID, Text: body, Evidence: []assemble.Ref{{ID: input.ID}}})
		}
		return rows.Err()
	})
	return packet, err
}
