// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type toolBindingRef struct {
	InputEventID string
	Input        string
	Text         string
	Request      int
	Ordinal      bool
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
				// A target-question continuation resumes the answered request.
				// An explicit ordinal still selects the request it names.
				if s.bindingSource != nil && ref.Input == "" && ref.InputEventID == "" && !ref.Ordinal {
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
				// follow_work's prepared closure carries the turn that built it.
				// A selected steering input must submit on that input's runtime,
				// so the follow-up keeps the steering channel's receipt.
				var result loop.Result
				if clean.Name == "follow_work" {
					result = r.submitFollowWork(ctx, c.ID, s.turnID, clean.Arguments, &input)
				} else {
					result = tool.Execute(ctx, clean)
				}
				if result.Code == loop.OK {
					if err := r.recordCreatedBinding(ctx, input, ref.Request, s.turnID, clean, result); err != nil {
						return loop.Result{ID: c.ID, Code: loop.Unknown, Output: json.RawMessage(`{"code":"input_binding_receipt_failed"}`)}
					}
				}
				return result
			},
		}
	}
	return declarations
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
			id, err = r.followTargetTask(ctx, id)
		}
	case "worker_input", "worker_interrupt":
		// A worker session steers the task it controls. Resolving it here keeps
		// a person's chosen target authoritative: the tool cannot deliver the
		// instruction to another task in the same conversation.
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
	case "revise_code_goal":
		id = str("goal_id")
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
					var event *ledger.Event
					if preparation != nil {
						event = &preparation.event
					}
					return r.submitFollowWork(ctx, c.ID, turnID, c.Arguments, event)
				}
				line, err := r.workProgress(ctx, a.ID)
				if err != nil {
					return bindingFailure(c.ID, err)
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

// submitFollowWork queues a follow-up on this runtime. event is the input whose
// channel receipt must travel with the note. A target answer is a continuation:
// its receipt is the originating channel input, not the system event itself.
func (r *conversationRuntime) submitFollowWork(ctx context.Context, callID, turnID, arguments string, event *ledger.Event) loop.Result {
	var a struct {
		ID   string `json:"id"`
		Text string `json:"text,omitempty"`
	}
	if decodeQuestionJSON(arguments, &a) != nil {
		return bindingFailure(callID, binding.ErrChoice)
	}
	if err := authority.CheckPreset(ctx, r.db, r.epoch, authority.PolicyRequest{Key: "preset:follow:" + turnID + ":" + callID, Person: r.person, Agent: r.agent, TaskID: turnID, Tool: "follow_work", Target: a.ID, Class: tools.WorkerEffect, Arguments: json.RawMessage(arguments)}); err != nil {
		return loop.Result{ID: callID, Code: loop.Denied}
	}
	var source json.RawMessage
	if r.channelBinding != nil {
		var inbound gateway.Inbound
		if event == nil {
			return loop.Result{ID: callID, Code: loop.Denied}
		}
		receiptID := event.ID
		if !strings.HasPrefix(receiptID, "channel-input:") {
			var err error
			receiptID, err = r.originatingChannelInputID(ctx, *event)
			if err != nil {
				return loop.Result{ID: callID, Code: loop.Denied}
			}
		}
		err := r.db.Read(ctx, func(tx *canon.Tx) error {
			var raw []byte
			err := tx.QueryRowContext(ctx, "SELECT i.body FROM inbox i WHERE 'channel-input:'||i.id=?", receiptID).Scan(&raw)
			if err != nil {
				return err
			}
			return json.Unmarshal(raw, &inbound)
		})
		if err != nil {
			return loop.Result{ID: callID, Code: loop.Denied}
		}
		source, _ = json.Marshal(channelFollowSource{Address: r.channelAddress, Binding: *r.channelBinding, Sender: inbound.Sender, Direct: inbound.Direct, Addressed: inbound.Addressed})
	}
	id := a.ID
	if card, err := r.work.Get(ctx, id); err == nil {
		generation := 0
		if card.Run != nil {
			generation = card.Run.Generation
		}
		id = fmt.Sprintf("task:%s:%d", card.Task.ID, generation)
	}
	_, err := r.followRunWithSource(ctx, clientsrv.Session{PersonID: r.person, Epoch: r.epoch}, protocol.Envelope{MessageID: "binding-follow:" + turnID + ":" + callID, Payload: protocol.FollowUp{RunID: id, Text: a.Text}}, source)
	if err != nil {
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
		var receipt struct {
			ID       string   `json:"id"`
			RecordID string   `json:"record_id"`
			TaskID   string   `json:"task_id"`
			Result   []string `json:"result"`
		}
		if json.Unmarshal(result.Output, &receipt) != nil {
			return nil
		}
		c.CreatedID = receipt.ID
		if receipt.RecordID != "" {
			c.CreatedID = receipt.RecordID
		}
		switch call.Name {
		case "prepare_work":
			c.CreatedID = receipt.TaskID
		case "schedule_work":
			// The gate binds this call to its first task's title.
			if len(receipt.Result) > 0 {
				c.CreatedID = receipt.Result[0]
			}
		}
		if c.CreatedID == "" {
			return nil
		}
		return appendInputChoices(ctx, tx, input, "binding-created:"+turnID+":"+call.ID, []binding.Choice{c})
	})
}

func bindingAction(name string) string {
	switch name {
	case "follow_work", "worker_input", "worker_interrupt":
		// Stopping a worker is steering the running task it controls. A retained
		// tie must ask before cancellation, the same as any other steer.
		return "steer"
	case "start_run", "dispatch_code_goal", "dispatch_work", "schedule_work", "worker_start":
		return "start"
	case "send_handoff":
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

// Resolve the same generation-bound task references used by owner controls.
// Raw task and background-run IDs retain their identity.
func (r *conversationRuntime) followTargetTask(ctx context.Context, ref string) (string, error) {
	// Match execution's precedence when a raw task ID contains a prefix.
	if card, err := r.work.Get(ctx, ref); err == nil {
		return card.Task.ID, nil
	}
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
	return ref, nil
}
