// SPDX-License-Identifier: Apache-2.0

// Package exec binds Core-owned result operations to the authorized registry.
package exec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/result"
	"github.com/thisisjun786/lina/kit/tools"
)

// ResultService is implemented by the results flow. Composition supplies it;
// neither L4 package imports the other.
type ResultService interface {
	Present(context.Context, result.Scope, result.PresentRequest) (result.Card, error)
	Read(context.Context, result.Scope, result.ReadRequest) (result.Result, error)
	Save(context.Context, result.Scope, result.SaveRequest) (result.SaveReceipt, error)
}

// AuthorizeResult supplies current trusted grant facts for this operation.
// A read grant need not pin a revision; write revisions are checked again by
// both authority and the result store. Input is already schema-normalized.
type AuthorizeResult func(context.Context, string, tools.Input) (tools.Call, error)

var errNotAuthorized = errors.New("not_authorized")

func key(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func output(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
func refusal(err error) json.RawMessage {
	if errors.Is(err, result.ErrSurfaceValidation) {
		return output(map[string]any{"version": "v0.9", "error": map[string]any{"code": "VALIDATION_FAILED", "surfaceId": "", "path": "/", "message": "Card does not match lina.cards 1.0."}})
	}
	code := "unavailable"
	switch {
	case errors.Is(err, result.ErrSurfaceValidation):
		code = "VALIDATION_FAILED"
	case errors.Is(err, result.ErrMalformed), errors.Is(err, tools.ErrInvalid):
		code = "malformed"
	case errors.Is(err, errNotAuthorized):
		code = "not_authorized"
	default:
		// The service's closed error codes cross the interface without importing
		// another L4 flow; opaque storage errors never become model/user content.
		switch err.Error() {
		case "stale_revision", "out_of_scope", "unavailable":
			code = err.Error()
		}
	}
	return output(struct {
		Code  string `json:"code"`
		State string `json:"effect_state"`
	}{code, "refused"})
}

// ResultTools registers a set for one trusted turn. SaveIntent is captured from
// the authenticated person's request, never from model input. Its exact id,
// revision and resolved destination must match the save call in this turn.
// The owner supplies a registry for this binding, with its real gate and leases.
func ResultTools(registry *tools.Registry, service ResultService, scope result.Scope, authorize AuthorizeResult, saveIntent *result.SaveRequest) (map[string]loop.Tool, error) {
	if registry == nil || service == nil || authorize == nil || scope.PersonID == "" || scope.AgentID == "" || scope.ConversationID == "" || scope.TurnID == "" {
		return nil, result.ErrMalformed
	}
	var intent *result.SaveRequest
	if saveIntent != nil {
		copy := *saveIntent
		intent = &copy
	}
	bindings := map[string]loop.Tool{}
	for _, name := range []string{"present_result", "present_surface", "read_result", "save_result"} {
		schema := func(raw json.RawMessage) (tools.Input, error) {
			if len(raw) > (1<<20)+4096 {
				return tools.Input{}, result.ErrMalformed
			}
			var normalized any
			target := "result:new:" + scope.ConversationID
			switch name {
			case "present_result", "present_surface":
				var p result.PresentRequest
				if err := result.Decode(raw, &p); err != nil {
					return tools.Input{}, err
				}
				if name == "present_surface" {
					var batch result.SurfaceBatch
					if p.Type != result.SurfaceType || result.Decode(p.Body, &batch) != nil || len(batch.Messages) == 0 || len(p.Body) > 64<<10 {
						return tools.Input{}, result.ErrSurfaceValidation
					}
				} else if _, err := result.Validate(p.Type, p.Body, result.Limits{}); err != nil {
					return tools.Input{}, err
				}
				if !result.ValidReplyType(p.ReplyType) || p.Title == "" || p.Summary == "" || p.ID == "" && p.BaseRevision != 0 || p.ID != "" && p.BaseRevision <= 0 {
					return tools.Input{}, result.ErrMalformed
				}
				normalized = p
				if p.ID != "" {
					target = "result:" + p.ID
				}
			case "read_result":
				var p result.ReadRequest
				if err := result.Decode(raw, &p); err != nil {
					return tools.Input{}, err
				}
				if p.ID == "" {
					return tools.Input{}, result.ErrMalformed
				}
				normalized = p
				target = "result:" + p.ID
			case "save_result":
				var p result.SaveRequest
				if err := result.Decode(raw, &p); err != nil {
					return tools.Input{}, err
				}
				if p.ID == "" || p.Revision <= 0 {
					return tools.Input{}, result.ErrMalformed
				}
				if intent == nil || *intent != p {
					return tools.Input{}, errNotAuthorized
				}
				normalized = p
				target = "result:" + p.ID
			}
			return tools.Input{Arguments: output(normalized), Target: target, What: name, Where: "conversation", ScopeKey: scope.ConversationID, Write: name != "read_result", Undoable: true}, nil
		}
		spec := tools.Spec{Classify: func(in tools.Input) tools.EffectClass {
			if name == "save_result" {
				return tools.LocalWrite
			}
			if in.Write {
				return tools.PrivateDraft
			}
			return tools.Research
		}, Name: name, Schema: schema, Conc: 1, Deadline: 30 * time.Second, Placement: tools.Core}
		handler := func(ctx context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
			switch name {
			case "present_result", "present_surface":
				var p result.PresentRequest
				_ = json.Unmarshal(in.Arguments, &p)
				var card result.Card
				var err error
				if name == "present_surface" {
					presenter, ok := service.(interface {
						PresentSurface(context.Context, result.Scope, result.PresentRequest) (result.Card, error)
					})
					if !ok {
						return tools.Result{State: tools.Failed, Body: refusal(result.ErrSurfaceValidation)}, nil
					}
					card, err = presenter.PresentSurface(ctx, scope, p)
				} else {
					card, err = service.Present(ctx, scope, p)
				}
				if err != nil {
					return tools.Result{State: tools.Failed, Body: refusal(err)}, nil
				}
				return tools.Result{State: tools.Applied, Body: output(card), Revision: strconv.Itoa(card.Revision)}, nil
			case "read_result":
				var p result.ReadRequest
				_ = json.Unmarshal(in.Arguments, &p)
				r, err := service.Read(ctx, scope, p)
				if err != nil {
					return tools.Result{State: tools.Failed, Body: refusal(err)}, nil
				}
				return tools.Result{State: tools.Applied, Body: output(r)}, nil
			case "save_result":
				var p result.SaveRequest
				_ = json.Unmarshal(in.Arguments, &p)
				r, err := service.Save(ctx, scope, p)
				if r.State == "unknown" {
					return tools.Result{State: tools.Unknown, Body: output(r)}, err
				}
				if err != nil {
					return tools.Result{State: tools.Failed, Body: refusal(err)}, nil
				}
				if r.State != "applied" || r.SavedAs == nil {
					return tools.Result{State: tools.Unknown}, tools.ErrUnknown
				}
				return tools.Result{State: tools.Applied, Body: output(r), Revision: strconv.Itoa(p.Revision)}, nil
			}
			return tools.Result{State: tools.Failed}, tools.ErrInvalid
		}
		if err := registry.Register(spec, handler); err != nil {
			return nil, err
		}
		bindings[name] = loop.Tool{
			Validate: func(c provider.ToolCall) (string, error) {
				// Dispatch returns schema errors as a typed private tool result. Returning
				// an error here would make the loop replace it with its generic invalid.
				in, err := schema([]byte(c.Arguments))
				if err != nil {
					return "", nil
				}
				if in.Write {
					return in.Target, nil
				}
				return "", nil
			},
			Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
				if c.Name != name || c.ID == "" || c.Custom {
					return loop.Result{ID: c.ID, Code: loop.Invalid, Output: refusal(result.ErrMalformed)}
				}
				in, err := schema([]byte(c.Arguments))
				if err != nil {
					return loop.Result{ID: c.ID, Code: loop.Denied, Output: refusal(err)}
				}
				call, err := authorize(ctx, name, in)
				if err != nil {
					return loop.Result{ID: c.ID, Code: loop.Denied, Output: refusal(err)}
				}
				call.Name = name
				call.Args = in.Arguments
				call.Key = key([]string{scope.TurnID, name, in.Target, c.ID})
				call.Auth.Person = scope.PersonID
				call.Auth.Agent = scope.AgentID
				switch name {
				case "present_result", "present_surface":
					var p result.PresentRequest
					_ = json.Unmarshal(in.Arguments, &p)
					call.Auth.Revision = strconv.Itoa(p.BaseRevision)
				case "save_result":
					var p result.SaveRequest
					_ = json.Unmarshal(in.Arguments, &p)
					call.Key = key([]any{p.ID, p.Revision, "result_save", p.Destination})
					call.Auth.Revision = strconv.Itoa(p.Revision)
				}
				r, err := registry.Dispatch(ctx, call)
				if err != nil && r.State == tools.Refused {
					return loop.Result{ID: c.ID, Code: loop.Denied, Output: refusal(err)}
				}
				code := loop.Unknown
				switch r.State {
				case tools.Applied:
					code = loop.OK
				case tools.Failed:
					code = loop.Failed
				case tools.Cancelled:
					code = loop.Aborted
				case tools.Refused:
					code = loop.Denied
				case tools.None, tools.Unknown:
				}
				return loop.Result{ID: c.ID, Code: code, Output: r.Body}
			},
		}
	}
	return bindings, nil
}

func ResultDeclarations() []provider.ToolDecl {
	props := map[string]any{
		"reply_type": map[string]any{"type": "string", "enum": []string{"chat", "status", "comparison", "candidates", "third_party"}},
		"result_id":  map[string]any{"type": "string"}, "base_revision": map[string]any{"type": "integer"},
		"type":  map[string]any{"type": "string", "enum": []string{"summary", "table", "diagram", "chart", "document_draft", "comparison", "plan_view", "file"}},
		"title": map[string]any{"type": "string"}, "summary": map[string]any{"type": "string"},
		"body": map[string]any{"type": "object"}, "completion": result.CompletionSchema(),
	}
	present, _ := json.Marshal(map[string]any{"type": "function", "name": "present_result", "description": "Present a complete typed result. summary is the verdict shown first. Optional reply_type: status, comparison, candidates, third_party or chat. For candidates, one table row or comparison target is one artifact option. A table body has columns [{name,type:text|number|date|boolean,unit}], rows [[values]], optional totals [values]. A comparison body has targets [{label,old,new,hunks:[{old_start,old_count,new_start,new_count}]}], apply:null. A document_draft has blocks (kind: paragraph, text), from_summary:null, optional recipient; blocks may name audience as recipient or person. This does not send or save the result. A reported completion includes completion.work_kind and evidence references to successful tool calls (turn_id, call_id, kind); an empty turn_id means this turn. Missing required evidence is recorded as claimed and shown as not verified. Editing requires result_id and base_revision.", "parameters": map[string]any{"type": "object", "properties": props, "required": []string{"type", "title", "summary", "body"}, "additionalProperties": false}})
	read := provider.ToolDecl(`{"type":"function","name":"read_result","description":"Read a result card in this conversation, including its completion evidence and current revision.","parameters":{"type":"object","properties":{"result_id":{"type":"string"}},"required":["result_id"],"additionalProperties":false}}`)
	surface, _ := json.Marshal(map[string]any{"type": "function", "name": "present_surface", "description": "Present a generated card from lina.cards 1.0, A2UI wire version v0.9. Set type to surface; body is {messages:[...]}. Submit the entire batch in one call. Start with createSurface {surfaceId,catalogId:lina.cards}; updateComponents carries a flat components array with root id root; updateDataModel carries surfaceId, optional JSON pointer path and value; deleteSurface removes a card. Allowed components: Text, Column, Row, List, Card, Divider, Button, CheckBox, ChoicePicker, TextField, DateTimeInput, WorkRow. Text uses text; layouts use children ids; Card/Button use child id; Button and WorkRow use action:{event:{name,context:{field:value or {path:/field}}}}. ChoicePicker uses options:[{label,value}], value:{path:/field}, variant:mutuallyExclusive or multipleSelection. WorkRow uses title,state,elapsed,action. No colors, icons, URLs, code, approval or sign-in controls. Updates require result_id and base_revision. Input is delivered as a person message on the next turn and gives no effect approval.", "parameters": map[string]any{"type": "object", "properties": props, "required": []string{"type", "title", "summary", "body"}, "additionalProperties": false}})
	var declaration map[string]any
	_ = json.Unmarshal(surface, &declaration)
	declaration["parameters"].(map[string]any)["properties"].(map[string]any)["type"] = map[string]any{"type": "string", "enum": []string{"surface"}}
	surface, _ = json.Marshal(declaration)
	return []provider.ToolDecl{present, read, surface}
}
