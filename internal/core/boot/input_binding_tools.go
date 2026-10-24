// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/reply"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type toolBindingRef struct {
	InputEventID string `json:"input_event_id,omitempty"`
	Input        string `json:"input,omitempty"`
	Text         string `json:"text,omitempty"`
	Request      int    `json:"request"`
	Ordinal      bool   `json:"-"`
}

// UnmarshalJSON accepts the shapes a model uses for the optional work binding:
// an object, an ordinal, or a selector string holding a request or input text.
func (t *toolBindingRef) UnmarshalJSON(raw []byte) error {
	if ordinal, ok := flexIntRef(raw); ok {
		t.Request, t.Ordinal = ordinal, true
		return t.valid()
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if ordinal, err := strconv.Atoi(strings.TrimSpace(text)); err == nil {
			t.Request, t.Ordinal = ordinal, true
		} else {
			t.Input = text
		}
		return t.valid()
	}
	var obj struct {
		InputEventID string          `json:"input_event_id"`
		EventID      string          `json:"event_id"`
		Input        string          `json:"input"`
		Text         string          `json:"text"`
		Request      json.RawMessage `json:"request"`
		Index        json.RawMessage `json:"index"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return binding.ErrChoice
	}
	t.InputEventID = firstString(obj.InputEventID, obj.EventID)
	t.Input = firstString(obj.Input, obj.Text)
	ordinal, ok := flexIntRef(obj.Request)
	if !ok {
		ordinal, ok = flexIntRef(obj.Index)
	}
	if ok {
		t.Request, t.Ordinal = ordinal, true
	}
	return t.valid()
}

func (t *toolBindingRef) valid() error {
	if t.Request < 0 || t.Request >= 32 {
		return binding.ErrChoice
	}
	return nil
}

func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func flexIntRef(raw []byte) (int, bool) {
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) != nil {
		return 0, false
	}
	value, err := number.Int64()
	if err != nil {
		return 0, false
	}
	return int(value), true
}

func stripToolBinding(c provider.ToolCall) (provider.ToolCall, toolBindingRef, error) {
	var args map[string]json.RawMessage
	var ref toolBindingRef
	if err := json.Unmarshal([]byte(c.Arguments), &args); err != nil || args == nil {
		return c, ref, binding.ErrChoice
	}
	if raw, ok := args["input_binding"]; ok {
		if json.Unmarshal(raw, &ref) != nil {
			return c, ref, binding.ErrChoice
		}
		delete(args, "input_binding")
		body, _ := json.Marshal(args)
		c.Arguments = string(body)
	}
	return c, ref, nil
}
func (s *turnState) wrapInputTools(declarations []provider.ToolDecl) []provider.ToolDecl {
	for n, decl := range declarations {
		var d map[string]any
		if json.Unmarshal(decl, &d) != nil {
			continue
		}
		name, _ := d["name"].(string)
		if name == "bind_input" || name == "resolve_question" || name == "ask_question" {
			continue
		}
		tool, ok := s.bindings[name]
		if !ok {
			continue
		}
		params, ok := d["parameters"].(map[string]any)
		if !ok {
			continue
		}
		properties, ok := params["properties"].(map[string]any)
		if !ok {
			continue
		}
		properties["input_binding"] = map[string]any{"type": "object", "properties": map[string]any{"input_event_id": map[string]string{"type": "string"}, "request": map[string]any{"type": "integer", "default": 0}}, "additionalProperties": false}
		declarations[n], _ = json.Marshal(d)
		s.bindings[name] = loop.Tool{
			Validate: func(c provider.ToolCall) (string, error) {
				clean, _, err := stripToolBinding(c)
				if err != nil {
					return "", err
				}
				return tool.Validate(clean)
			},
			Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
				clean, ref, err := stripToolBinding(c)
				if err != nil {
					return bindingFailure(c.ID, err)
				}
				if _, err = tool.Validate(clean); err != nil {
					return loop.Result{ID: c.ID, Code: loop.Invalid}
				}
				if s.bindingSource != nil && ref.Input == "" && ref.InputEventID == "" {
					ref.Request = s.bindingRequest
				}
				input, r, err := s.selectedBindingInput(ref.InputEventID, ref.Input)
				if err != nil {
					return bindingFailure(c.ID, err)
				}
				root := r
				if r.router != nil {
					root = r.router.root
				}
				if createsBindingItem(clean.Name) {
					root.inputCreationMu.Lock()
					defer root.inputCreationMu.Unlock()
				}
				// Gate and bind before the original tool's own effect/authority path.
				if result := r.bindWorkCall(ctx, input, ref.Request, s.turnID, clean); result != nil {
					return *result
				}
				exec := tool
				// A selected steering input keeps its runtime and channel source
				// through execution, not only through the binding gate.
				if r != s.runtime || input.ID != s.input.ID {
					alt, err := r.steeringTool(ctx, s.turnID, input, s.replyShapes, clean.Name)
					if err != nil {
						return bindingFailure(c.ID, err)
					}
					if alt.Execute != nil {
						exec = alt
					}
				}
				var args map[string]json.RawMessage
				_ = json.Unmarshal([]byte(clean.Arguments), &args)
				before := r.captureEffectBefore(ctx, clean, args)
				result := exec.Execute(ctx, clean)
				if result.Code == loop.OK {
					if err := r.recordCreatedBinding(ctx, input, ref.Request, s.turnID, clean, result); err != nil {
						return loop.Result{ID: c.ID, Code: loop.Unknown, Output: json.RawMessage(`{"code":"input_binding_receipt_failed"}`)}
					}
					if err := r.recordBindingEffect(ctx, input, s.turnID, clean, before, result); err != nil {
						return loop.Result{ID: c.ID, Code: loop.Unknown, Output: json.RawMessage(`{"code":"input_binding_effect_failed"}`)}
					}
					if err := r.publishBindingChip(ctx, input); err != nil {
						return loop.Result{ID: c.ID, Code: loop.Unknown, Output: json.RawMessage(`{"code":"input_binding_receipt_failed"}`)}
					}
				}
				return result
			},
		}
	}
	return declarations
}

// steeringTool rebuilds a tool on the runtime that owns the selected input.
// Families that close over the runtime or the event are prepared again so a
// channel follow-up, record write, or work preparation keeps that channel.
// Result tools keep the speaking turn's prepared reply shapes, and their grant
// uses the selected conversation's turn so a new result is checked there.
// Persona preparation needs the turn's persona and stays on the original tool.
func (r *conversationRuntime) steeringTool(ctx context.Context, turnID string, event ledger.Event, shapes reply.Shapes, name string) (loop.Tool, error) {
	if name == "present_result" || name == "present_surface" || name == "read_result" {
		selected, err := r.selectedResultTurn(ctx, turnID)
		if err != nil {
			return loop.Tool{}, err
		}
		turnID = selected
	}
	prep := &toolPreparation{event: event, replyShapes: shapes}
	var found loop.Tool
	for _, prepare := range []toolPreparer{
		r.prepareRecordTools,
		r.prepareBindingWorkTools,
		r.prepareResultTools,
		r.prepareGoalWorkTools,
		r.prepareRunTools,
		r.prepareWebTools,
		r.prepareA2ATools,
		r.prepareDispatchTools,
		r.prepareWorkTools,
		r.prepareWorkerTools,
		r.prepareDelegationTools,
		r.prepareTimerTools,
		r.prepareExchangeTools,
	} {
		family := map[string]loop.Tool{}
		_, err := prepare(turnID, prep, family)
		tool, ok := family[name]
		if err != nil && ok && tool.Execute != nil {
			return loop.Tool{}, err
		}
		if err != nil || !ok || tool.Execute == nil {
			continue
		}
		found = tool
	}
	return found, nil
}

// selectedResultTurn is the turn whose conversation matches the runtime that
// owns the selected input. A private turn consuming channel steering would
// otherwise keep its own id, and the result grant would look up the private
// conversation while the tool creates under result:new: on the channel.
func (r *conversationRuntime) selectedResultTurn(ctx context.Context, turnID string) (string, error) {
	var conversation string
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT conversation FROM turn_runs WHERE id=?`, turnID).Scan(&conversation)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil || conversation == r.conversation {
		return turnID, err
	}
	var id string
	err = r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT id FROM turn_runs WHERE conversation=? AND state='active' ORDER BY rowid DESC LIMIT 1`, r.conversation).Scan(&id)
	})
	return id, err
}

func (r *conversationRuntime) bindWorkCall(ctx context.Context, input ledger.Event, index int, turnID string, c provider.ToolCall) *loop.Result {
	// Explicit steering provenance cannot borrow the original turn's tools.
	if input.PersonID != r.person || input.AgentID != r.agent || input.ConversationID != r.conversation || r.channelBinding != nil && (!r.channelBinding.Scope.Tools || r.validateChannel(ctx) != nil) {
		result := loop.Result{ID: c.ID, Code: loop.Denied, Output: json.RawMessage(`{"code":"input_scope_denied"}`)}
		return &result
	}
	choices, err := r.currentInputChoices(ctx, input)
	if err != nil {
		result := bindingFailure(c.ID, err)
		return &result
	}
	if index >= len(choices) {
		result := bindingFailure(c.ID, binding.ErrChoice)
		return &result
	}
	choice := choices[index]
	if choice.Question != "" || choice.Cancelled {
		result := bindingFailure(c.ID, errBindingPending)
		return &result
	}
	// An unresolved reply keeps the referenced input's ambiguity. Without a
	// reply reference, a no-rule follow-up keeps the previous input's targets.
	// Naming a worker in a tool call must not silently choose between them.
	if len(choice.Tied) < 2 && (choice.Target.Class == "" || choice.Target.Class == "none") {
		var parent []binding.Choice
		err := r.db.Read(ctx, func(tx *canon.Tx) error {
			id := input.Surface.ReplyTo
			action := bindingAction(c.Name)
			if id == "" && choice.Decider == "no-rule" && (action == "stop" || action == "steer") {
				_, _, previous, err := r.previousInputBinding(ctx, tx, input)
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				if err != nil {
					return err
				}
				id = previous
			}
			if id == "" {
				return nil
			}
			var err error
			parent, err = readInputChoices(ctx, tx, ledger.Event{ID: id})
			// A card action replies to the agent event that presented the card,
			// which holds no choices.
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		})
		if err != nil {
			result := bindingFailure(c.ID, err)
			return &result
		}
		var targets []binding.Target
		seen := map[binding.Target]bool{}
		for _, earlier := range parent {
			if earlier.Question != "" || earlier.Cancelled {
				continue
			}
			// A retained read stores the rest on More. The next control still sees them.
			candidates := append([]binding.Target{}, earlier.Tied...)
			candidates = append(candidates, earlier.More...)
			if len(candidates) == 0 {
				candidates = []binding.Target{overlayChoiceTarget(binding.Target{}, []binding.Choice{earlier})}
			}
			for _, target := range candidates {
				if (target.Class == "existing" || target.Class == "new") && !seen[target] {
					targets = append(targets, target)
					seen[target] = true
				}
			}
		}
		if len(targets) > 1 {
			// One question lists at most five answers. Keep the rest for the
			// next page instead of failing the five-target check.
			shown, rest := binding.QuestionPage(targets)
			choice.Tied = shown
			choice.Target = shown[0]
			choice.More = rest
		}
	}
	if _, err := r.journal().Get(ctx, "binding-tool:"+turnID+":"+c.ID); err == nil {
		return nil
	}
	if bindingAction(c.Name) != "" && choice.Decider == "no-rule" {
		pending, err := waiting.NewConversationQuestions(r.db).Pending(ctx, r.person, r.agent)
		if err != nil {
			result := bindingFailure(c.ID, err)
			return &result
		}
		for _, q := range pending {
			if q.Binding != nil && q.Conversation == r.conversation {
				visible, err := r.questionVisible(ctx, q)
				if err != nil {
					result := bindingFailure(c.ID, err)
					return &result
				}
				if visible {
					result := bindingFailure(c.ID, errBindingPending)
					return &result
				}
			}
		}
	}
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(c.Arguments), &args) != nil {
		result := bindingFailure(c.ID, binding.ErrChoice)
		return &result
	}
	str := func(key string) string { var v string; _ = json.Unmarshal(args[key], &v); return v }
	id, title, kind := "", "", ""
	var taskTitles []string
	goalRecord := false
	switch c.Name {
	case "read_record", "write_record":
		id = str("id")
		if id == "" && c.Name == "write_record" && (str("kind") == "todo" || str("kind") == "project" || str("kind") == "initiative") {
			title = str("title")
			kind = str("kind")
		}
	case "read_work_progress", "follow_work":
		id = str("id")
		if c.Name == "follow_work" {
			id, err = r.followTaskID(ctx, id)
		} else if taskID, ok := workerFollowTask(id); ok && taskID != id {
			if _, lookupErr := r.work.Get(ctx, taskID); lookupErr == nil {
				id = taskID
			}
		}
	case "worker_input", "worker_interrupt":
		// These tools name a session, not a work item. The session's task is
		// the binding identity, so a settled choice still applies, including a
		// session retained in the recovery record after a restart.
		id, err = r.workerSessionTask(ctx, str("session"))
	case "schedule_work":
		var tasks []struct {
			What string `json:"what"`
		}
		_ = json.Unmarshal(args["tasks"], &tasks)
		for _, task := range tasks {
			taskTitles = append(taskTitles, task.What)
		}
		if len(taskTitles) > 0 {
			title = taskTitles[0]
			kind = "task"
		}
	case "draft_plan":
		id = str("root_id")
	case "propose_candidate":
		title = str("title")
		kind = "todo"
	case "revise_code_goal", "submit_step_graph", "read_step_graph":
		// Resolve through the goal package. A goal that does not resolve must
		// not skip the person's chosen project.
		recordID, ok := goal.ProjectRecordID(str("goal_id"))
		if !ok {
			result := bindingFailure(c.ID, errBindingPending)
			return &result
		}
		id = recordID
		goalRecord = true
	case "prepare_work":
		id = str("task_id")
		title = str("title")
		kind = "task"
	case "dispatch_code_goal":
		title = str("outcome")
		kind = "project"
	case "start_run":
		title = str("Goal")
		kind = "run"
	case "dispatch_work":
		title = str("what")
		if title == "" {
			title = str("title")
		}
		kind = "task"
	}

	if err != nil {
		result := bindingFailure(c.ID, err)
		return &result
	}
	if id == "" && title == "" && len(choice.Tied) < 2 {
		return nil
	}
	var state binding.State
	err = r.db.Read(ctx, func(tx *canon.Tx) error { var e error; state, e = r.bindingState(ctx, tx, input); return e })
	if err != nil {
		result := bindingFailure(c.ID, err)
		return &result
	}
	if len(choice.Tied) > 1 {
		action := bindingAction(c.Name)
		if action != "" {
			choice.Action = action
			if binding.NeedsQuestion(state, choice) {
				choice.Decider = ""
				choice.Probabilities = nil
				choice.CreatedID = ""
				saved, err := r.saveModelBinding(ctx, input, "binding-tie:"+turnID+":"+c.ID, []binding.Choice{choice}, "model")
				if err == nil {
					for _, c := range saved {
						if c.Question != "" {
							err = r.questionDecisions().PublishQuestion(ctx, c.Question)
						}
					}
				}
				if err == nil {
					err = errBindingPending
				}
				result := bindingFailure(c.ID, err)
				return &result
			}
		}
	}
	if id == "" && title == "" {
		return nil
	}
	target := binding.Target{}
	for _, i := range state.Items {
		if i.ID == id {
			target = binding.Target{Class: "existing", ID: i.ID, Kind: i.Kind}
			break
		}
	}
	if target.Class == "" && title != "" {
		matches := binding.Duplicates(state, title, choice.Close)
		for _, taskTitle := range taskTitles {
			matches = append(matches, binding.Duplicates(state, taskTitle, choice.Close)...)
		}
		if len(matches) > 0 && strings.TrimSpace(choice.Difference) == "" {
			if c.Name == "propose_candidate" {
				for _, match := range matches {
					if match.Kind == "todo" && strings.ToLower(strings.Join(strings.Fields(match.Title), " ")) == strings.ToLower(strings.Join(strings.Fields(title), " ")) {
						// Records retains an equivalent proposal as pending and
						// checks duplication again before any automatic adoption.
						return nil
					}
				}
			}
			raw, _ := json.Marshal(map[string]any{"code": "open_item_matches", "matches": matches, "next": "Bind to an existing match, or use bind_input to record why the new item differs."})
			result := loop.Result{ID: c.ID, Code: loop.Invalid, Output: raw}
			return &result
		}
		target = binding.Target{Class: "new", Kind: kind, Parent: choice.Target.Parent}
	}
	if target.Class == "" {
		if goalRecord {
			result := bindingFailure(c.ID, errBindingPending)
			return &result
		}
		return nil
	} // Original tool supplies its authorization/not-found error.
	// Observing a candidate is not a resolution of the original request.
	// Keep the recommendation and tied targets until bind_input explicitly
	// chooses, or a risky action persists the person's target question above.
	if len(choice.Tied) == 0 {
		choice.Target = target
	}
	choice.Decider = ""
	choice.CreatedID = ""
	choice.Probabilities = nil
	if _, err = r.saveModelBinding(ctx, input, "binding-tool:"+turnID+":"+c.ID, []binding.Choice{choice}, "tool:"+c.Name); err != nil {
		result := bindingFailure(c.ID, err)
		return &result
	}
	return nil
}
func (r *conversationRuntime) prepareBindingWorkTools(turnID string, preparation *toolPreparation, bindings map[string]loop.Tool) ([]provider.ToolDecl, error) {
	for _, name := range []string{"read_work_progress", "follow_work"} {
		bindings[name] = loop.Tool{
			Validate: func(c provider.ToolCall) (string, error) {
				var a struct {
					ID   string `json:"id"`
					Text string `json:"text,omitempty"`
				}
				err := decodeQuestionJSON(c.Arguments, &a)
				if a.ID == "" || name == "follow_work" && strings.TrimSpace(a.Text) == "" {
					err = binding.ErrChoice
				}
				return "work:" + a.ID, err
			},
			Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
				var a struct {
					ID   string `json:"id"`
					Text string `json:"text,omitempty"`
				}
				if decodeQuestionJSON(c.Arguments, &a) != nil {
					return bindingFailure(c.ID, binding.ErrChoice)
				}
				if name == "follow_work" {
					if err := authority.CheckPreset(ctx, r.db, r.epoch, authority.PolicyRequest{Key: "preset:follow:" + turnID + ":" + c.ID, Person: r.person, Agent: r.agent, TaskID: turnID, Tool: c.Name, Target: a.ID, Class: tools.WorkerEffect, Arguments: json.RawMessage(c.Arguments)}); err != nil {
						return loop.Result{ID: c.ID, Code: loop.Denied}
					}
					event := ledger.Event{}
					if preparation != nil {
						event = preparation.event
					}
					// Deliver to the identity the chosen-target gate checked.
					taskID, err := r.followCallTask(ctx, event.ID, turnID, c.ID, a.ID)
					if err != nil {
						return loop.Result{ID: c.ID, Code: loop.Denied}
					}
					// A session kept after restart still names a task card, but
					// its worker is not registered. Sending that card to the
					// live path drops the follow-up. Keep the offline mailbox.
					if session, ok := strings.CutPrefix(a.ID, "session:"); ok && !r.workerSessionLive(session) {
						return r.followSubmitted(ctx, preparation, turnID, c.ID, a.ID, a.Text)
					}
					if _, err := r.work.Get(ctx, taskID); err == nil {
						return r.followWorkerTask(ctx, event, turnID, c.ID, taskID, a.Text)
					}
					return r.followSubmitted(ctx, preparation, turnID, c.ID, taskID, a.Text)
				}
				projection, err := r.ownerSnapshot(ctx)
				if err != nil {
					return bindingFailure(c.ID, err)
				}
				var line any = map[string]string{"state": "unknown"}
				// The binding check keeps the project record id. The progress
				// line uses the bare goal id, so resolve that reference here.
				progressID := a.ID
				if ref, ok := goal.GoalID(a.ID); ok && ref != a.ID {
					progressID = ref
				}
				if projection.Rows != nil {
					for _, row := range *projection.Rows {
						if row.Ref == progressID || row.Ref == a.ID || row.Ref == "run:"+a.ID || row.Ref == "task:"+a.ID || row.Ref == "record:"+a.ID {
							line = row.Line
							break
						}
					}
				}
				raw, _ := json.Marshal(map[string]any{"id": a.ID, "progress": line})
				return loop.Result{ID: c.ID, Code: loop.OK, Output: raw}
			},
		}
	}
	return []provider.ToolDecl{
		provider.ToolDecl(`{"type":"function","name":"read_work_progress","description":"Answer progress questions from the current item's progress line, never remembered status. Unknown stays unknown. Names the input's work target.","parameters":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}}`),
		provider.ToolDecl(`{"type":"function","name":"follow_work","description":"Send a follow-up to the named running task or background run under its existing controls. Resolve a risky target tie with bind_input first; a pending instruction must not be sent. This does not cancel work.","parameters":{"type":"object","properties":{"id":{"type":"string"},"text":{"type":"string"}},"required":["id","text"],"additionalProperties":false}}`),
	}, nil
}

func workerFollowTask(id string) (string, bool) {
	raw := id
	prefixed := false
	if rest, ok := strings.CutPrefix(raw, "task:"); ok {
		raw, prefixed = rest, true
	}
	if i := strings.LastIndexByte(raw, ':'); i > 0 {
		suffix := raw[i+1:]
		if n, err := strconv.Atoi(suffix); err == nil && n >= 0 && strconv.Itoa(n) == suffix {
			return raw[:i], true
		}
	}
	if prefixed {
		return raw, raw != ""
	}
	return id, id != ""
}

func (r *conversationRuntime) workerSessionLive(session string) bool {
	if r.workers == nil || session == "" {
		return false
	}
	r.workers.mu.Lock()
	defer r.workers.mu.Unlock()
	_, ok := r.workers.sessions[session]
	return ok
}

// followSubmitted stores a follow-up through the scoped run controls. A
// session reference reaches the worker mailbox, including one retained only
// in recovery after restart.
func (r *conversationRuntime) followSubmitted(ctx context.Context, preparation *toolPreparation, turnID, callID, runID, text string) loop.Result {
	var source json.RawMessage
	if r.channelBinding != nil {
		var e gateway.Inbound
		if preparation == nil {
			return loop.Result{ID: callID, Code: loop.Denied}
		}
		receiptID, err := r.channelReceiptEventID(ctx, preparation.event)
		if err != nil {
			return loop.Result{ID: callID, Code: loop.Denied}
		}
		err = r.db.Read(ctx, func(tx *canon.Tx) error {
			var raw []byte
			err := tx.QueryRowContext(ctx, "SELECT i.body FROM inbox i WHERE 'channel-input:'||i.id=?", receiptID).Scan(&raw)
			if err != nil {
				return err
			}
			return json.Unmarshal(raw, &e)
		})
		if err != nil {
			return loop.Result{ID: callID, Code: loop.Denied}
		}
		source, _ = json.Marshal(channelFollowSource{Address: r.channelAddress, Binding: *r.channelBinding, Sender: e.Sender, Direct: e.Direct, Addressed: e.Addressed})
	}
	_, err := r.followRunWithSource(ctx, clientsrv.Session{PersonID: r.person, Epoch: r.epoch}, protocol.Envelope{MessageID: "binding-follow:" + turnID + ":" + callID, Payload: protocol.FollowUp{RunID: runID, Text: text}}, source)
	if err != nil {
		return loop.Result{ID: callID, Code: loop.Denied}
	}
	return loop.Result{ID: callID, Code: loop.OK, Output: json.RawMessage(`{"state":"submitted"}`)}
}

// followWorkerTask delivers a model follow-up as an agent message. The
// coordination allowance replenishes once for the person input, not per call.
func (r *conversationRuntime) followWorkerTask(ctx context.Context, input ledger.Event, turnID, callID, taskID, text string) loop.Result {
	card, err := r.work.Get(ctx, taskID)
	if err != nil || card.Run == nil || r.workers == nil {
		return loop.Result{ID: callID, Code: loop.Denied}
	}
	var entry *workerSession
	for _, candidate := range r.workers.entries() {
		if candidate.thread.ID == card.Run.ThreadID {
			entry = candidate
			break
		}
	}
	if entry == nil || entry.adapter == nil {
		return loop.Result{ID: callID, Code: loop.Denied}
	}
	snap, err := entry.adapter.Snapshot(ctx, entry.thread)
	if err != nil {
		return loop.Result{ID: callID, Code: loop.Denied}
	}
	raw, _ := json.Marshal(workerArgs{Session: entry.thread.ID, Turn: snap.Thread.TurnID, Generation: snap.Thread.ProcessGeneration, Binding: snap.Thread.BindingEpoch, Text: text})
	in, err := workerSchema("worker_input", raw)
	if err != nil {
		return loop.Result{ID: callID, Code: loop.Denied}
	}
	// The registry is shared. Check and deliver as the requester so an
	// unrelated conversation cannot steer or speak as the owner.
	if !entry.origin.workerConversation(r) {
		return loop.Result{ID: callID, Code: loop.Denied}
	}
	ctx = context.WithValue(ctx, workerCallIdentityKey{}, "binding-follow:"+turnID+":"+callID)
	result, err := r.workerOperation(ctx, "worker_input", turnID, input, in)
	if err != nil || result.State != tools.Applied {
		return loop.Result{ID: callID, Code: loop.Denied}
	}
	return loop.Result{ID: callID, Code: loop.OK, Output: json.RawMessage(`{"state":"submitted"}`)}
}

func createsBindingItem(name string) bool {
	switch name {
	case "write_record", "propose_candidate", "prepare_work", "dispatch_code_goal", "start_run", "dispatch_work", "schedule_work":
		return true
	}
	return false
}
func (r *conversationRuntime) recordCreatedBinding(ctx context.Context, input ledger.Event, index int, turnID string, call provider.ToolCall, result loop.Result) error {
	if !createsBindingItem(call.Name) {
		return nil
	}
	return r.db.Write(ctx, func(tx *canon.Tx) error {
		var bound int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM conv_event WHERE id=? AND parent_id=? AND scaffold_kind=?", "binding-tool:"+turnID+":"+call.ID, input.ID, modelBindingKind).Scan(&bound); err != nil {
			return err
		}
		if bound == 0 {
			return nil
		}
		choices, err := readInputChoices(ctx, tx, input)
		if err != nil {
			return err
		}
		if index >= len(choices) {
			return binding.ErrChoice
		}
		c := choices[index]
		if c.Target.Class != "new" {
			return nil
		}
		c.CreatedID = createdBindingID(result.Output)
		if c.CreatedID == "" {
			return nil
		}
		return appendInputChoices(ctx, tx, input, "binding-created:"+turnID+":"+call.ID, []binding.Choice{c})
	})
}

// prepare_work returns a workspace with task_id. schedule_work returns task
// ids under result. One id becomes the focus; several stay unresolved.
func createdBindingID(output json.RawMessage) string {
	var receipt struct {
		ID       string          `json:"id"`
		RecordID string          `json:"record_id"`
		TaskID   string          `json:"task_id"`
		Result   json.RawMessage `json:"result"`
	}
	if json.Unmarshal(output, &receipt) != nil {
		return ""
	}
	if receipt.RecordID != "" {
		return receipt.RecordID
	}
	if receipt.ID != "" {
		return receipt.ID
	}
	if receipt.TaskID != "" {
		return receipt.TaskID
	}
	if len(receipt.Result) == 0 {
		return ""
	}
	var one string
	if json.Unmarshal(receipt.Result, &one) == nil && one != "" {
		return one
	}
	var many []string
	if json.Unmarshal(receipt.Result, &many) == nil && len(many) == 1 && many[0] != "" {
		return many[0]
	}
	return ""
}

// An answer event keeps the original channel receipt on its parent chain.
func (r *conversationRuntime) channelReceiptEventID(ctx context.Context, event ledger.Event) (string, error) {
	current := event
	seen := map[string]bool{}
	for {
		if strings.HasPrefix(current.ID, "channel-input:") {
			return current.ID, nil
		}
		if current.ID != "" {
			if seen[current.ID] {
				return "", channels.ErrDenied
			}
			seen[current.ID] = true
		}
		parentID := current.ParentID
		if parentID == "" || seen[parentID] {
			return "", channels.ErrDenied
		}
		if strings.HasPrefix(parentID, "channel-input:") {
			return parentID, nil
		}
		parent, err := r.journal().Get(ctx, parentID)
		if err != nil {
			return "", err
		}
		if parent.AgentID != event.AgentID || parent.ConversationID != event.ConversationID || parent.Surface.Kind != "channel" || parent.Origin.Kind != "native" || parent.ReplayOf != "" {
			return "", channels.ErrDenied
		}
		if parent.ScaffoldKind != "question_answer" && parent.ScaffoldKind != "worker_completion" {
			return "", channels.ErrDenied
		}
		current = parent
	}
}

func bindingAction(name string) string {
	switch name {
	case "follow_work", "worker_input":
		return "steer"
	case "worker_interrupt", "cancel_timer":
		return "stop"
	case "start_run", "dispatch_code_goal", "dispatch_work", "schedule_work", "worker_start", "submit_step_graph", "build_mock", "revise_code_goal", "call_tool", "refresh_work":
		return "start"
	case "send_handoff", "remote_agent", "begin_artifact_change":
		return "send"
	case "trash_record":
		return "delete"
	}
	return ""
}

// workerSessionTask resolves a worker session to the work item it controls.
// An owner-started session carries a conversation turn as its task id and names
// no work item, so the original tool keeps its own checks. Recovery records
// retain the task identity when the session is absent after a restart.
func (r *conversationRuntime) workerSessionTask(ctx context.Context, session string) (string, error) {
	if session == "" {
		return "", nil
	}
	if r.workers != nil {
		r.workers.mu.Lock()
		entry := r.workers.sessions[session]
		r.workers.mu.Unlock()
		if entry != nil {
			entry.control.Lock()
			defer entry.control.Unlock()
			return entry.thread.TaskID, nil
		}
	}
	records, err := (recovery.Recovery{DB: r.db}).Records(ctx)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if record.Thread.ID == session {
			return record.Thread.TaskID, nil
		}
	}
	return "", nil
}

// followTaskID resolves a follow_work id to the task the follow-up reaches.
// The chosen-target gate and execution share it, so the identity that is
// checked is the identity that receives the instruction. A literal task id
// wins over a generation suffix: a task named "a:1" is not generation 1 of "a".
func (r *conversationRuntime) followTaskID(ctx context.Context, ref string) (string, error) {
	if session, ok := strings.CutPrefix(ref, "session:"); ok {
		return r.workerSessionTask(ctx, session)
	}
	if task, ok := strings.CutPrefix(ref, "task:"); ok {
		var card work.Card
		err := r.db.Read(ctx, func(tx *canon.Tx) error {
			var err error
			card, err = work.ReadReference(ctx, tx, task)
			return err
		})
		return card.Task.ID, err
	}
	if card, err := r.work.Get(ctx, ref); err == nil {
		return card.Task.ID, nil
	}
	if taskID, ok := workerFollowTask(ref); ok && taskID != ref {
		if _, err := r.work.Get(ctx, taskID); err == nil {
			return taskID, nil
		}
	}
	return ref, nil
}

// followCallTask returns the task the gate checked for this call. It reads the
// saved binding so execution does not resolve the reference a second time; a
// call the gate left unbound (no settled choice) resolves once here.
func (r *conversationRuntime) followCallTask(ctx context.Context, inputID, turnID, callID, ref string) (string, error) {
	if inputID != "" {
		event, err := r.journal().Get(ctx, "binding-tool:"+turnID+":"+callID)
		if err == nil && event.ParentID == inputID {
			var batch bindingBatch
			if json.Unmarshal([]byte(event.Body.Text()), &batch) == nil && len(batch.Bindings) == 1 {
				if target := batch.Bindings[0].Target; target.Class == "existing" && target.ID != "" {
					return target.ID, nil
				}
			}
		}
	}
	return r.followTaskID(ctx, ref)
}
