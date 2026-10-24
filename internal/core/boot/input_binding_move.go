// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/intakereplay"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

const bindingMoveKind = "input_binding_move"

// effectBefore keeps the bytes an edit replaced, so a later move can restore
// the wrong item exactly.
type effectBefore struct {
	body     []byte
	revision string
}

func effectKind(name string, args map[string]json.RawMessage) (string, bool) {
	str := func(key string) string { var v string; _ = json.Unmarshal(args[key], &v); return v }
	switch name {
	case "write_record":
		if str("id") == "" {
			return binding.EffectCreate, false
		}
		return binding.EffectEdit, false
	case "follow_work", "worker_input":
		return binding.EffectSteer, false
	case "start_run", "dispatch_work", "schedule_work", "dispatch_code_goal", "prepare_work", "worker_start":
		return binding.EffectStart, true
	}
	return "", false
}

// captureEffectBefore reads the record an edit will replace. The snapshot is
// taken outside the write transaction and never repeated after a failure.
func (r *conversationRuntime) captureEffectBefore(ctx context.Context, call provider.ToolCall, args map[string]json.RawMessage) *effectBefore {
	if call.Name != "write_record" || r.records == nil {
		return nil
	}
	var id string
	if json.Unmarshal(args["id"], &id) != nil || id == "" {
		return nil
	}
	record, revision, err := r.records.Read(ctx, id)
	if err != nil {
		return nil
	}
	return &effectBefore{body: record.Data, revision: revision}
}

// recordBindingEffect keeps what the bound turn did, in call order. Only
// movable kinds and work that left the person are retained.
func (r *conversationRuntime) recordBindingEffect(ctx context.Context, input ledger.Event, turnID string, call provider.ToolCall, before *effectBefore, result loop.Result) error {
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(call.Arguments), &args) != nil {
		return nil
	}
	kind, left := effectKind(call.Name, args)
	if kind == "" {
		return nil
	}
	str := func(key string) string { var v string; _ = json.Unmarshal(args[key], &v); return v }
	var receipt struct {
		ID       string `json:"id"`
		RecordID string `json:"record_id"`
		Revision string `json:"revision"`
	}
	_ = json.Unmarshal(result.Output, &receipt)
	recordID, target := str("id"), strings.TrimSpace(str("id"))
	if kind == binding.EffectCreate {
		recordID = receipt.RecordID
		if recordID == "" {
			recordID = receipt.ID
		}
		target = recordID
	}
	if kind == binding.EffectStart && target == "" {
		target = receipt.ID
	}
	// worker_input names a session. The session's task is the steer target,
	// the same identity bindWorkCall checks before the call runs.
	if kind == binding.EffectSteer && call.Name == "worker_input" {
		if task := r.workerTaskID(str("session")); task != "" {
			target, recordID = task, task
		}
	}
	if target == "" && recordID == "" {
		return nil
	}
	var body any
	revision := ""
	if before != nil && kind == binding.EffectEdit {
		body, revision = before.body, before.revision
	}
	return r.db.Write(ctx, func(tx *canon.Tx) error {
		var bound int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM conv_event WHERE id=? AND scaffold_kind=?", inputBindingID(input.ID), inputBindingKind).Scan(&bound); err != nil || bound == 0 {
			return err
		}
		var ordinal int
		if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(ordinal)+1,0) FROM binding_effects WHERE input_event=?", input.ID).Scan(&ordinal); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO binding_effects(input_event,turn_id,call_id,ordinal,kind,target,record_id,before_body,before_revision,after_revision,payload,left_person) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(input_event,turn_id,call_id) DO NOTHING`,
			input.ID, turnID, call.ID, ordinal, kind, target, recordID, body, revision, receipt.Revision, string(call.Arguments), boolInt(left))
		return err
	})
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func readBindingEffects(ctx context.Context, tx *canon.Tx, input string) ([]binding.Effect, error) {
	rows, err := tx.QueryContext(ctx, `SELECT turn_id,call_id,ordinal,kind,target,record_id,coalesce(before_body,x''),before_revision,after_revision,payload,left_person FROM binding_effects WHERE input_event=? ORDER BY ordinal`, input)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var effects []binding.Effect
	for rows.Next() {
		e := binding.Effect{InputEvent: input}
		var left int
		if err := rows.Scan(&e.TurnID, &e.CallID, &e.Ordinal, &e.Kind, &e.Target, &e.RecordID, &e.BeforeBody, &e.BeforeRevision, &e.AfterRevision, &e.Payload, &left); err != nil {
			return nil, err
		}
		e.LeftPerson = left == 1
		effects = append(effects, e)
	}
	return combineBindingEdits(effects), rows.Err()
}

// combineBindingEdits restores the first before-image and transfers the final
// fields of an uninterrupted edit chain. A created item already carries edits.
func combineBindingEdits(effects []binding.Effect) []binding.Effect {
	out := make([]binding.Effect, 0, len(effects))
	latest := map[string]int{}
	for _, effect := range effects {
		if index, ok := latest[effect.RecordID]; ok && effect.RecordID != "" && effect.Kind == binding.EffectEdit {
			previous := &out[index]
			if previous.Kind == binding.EffectCreate {
				continue
			}
			if previous.Kind == binding.EffectEdit && previous.AfterRevision == effect.BeforeRevision {
				previous.AfterRevision = effect.AfterRevision
				previous.Payload = effect.Payload
				previous.Folded = append(previous.Folded, binding.EffectCall{TurnID: effect.TurnID, CallID: effect.CallID})
				continue
			}
		}
		if effect.RecordID != "" {
			latest[effect.RecordID] = len(out)
		}
		out = append(out, effect)
	}
	return out
}

// itemName reads the display name of a bound item from the same projections
// the candidate ranking uses.
func itemName(ctx context.Context, tx *canon.Tx, person, id string) string {
	var title string
	if err := tx.QueryRowContext(ctx, "SELECT title FROM records_text WHERE id=? AND person=?", id, person).Scan(&title); err == nil {
		return title
	}
	if err := tx.QueryRowContext(ctx, "SELECT json_extract(record,'$.Task.Title') FROM work_cards WHERE task_id=?", strings.TrimPrefix(id, "task:")).Scan(&title); err == nil {
		return title
	}
	if err := tx.QueryRowContext(ctx, "SELECT json_extract(record,'$.Brief.Goal') FROM background_runs WHERE id=?", id).Scan(&title); err == nil {
		return title
	}
	return ""
}

// boundChip derives the chip from the final choice, then the admission
// decision. A created item names itself once its receipt exists.
func (r *conversationRuntime) boundChip(ctx context.Context, tx *canon.Tx, input ledger.Event) (binding.Chip, error) {
	choices, err := readInputChoices(ctx, tx, input)
	if err != nil {
		return binding.Chip{}, err
	}
	current := currentChoice(choices)
	target := current.Target
	name := ""
	if current.CreatedID != "" {
		name = itemName(ctx, tx, input.PersonID, current.CreatedID)
	}
	if name == "" && target.ID != "" {
		name = itemName(ctx, tx, input.PersonID, target.ID)
	}
	state, err := r.bindingState(ctx, tx, input)
	if err != nil {
		return binding.Chip{}, err
	}
	var raw string
	if err = tx.QueryRowContext(ctx, "SELECT body FROM conv_event WHERE id=?", inputBindingID(input.ID)).Scan(&raw); err != nil {
		return binding.Chip{}, err
	}
	var decision binding.Decision
	if err = json.Unmarshal([]byte(raw), &decision); err != nil {
		return binding.Chip{}, err
	}
	if decision.Snapshot != nil {
		state = *decision.Snapshot
	}
	chip := binding.ChipFor(state, target)
	chip.Name = name
	return chip, nil
}

// chipFor builds the chip a surface shows for one admitted input, without
// publishing it. A missing binding leaves the message without a chip.
func (r *conversationRuntime) chipFor(ctx context.Context, input ledger.Event) *protocol.BindingChip {
	var chip binding.Chip
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		var e error
		chip, e = r.boundChip(ctx, tx, input)
		return e
	})
	if err != nil {
		return nil
	}
	return chipWire(input.ID, chip)
}

func currentChoice(choices []binding.Choice) binding.Choice {
	current := choices[0]
	for _, c := range choices[1:] {
		if c.Question == "" && !c.Cancelled {
			current = c
		}
	}
	return current
}

func chipWire(input string, chip binding.Chip) *protocol.BindingChip {
	return &protocol.BindingChip{InputID: input, Show: chip.Show, Name: chip.Name, Target: targetWire(chip.Target)}
}

func targetWire(t binding.Target) protocol.BindingTarget {
	wire := protocol.BindingTarget{Class: t.Class}
	if t.ID != "" {
		wire.ID = &t.ID
	}
	if t.Kind != "" {
		wire.Kind = &t.Kind
	}
	if t.Parent != "" {
		wire.Parent = &t.Parent
	}
	return wire
}

func targetFromWire(t protocol.BindingTarget) binding.Target {
	out := binding.Target{Class: t.Class}
	if t.ID != nil {
		out.ID = *t.ID
	}
	if t.Kind != nil {
		out.Kind = *t.Kind
	}
	if t.Parent != nil {
		out.Parent = *t.Parent
	}
	return out
}

// publishBindingChip republishes the person's message with its current chip so
// a live surface follows a model, tool or person choice.
func (r *conversationRuntime) publishBindingChip(ctx context.Context, input ledger.Event) error {
	if input.SpeakerKind != guard.Person || r.topics == nil {
		return nil
	}
	var chip binding.Chip
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		chip, err = r.boundChip(ctx, tx, input)
		return err
	})
	if err != nil {
		return err
	}
	text := input.Body.Text()
	speaker := protocol.ConversationSpeaker(guard.Person)
	view := r
	if view.coordinator == nil && (view.conversation != input.ConversationID || view.agent != input.AgentID) {
		view = newConversationView(r, input.AgentID, input.ConversationID)
	}
	return view.publish(ctx, protocol.KindItemDone, input.ID, protocol.ItemDone{Text: &text, Speaker: &speaker, PersonID: &input.PersonID, AgentID: &input.AgentID, Binding: chipWire(input.ID, chip)})
}

var errOutsideBinding = errors.New("outside input binding")

// moveBinding appends one rebinding event, re-attaches what the turn did and
// records the counter, label and replay case.
func (r *conversationRuntime) moveBinding(ctx context.Context, inputID string, target binding.Target, key string) (binding.Move, error) {
	root := r
	if r.router != nil {
		root = r.router.root
	}
	root.inputMoveMu.Lock()
	defer root.inputMoveMu.Unlock()
	input, err := r.journal().Get(ctx, inputID)
	if err != nil {
		return binding.Move{}, err
	}
	if input.PersonID != r.person || input.SpeakerKind != guard.Person {
		return binding.Move{}, errOutsideBinding
	}
	moveID := "move:" + inputID + ":" + shortKey(key)
	var prior binding.Move
	err = r.db.Read(ctx, func(tx *canon.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, "SELECT record FROM binding_moves WHERE id=?", moveID).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &prior)
	})
	if err == nil {
		if prior.To != target {
			return binding.Move{}, errOutsideBinding
		}
		return prior, r.publishBindingChip(ctx, input)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return binding.Move{}, err
	}
	var decision binding.Decision
	var state binding.State
	var from binding.Target
	var decider string
	var current binding.Choice
	err = r.db.Read(ctx, func(tx *canon.Tx) error {
		var raw string
		if err = tx.QueryRowContext(ctx, "SELECT body FROM conv_event WHERE id=? AND scaffold_kind=?", inputBindingID(inputID), inputBindingKind).Scan(&raw); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(raw), &decision); err != nil {
			return err
		}
		choices, err := readInputChoices(ctx, tx, input)
		if err != nil {
			return err
		}
		current = currentChoice(choices)
		from, decider = current.Target, current.Decider
		var original string
		err = tx.QueryRowContext(ctx, "SELECT decided_by FROM binding_moves WHERE input_event=? ORDER BY rowid LIMIT 1", inputID).Scan(&original)
		if err == nil {
			decider = original
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if decider == "" {
			decider = decision.Decider
		}
		if decider == "" {
			decider = "no-rule"
		}
		state, err = r.bindingState(ctx, tx, input)
		if err != nil {
			return err
		}
		offers := binding.Offers(decision, current.Target)
		if !binding.Offered(offers, target) || !binding.ValidTarget(state, target) {
			return errOutsideBinding
		}
		return nil
	})
	if err != nil {
		return binding.Move{}, err
	}
	now := time.Now().UTC()
	move := binding.Move{ID: moveID, InputEvent: inputID, Person: input.PersonID, Agent: input.AgentID, Decider: decider, From: from, To: target, Effects: []binding.Outcome{}, At: now.Format(time.RFC3339Nano), State: state, Candidates: decision.Candidates, Input: binding.Input{Text: input.Body.Text(), ReplyTo: input.Surface.ReplyTo, Card: input.Surface.Card, Thread: input.Surface.Thread}}
	if decision.Snapshot != nil {
		move.State = *decision.Snapshot
	}
	var effects []binding.Effect
	err = r.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		effects, err = readBindingEffects(ctx, tx, inputID)
		return err
	})
	if err != nil {
		return binding.Move{}, err
	}
	var relocated []binding.Effect
	for _, effect := range effects {
		outcome, next, e := r.applyBindingEffect(ctx, moveID, effect, target)
		if e != nil {
			outcome.Moved, outcome.Reason = false, "failed"
			next = nil
		}
		if next != nil {
			next.Folded = effect.Folded
			relocated = append(relocated, *next)
		}
		move.Effects = append(move.Effects, outcome)
	}
	// The store keeps the move before any surface learns it, so a restart
	// replays the counter, the label and the exported case.
	err = r.db.Write(ctx, func(tx *canon.Tx) error {
		raw, err := json.Marshal(move)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO binding_moves(id,input_event,person,agent,conversation,decided_by,from_target,to_target,record,moved_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING", moveID, inputID, move.Person, move.Agent, input.ConversationID, move.Decider, move.From.ID, move.To.ID, raw, move.At); err != nil {
			return err
		}
		if _, err = ledger.AppendSystemSnapshot(ctx, tx, ledger.Input{ID: "binding-move:" + moveID, ParentID: inputID, ConversationID: input.ConversationID, AgentID: input.AgentID, Surface: input.Surface, ScaffoldKind: bindingMoveKind, OccurredAt: now, RecordedAt: now, TimeQuality: "recorded", Body: string(raw), Relations: []ledger.Relation{{TargetID: inputID, Kind: "view_of"}}}); err != nil {
			return err
		}
		choice := binding.Choice{Request: current.Request, Quote: current.Quote, Target: target, Decider: "person"}
		if err = appendInputChoices(ctx, tx, input, "binding-move-choice:"+moveID, []binding.Choice{choice}); err != nil {
			return err
		}
		for _, effect := range relocated {
			if err = storeRelocatedEffect(ctx, tx, effect); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return binding.Move{}, err
	}
	if err = r.publishBindingChip(ctx, input); err != nil {
		return move, err
	}
	return move, nil
}

func shortKey(key string) string {
	if key == "" {
		return fmt.Sprint(time.Now().UTC().UnixNano())
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// applyBindingEffect re-attaches one recorded effect at the new target. A
// non-nil effect is that effect at its new location, ready to store.
func (r *conversationRuntime) applyBindingEffect(ctx context.Context, moveID string, effect binding.Effect, to binding.Target) (binding.Outcome, *binding.Effect, error) {
	outcome := binding.Outcome{Kind: effect.Kind, Item: effect.Target}
	if effect.LeftPerson || !binding.Movable(effect.Kind) {
		outcome.Reason = "left"
		return outcome, nil, nil
	}
	switch effect.Kind {
	case binding.EffectCreate:
		moved, err := r.moveCreatedRecord(ctx, effect, to, outcome)
		return moved, nil, err
	case binding.EffectEdit:
		return r.moveEditedRecord(ctx, effect, to, outcome)
	case binding.EffectSteer:
		return r.moveSteer(ctx, moveID, effect, to, outcome)
	}
	outcome.Reason = "left"
	return outcome, nil, nil
}

// workerTaskID resolves a worker session to the task it is running. Tool
// binding and effect recording share this lookup.
func (r *conversationRuntime) workerTaskID(session string) string {
	if r == nil || r.workers == nil || session == "" {
		return ""
	}
	for _, entry := range r.workers.entries() {
		if entry != nil && entry.thread.ID == session && entry.thread.TaskID != "" {
			return entry.thread.TaskID
		}
	}
	return ""
}

// storeRelocatedEffect keeps a moved effect at its new location so the next
// move restores and withdraws from there. Folded calls belonged to the same
// edit chain and must not be replayed against the old record.
func storeRelocatedEffect(ctx context.Context, tx *canon.Tx, effect binding.Effect) error {
	if _, err := tx.ExecContext(ctx, `UPDATE binding_effects SET target=?, record_id=?, before_body=?, before_revision=?, after_revision=?, payload=? WHERE input_event=? AND turn_id=? AND call_id=?`,
		effect.Target, effect.RecordID, effect.BeforeBody, effect.BeforeRevision, effect.AfterRevision, effect.Payload, effect.InputEvent, effect.TurnID, effect.CallID); err != nil {
		return err
	}
	for _, call := range effect.Folded {
		if _, err := tx.ExecContext(ctx, `DELETE FROM binding_effects WHERE input_event=? AND turn_id=? AND call_id=?`, effect.InputEvent, call.TurnID, call.CallID); err != nil {
			return err
		}
	}
	return nil
}

func (r *conversationRuntime) moveCreatedRecord(ctx context.Context, effect binding.Effect, to binding.Target, outcome binding.Outcome) (binding.Outcome, error) {
	if r.records == nil || effect.RecordID == "" {
		outcome.Reason = "missing_record"
		return outcome, nil
	}
	record, revision, err := r.records.Read(ctx, effect.RecordID)
	if err != nil {
		outcome.Reason = "missing_record"
		return outcome, nil
	}
	todo, front, body, err := records.ReadTodo(record.Data)
	if err != nil {
		outcome.Reason = "not_a_todo"
		return outcome, nil
	}
	switch to.Class {
	case "none", "new":
		todo.BelongsTo = nil
		if to.Class == "new" && to.Parent != "" {
			todo.BelongsTo = []string{to.Parent}
		}
	case "existing":
		if err = r.confirmTodoParent(ctx, to.ID); err != nil {
			outcome.Reason = "not_a_parent"
			return outcome, nil
		}
		todo.BelongsTo = []string{to.ID}
	default:
		outcome.Reason = "not_a_parent"
		return outcome, nil
	}
	next, err := records.WithTodo(front, *todo)
	if err != nil {
		outcome.Reason = "not_a_parent"
		return outcome, nil
	}
	saved, err := r.records.Commit(ctx, records.Change{ID: effect.RecordID, Base: revision, Path: record.Path, Data: []byte("---\n" + next + "\n---\n" + body)}, records.Actor{Person: r.person, Agent: r.agent})
	if err != nil {
		outcome.Reason = "stale"
		return outcome, nil
	}
	outcome.Moved, outcome.Receipt = true, saved
	return outcome, nil
}

// confirmTodoParent checks that the new target may own a todo record.
func (r *conversationRuntime) confirmTodoParent(ctx context.Context, id string) error {
	var kind string
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT kind FROM records_text WHERE id=? AND person=?", id, r.person).Scan(&kind)
	})
	if err != nil {
		return err
	}
	if kind != "project" && kind != "initiative" && kind != "area" {
		return records.ErrInvalid
	}
	return nil
}

func (r *conversationRuntime) moveEditedRecord(ctx context.Context, effect binding.Effect, to binding.Target, outcome binding.Outcome) (binding.Outcome, *binding.Effect, error) {
	if r.records == nil || effect.RecordID == "" || len(effect.BeforeBody) == 0 {
		outcome.Reason = "missing_record"
		return outcome, nil, nil
	}
	record, revision, err := r.records.Read(ctx, effect.RecordID)
	if err != nil {
		outcome.Reason = "missing_record"
		return outcome, nil, nil
	}
	if effect.AfterRevision != "" && revision != effect.AfterRevision {
		outcome.Reason = "changed_since"
		return outcome, nil, nil
	}
	changes := []records.Change{{ID: effect.RecordID, Base: revision, Path: record.Path, Data: effect.BeforeBody}}
	receipt := "restored"
	var applied *records.Change
	var beforeBody []byte
	var beforeRev string
	if to.Class == "existing" {
		if to.ID == effect.RecordID {
			outcome.Reason = "same_target"
			return outcome, nil, nil
		}
		var reason string
		applied, beforeBody, beforeRev, reason = r.reapplyEdit(ctx, effect, to.ID)
		if reason != "" {
			outcome.Reason = reason
			return outcome, nil, nil
		}
		if applied != nil {
			changes = append(changes, *applied)
			receipt = "restored+applied"
		}
	}
	if _, err = r.records.CommitBatch(ctx, changes, records.Actor{Person: r.person, Agent: r.agent}); err != nil {
		outcome.Reason = "stale"
		return outcome, nil, nil
	}
	outcome.Moved, outcome.Receipt = true, receipt
	next := effect
	if applied != nil {
		_, rev, err := r.records.Read(ctx, to.ID)
		if err != nil {
			return outcome, nil, nil
		}
		next.Target, next.RecordID = to.ID, to.ID
		next.BeforeBody, next.BeforeRevision, next.AfterRevision = beforeBody, beforeRev, rev
		if payload := relocatedEditPayload(to.ID, applied.Data); len(payload) > 0 {
			next.Payload = payload
		}
		return outcome, &next, nil
	}
	if _, rev, err := r.records.Read(ctx, effect.RecordID); err == nil {
		next.AfterRevision = rev
		return outcome, &next, nil
	}
	return outcome, nil, nil
}

// moveEditRepo stages the validated target write for the same publication as
// the restoration. A rejected destination must leave the original edit intact.
type moveEditRepo struct {
	records.Repo
	change *records.Change
}

func (s *moveEditRepo) Commit(_ context.Context, c records.Change, _ records.Actor) (records.Rev, error) {
	s.change = &c
	return c.Base, nil
}
func (s *moveEditRepo) Read(ctx context.Context, id records.ID) (records.Record, records.Rev, error) {
	if s.change != nil && s.change.ID == id {
		return records.Record{ID: id, Path: s.change.Path, Data: s.change.Data}, s.change.Base, nil
	}
	return s.Repo.Read(ctx, id)
}

// reapplyEdit prepares the changed fields on the right item while preserving
// its other fields. WriteRecord performs the ordinary ownership/field checks.
func (r *conversationRuntime) reapplyEdit(ctx context.Context, effect binding.Effect, target string) (*records.Change, []byte, string, string) {
	var write coreexec.RecordWrite
	if json.Unmarshal(effect.Payload, &write) != nil {
		return nil, nil, "", "missing_payload"
	}
	right, rightRevision, err := r.records.Read(ctx, target)
	if err != nil {
		return nil, nil, "", "not_a_record"
	}
	beforeKind, beforeTitle, beforeNote, _, _ := recordParts(effect.BeforeBody)
	kind, title, note, _, _ := recordParts(right.Data)
	if kind != write.Kind {
		return nil, nil, "", "target_kind"
	}
	if write.Title == beforeTitle && write.Note == beforeNote && write.Todo == nil {
		return nil, nil, "", ""
	}
	if write.Title != beforeTitle {
		title = write.Title
	}
	if write.Note != beforeNote {
		note = write.Note
	}
	var todo *records.Todo
	if write.Todo != nil {
		var beforeTodo, rightTodo *records.Todo
		if beforeKind == "todo" {
			beforeTodo, _, _, _ = records.ReadTodo(effect.BeforeBody)
		}
		if kind == "todo" {
			rightTodo, _, _, _ = records.ReadTodo(right.Data)
		}
		if beforeTodo != nil && rightTodo != nil {
			todo = mergeTodo(beforeTodo, write.Todo, rightTodo)
		}
	}
	staged := &moveEditRepo{Repo: r.records}
	if _, err = coreexec.WriteRecord(ctx, staged, records.Actor{Person: r.person, Agent: r.agent}, coreexec.RecordWrite{ID: target, Revision: rightRevision, Kind: kind, Title: title, Note: note, Todo: todo}); err != nil {
		return nil, nil, "", "target_refused"
	}
	return staged.change, right.Data, rightRevision, ""
}

// relocatedEditPayload describes the edit as it now sits on the destination,
// so a later move diffs against that item rather than the original one.
func relocatedEditPayload(id string, data []byte) []byte {
	kind, title, note, _, _ := recordParts(data)
	write := coreexec.RecordWrite{ID: id, Kind: kind, Title: title, Note: note}
	if todo, _, _, err := records.ReadTodo(data); err == nil && todo != nil {
		write.Todo = todo
	}
	raw, err := json.Marshal(write)
	if err != nil {
		return nil
	}
	return raw
}

// mergeTodo copies fields the edit changed and drops fields the edit cleared.
// A cleared optional field is absent after marshalling because Todo uses omitempty.
func mergeTodo(before, edit, right *records.Todo) *records.Todo {
	b, _ := json.Marshal(before)
	e, _ := json.Marshal(edit)
	current, _ := json.Marshal(right)
	var bm, em, rm map[string]json.RawMessage
	if json.Unmarshal(b, &bm) != nil || json.Unmarshal(e, &em) != nil || json.Unmarshal(current, &rm) != nil {
		return right
	}
	kept := map[string]bool{}
	for key, value := range em {
		kept[key] = true
		if string(bm[key]) != string(value) {
			rm[key] = value
		}
	}
	for key := range bm {
		if !kept[key] {
			delete(rm, key)
		}
	}
	raw, _ := json.Marshal(rm)
	var out records.Todo
	if json.Unmarshal(raw, &out) != nil {
		return right
	}
	return &out
}

// recordParts reads the identity, title and note a record shows, without
// interpreting its other front matter.
func recordParts(data []byte) (kind, title, note, front, body string) {
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return
	}
	front, body, ok := strings.Cut(text[4:], "\n---\n")
	if !ok {
		return "", "", "", "", ""
	}
	for _, line := range strings.Split(front, "\n") {
		if value, ok := strings.CutPrefix(line, "kind: "); ok {
			kind = strings.Trim(value, `"`)
		}
	}
	trimmed := strings.TrimSpace(body)
	if rest, ok := strings.CutPrefix(trimmed, "# "); ok {
		title, note, _ = strings.Cut(rest, "\n")
		note = strings.TrimSpace(note)
	}
	return kind, title, note, front, body
}

func (r *conversationRuntime) moveSteer(ctx context.Context, moveID string, effect binding.Effect, to binding.Target, outcome binding.Outcome) (binding.Outcome, *binding.Effect, error) {
	effectKey := moveID + ":" + shortKey(effect.TurnID+":"+effect.CallID)
	withdrawal := ""
	if effect.Target != "" {
		var err error
		withdrawal, err = r.withdrawSteer(ctx, effectKey, effect)
		if err != nil {
			outcome.Reason = "withdrawal_refused"
			return outcome, nil, nil
		}
	}
	relocated := effect
	if to.Class == "none" || to.Class == "new" {
		// The withdrawal still runs; there is nowhere to deliver the note.
		outcome.Reason, outcome.Receipt = "no_target", withdrawal
		relocated.Target, relocated.RecordID = "", ""
		return outcome, &relocated, nil
	}
	delivery, err := r.followMove(ctx, effectKey, to.ID, steerText(effect.Payload), "deliver")
	if err != nil {
		outcome.Reason, outcome.Receipt = "delivery_refused", withdrawal
		return outcome, nil, nil
	}
	outcome.Moved, outcome.Receipt = true, withdrawal+","+delivery
	relocated.Target, relocated.RecordID = to.ID, to.ID
	return outcome, &relocated, nil
}

func steerText(payload []byte) string {
	var text string
	if json.Unmarshal(payload, &text) == nil && strings.TrimSpace(text) != "" {
		return text
	}
	var args struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(payload, &args) == nil && strings.TrimSpace(args.Text) != "" {
		return args.Text
	}
	return "the earlier note"
}

// withdrawSteer sends the wrong target a message that withdraws the earlier
// note. Delivery stays with the durable follow-up queue, so the retained key
// is its receipt.
func (r *conversationRuntime) withdrawSteer(ctx context.Context, moveID string, effect binding.Effect) (string, error) {
	return r.followMove(ctx, moveID, effect.Target, "Withdrawal: do not act on an earlier note for this target. "+steerText(effect.Payload), "withdraw")
}

func (r *conversationRuntime) followMove(ctx context.Context, moveID, target, text, action string) (string, error) {
	key := "binding-move:" + moveID + ":" + action
	run := target
	if _, err := r.work.Get(ctx, strings.TrimPrefix(target, "task:")); err == nil {
		run = "task:" + strings.TrimPrefix(target, "task:")
	}
	_, err := r.followRunWithSource(ctx, clientsrv.Session{PersonID: r.person, Epoch: r.epoch}, protocol.Envelope{MessageID: key, Payload: protocol.FollowUp{RunID: run, Text: text}}, nil)
	if err != nil {
		return "", err
	}
	return key, nil
}

// bindingStats counts moves per hundred bindings, overall and by decider.
func (r *conversationRuntime) bindingStats(ctx context.Context) (binding.Stats, error) {
	bindings := map[string]int{}
	moves := map[string]int{}
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT parent_id FROM conv_event WHERE scaffold_kind=? AND agent_id=?", inputBindingKind, r.agent)
		if err != nil {
			return err
		}
		var inputs []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			inputs = append(inputs, id)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, id := range inputs {
			var decider string
			err = tx.QueryRowContext(ctx, "SELECT decided_by FROM binding_moves WHERE input_event=? ORDER BY rowid LIMIT 1", id).Scan(&decider)
			if errors.Is(err, sql.ErrNoRows) {
				choices, e := readInputChoices(ctx, tx, ledger.Event{ID: id})
				if e != nil {
					return e
				}
				decider = currentChoice(choices).Decider
			} else if err != nil {
				return err
			}
			if decider == "" {
				decider = "no-rule"
			}
			bindings[decider]++
		}

		rows, err = tx.QueryContext(ctx, "SELECT decided_by,count(*) FROM binding_moves WHERE agent=? GROUP BY decided_by", r.agent)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var decider string
			var count int
			if err = rows.Scan(&decider, &count); err != nil {
				return err
			}
			moves[decider] += count
		}
		return rows.Err()
	})
	if err != nil {
		return binding.Stats{}, err
	}
	return binding.Tally(bindings, moves), nil
}

// exportBindingCases writes one replay case per recorded move. The cases stay
// readable after a restart because each is rebuilt from the durable move.
func (r *conversationRuntime) exportBindingCases(ctx context.Context) (string, int, error) {
	var cases []intakereplay.Case
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM binding_moves WHERE agent=? ORDER BY moved_at, id", r.agent)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			var move binding.Move
			if json.Unmarshal(raw, &move) != nil {
				continue
			}
			if c, ok := moveReplayCase(move); ok {
				cases = append(cases, c)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return "", 0, err
	}
	// Cases are rebuildable derived data; material export staging belongs to
	// the asset store, which removes files it does not recognise on reopen.
	dir := filepath.Join(r.root, "derived", "intake-cases")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	path := filepath.Join(dir, "binding-cases-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".jsonl")
	var body strings.Builder
	for _, c := range cases {
		raw, err := json.Marshal(c)
		if err != nil {
			return "", 0, err
		}
		body.Write(raw)
		body.WriteByte('\n')
	}
	if err = os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		return "", 0, err
	}
	return dir, len(cases), nil
}

// moveReplayCase turns one move into a scored intake case: the move is the
// label of the choice, so the original pick is the observation and the chosen
// target is the gold answer.
func moveReplayCase(move binding.Move) (intakereplay.Case, bool) {
	state := move.State
	if state.Clock.IsZero() {
		state.Clock = time.Now().UTC()
	}
	items := []binding.Item{}
	for _, item := range state.Items {
		switch item.Kind {
		case "todo", "initiative", "project", "task", "run":
		default:
			continue
		}
		if item.LastActivity.IsZero() || item.LastActivity.After(state.Clock) {
			item.LastActivity = state.Clock
		}
		items = append(items, item)
	}
	state.Items = items
	if state.Questions == nil {
		state.Questions = []binding.Question{}
	}
	if state.Surfaces == nil {
		state.Surfaces = map[string]string{}
	}
	if state.Focus.Class != "existing" {
		state.Focus = binding.Target{Class: "none"}
	}
	c := intakereplay.Case{Version: 1, ID: move.ID, Set: "hard", State: &state, Input: move.Input, Gold: []intakereplay.Gold{{Target: move.To}}, Observed: []intakereplay.Binding{{Target: move.From, Decider: move.Decider}}}
	if intakereplay.Validate(c) != "" {
		return intakereplay.Case{}, false
	}
	return c, true
}

// bindingOperations exposes the chip, the move list, the counter and the case
// export on the conversation capability the TUI already negotiates.
func bindingOperations(service clientsrv.ConversationService) map[protocol.Kind]clientsrv.Operation {
	handle := func(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
		binder, ok := service.(bindingService)
		if !ok {
			return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
		}
		switch p := e.Payload.(type) {
		case protocol.BindingGet:
			b, err := binder.Binding(ctx, s, p)
			return clientsrv.Result{Kind: protocol.KindBinding, Payload: b}, err
		case protocol.BindingMove:
			m, err := binder.MoveBinding(ctx, s, p, e.MessageID)
			return clientsrv.Result{Kind: protocol.KindBindingMoveResult, Payload: m, Effect: protocol.EffectApplied}, err
		case protocol.BindingStatsGet:
			stats, err := binder.BindingStats(ctx, s)
			return clientsrv.Result{Kind: protocol.KindBindingStats, Payload: stats}, err
		case protocol.BindingExport:
			result, err := binder.ExportBindingCases(ctx, s)
			return clientsrv.Result{Kind: protocol.KindBindingExportResult, Payload: result}, err
		}
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
	}
	ops := make(map[protocol.Kind]clientsrv.Operation)
	for _, kind := range []protocol.Kind{protocol.KindBindingGet, protocol.KindBindingMove, protocol.KindBindingStatsGet, protocol.KindBindingExport} {
		ops[kind] = clientsrv.Operation{Capability: clientsrv.ConversationCapability, Handle: handle}
	}
	return ops
}
