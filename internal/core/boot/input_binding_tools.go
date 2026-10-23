// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type toolBindingRef struct {
	Input   string `json:"input,omitempty"`
	Request int    `json:"request"`
}

func stripToolBinding(c provider.ToolCall) (provider.ToolCall, toolBindingRef, error) {
	var args map[string]json.RawMessage
	var ref toolBindingRef
	if err := json.Unmarshal([]byte(c.Arguments), &args); err != nil || args == nil {
		return c, ref, binding.ErrChoice
	}
	if raw, ok := args["input_binding"]; ok {
		if decodeQuestionJSON(string(raw), &ref) != nil || ref.Request < 0 || ref.Request >= 32 {
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
		properties["input_binding"] = map[string]any{"type": "object", "description": "Request ordinal from bind_input, default 0. Optional input is a consumed person input; use this when acting on a later request or steering.", "properties": map[string]any{"input": map[string]string{"type": "string"}, "request": map[string]any{"type": "integer", "minimum": 0, "maximum": 31}}, "required": []string{"request"}, "additionalProperties": false}
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
				if s.bindingSource != nil && ref.Input == "" {
					ref.Request = s.bindingRequest
				}
				input, r, err := s.bindingInput(ref.Input)
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
				result := tool.Execute(ctx, clean)
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
			raw, _ := json.Marshal(map[string]any{"code": "open_item_matches", "matches": matches, "next": "Bind to an existing match, or use bind_input to record why the new item differs."})
			result := loop.Result{ID: c.ID, Code: loop.Invalid, Output: raw}
			return &result
		}
		target = binding.Target{Class: "new", Kind: kind, Parent: choice.Target.Parent}
	}
	if target.Class == "" {
		return nil
	} // Original tool supplies its authorization/not-found error.
	choice.Target = target
	choice.Decider = ""
	choice.CreatedID = ""
	choice.Probabilities = nil
	choice.Tied = nil
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
					var source json.RawMessage
					if r.channelBinding != nil {
						var e gateway.Inbound
						if preparation == nil {
							return loop.Result{ID: c.ID, Code: loop.Denied}
						}
						err := r.db.Read(ctx, func(tx *canon.Tx) error {
							var raw []byte
							err := tx.QueryRowContext(ctx, "SELECT i.body FROM inbox i WHERE 'channel-input:'||i.id=?", preparation.event.ID).Scan(&raw)
							if err != nil {
								return err
							}
							return json.Unmarshal(raw, &e)
						})
						if err != nil {
							return loop.Result{ID: c.ID, Code: loop.Denied}
						}
						source, _ = json.Marshal(channelFollowSource{Address: r.channelAddress, Binding: *r.channelBinding, Sender: e.Sender, Direct: e.Direct, Addressed: e.Addressed})
					}
					id := a.ID
					if _, err := r.work.Get(ctx, id); err == nil {
						id = "task:" + id
					}
					_, err := r.followRunWithSource(ctx, clientsrv.Session{PersonID: r.person, Epoch: r.epoch}, protocol.Envelope{MessageID: "binding-follow:" + turnID + ":" + c.ID, Payload: protocol.FollowUp{RunID: id, Text: a.Text}}, source)
					if err != nil {
						return loop.Result{ID: c.ID, Code: loop.Denied}
					}
					return loop.Result{ID: c.ID, Code: loop.OK, Output: json.RawMessage(`{"state":"submitted"}`)}
				}
				projection, err := r.ownerSnapshot(ctx)
				if err != nil {
					return bindingFailure(c.ID, err)
				}
				var line any = map[string]string{"state": "unknown"}
				if projection.Rows != nil {
					for _, row := range *projection.Rows {
						if row.Ref == a.ID || row.Ref == "run:"+a.ID || row.Ref == "task:"+a.ID || row.Ref == "record:"+a.ID {
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
			ID       string `json:"id"`
			RecordID string `json:"record_id"`
		}
		if json.Unmarshal(result.Output, &receipt) != nil {
			return nil
		}
		c.CreatedID = receipt.ID
		if receipt.RecordID != "" {
			c.CreatedID = receipt.RecordID
		}
		if c.CreatedID == "" {
			return nil
		}
		return appendInputChoices(ctx, tx, input, "binding-created:"+turnID+":"+call.ID, []binding.Choice{c})
	})
}

func bindingAction(name string) string {
	switch name {
	case "follow_work", "worker_input":
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
