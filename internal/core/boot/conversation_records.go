// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

func (r *conversationRuntime) recordEnvironment(ctx context.Context, g grant.Grant) (grant.Environment, error) {
	env := grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true, Revision: "new"}
	if !g.Scope[0].Write || g.Scope[0].Target == "records:new" {
		return env, nil
	}
	_, revision, err := r.records.Read(ctx, strings.TrimPrefix(g.Scope[0].Target, "record:"))
	env.Revision = revision
	return env, err
}
func (r *conversationRuntime) prepareRecordTools(turnID, currentEvent string, bindings map[string]loop.Tool) error {
	registry := tools.NewRegistry(r.personaAuthority, r.activities)
	specs, err := coreexec.RegisterRecords(registry, r.records, records.Actor{Person: r.person, Agent: r.agent})
	if err != nil {
		return err
	}
	extra, err := coreexec.RegisterRecordCandidates(registry, r.records, records.Actor{Person: r.person, Agent: r.agent}, currentEvent)
	if err != nil {
		return err
	}
	specs = append(specs, extra...)
	for _, spec := range specs {
		validated := turn.Tool(registry, spec, tools.Call{}, turnID)
		bindings[spec.Name] = loop.Tool{Validate: validated.Validate, Execute: func(ctx context.Context, call provider.ToolCall) loop.Result {
			if _, err := validated.Validate(call); err != nil {
				return loop.Result{ID: call.ID, Code: loop.Invalid}
			}
			in, err := spec.Schema(json.RawMessage(call.Arguments))
			if err != nil {
				return loop.Result{ID: call.ID, Code: loop.Invalid}
			}
			auth := grant.Request{GrantID: turn.EffectKey(turnID, spec.Name, in.Target, call.ID) + ":records", MainID: r.main, Epoch: r.epoch, TaskID: turnID, Host: r.main, OSUser: "core", Person: r.person, Agent: r.agent, Profile: "records"}
			g := grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: spec.Name, Target: in.Target, Write: in.Write}}}
			if in.Write {
				var a coreexec.RecordWrite
				_ = json.Unmarshal(in.Arguments, &a)
				auth.Revision = a.Revision
				if auth.Revision == "" {
					auth.Revision = "new"
				}
				g.Identity = auth
				g.Revisions = map[string]string{in.Target: auth.Revision}
			}
			r.personaMu.Lock()
			r.personaGrants[auth.GrantID] = g
			r.personaMu.Unlock()
			defer func() { r.personaMu.Lock(); delete(r.personaGrants, auth.GrantID); r.personaMu.Unlock() }()
			result := turn.Tool(registry, spec, tools.Call{Auth: auth}, turnID).Execute(ctx, call)
			// A registry stale refusal carries no body; surface its closed error code
			// without weakening either the grant check or Commit's final CAS.
			if result.Code == loop.Denied && len(result.Output) == 0 && in.Write {
				env, err := r.recordEnvironment(ctx, g)
				if errors.Is(err, records.ErrRecordsChanged) {
					result.Output = coreexec.RecordFailure(err).Body
				} else if err == nil && env.Revision != auth.Revision {
					result.Output = json.RawMessage(`{"code":"stale_revision"}`)
				}
			}
			return result
		}}
	}
	return nil
}
