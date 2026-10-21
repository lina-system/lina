// SPDX-License-Identifier: Apache-2.0
package turn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

// EffectKey uses the task id, or the turn id for a conversational effect. Target
// comes from the trusted schema's normalization; intent is the stable call id.
// The unambiguous tuple excludes epochs so a restart finds the same receipt.
func EffectKey(scope, action, target, intent string) string {
	b, _ := json.Marshal([4]string{scope, action, target, intent})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Tool binds a registered tool to trusted Core authorization and scope. The
// registry remains the only dispatch path: it commits intent before the handler
// and commits its receipt before returning. Schemas must be deterministic/pure
// and accept their normalized JSON output when the registry revalidates custom
// input before dispatch.
func Tool(registry *tools.Registry, spec tools.Spec, base tools.Call, scope string) loop.Tool {
	validate := func(c provider.ToolCall) (tools.Input, error) {
		if registry == nil || spec.Schema == nil || scope == "" || c.ID == "" || c.Name != spec.Name || !c.Custom && !json.Valid([]byte(c.Arguments)) {
			return tools.Input{}, tools.ErrInvalid
		}
		in, err := spec.Schema(json.RawMessage(c.Arguments))
		if err != nil {
			return in, err
		}
		if in.Target == "" || in.What == "" || in.Where == "" || in.ScopeKey == "" || !json.Valid(in.Arguments) {
			return in, tools.ErrInvalid
		}
		return in, nil
	}
	return loop.Tool{
		Validate: func(c provider.ToolCall) (string, error) {
			in, err := validate(c)
			if err != nil {
				return "", err
			}
			if in.Write {
				return in.Target, nil
			}
			return "", nil
		},
		Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
			in, err := validate(c)
			if err != nil {
				return loop.Result{ID: c.ID, Code: loop.Invalid}
			}
			call := base
			call.Name = c.Name
			call.Args = json.RawMessage(c.Arguments)
			if c.Custom {
				call.Args = in.Arguments
			}
			call.Key = EffectKey(scope, c.Name, in.Target, c.ID)
			result, err := registry.Dispatch(ctx, call)
			code := loop.Unknown
			switch result.State {
			case tools.Applied:
				code = loop.OK
			case tools.Failed:
				code = loop.Failed
			case tools.Cancelled:
				code = loop.Aborted
			case tools.Refused:
				code = loop.Denied
			case tools.None, tools.Unknown:
				code = loop.Unknown
			}
			if errors.Is(err, tools.ErrApprovalRequired) {
				code = loop.AwaitingPerson
			}
			return loop.Result{ID: c.ID, Code: code, Output: result.Body}
		},
	}
}

// ResumeApproval dispatches the stored arguments after an authenticated answer.
// It starts a fresh registry deadline; no tool deadline spans the person's wait.
// Base supplies trusted identity/account facts, never replacement arguments.
// The original awaiting_person result stays immutable; the effect ledger owns
// the later receipt, which the consuming flow can announce at a turn boundary.
func ResumeApproval(ctx context.Context, service *authority.Service, registry *tools.Registry, id string, base tools.Call) (tools.Result, error) {
	if service == nil || registry == nil {
		return tools.Result{State: tools.Refused}, tools.ErrInvalid
	}
	approval, err := service.Approval(ctx, id)
	if err != nil {
		return tools.Result{State: tools.Refused}, err
	}
	if approval.Person != base.Auth.Person || approval.Agent != base.Auth.Agent || approval.Epoch != base.Auth.Epoch {
		return tools.Result{State: tools.Refused}, tools.ErrInvalid
	}
	base.Key = approval.EffectKey
	base.Name = approval.Tool
	base.Args = append(json.RawMessage(nil), approval.Arguments...)
	return registry.Dispatch(ctx, base)
}
