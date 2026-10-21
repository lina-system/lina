// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

// Keep the configured route's conservative byte budget while counting all
// assembled instructions, context, history and tool declarations.
func conversationRequestSize(request provider.Request) int {
	n := len(request.Instructions)
	for _, items := range [][]provider.Item{request.Items, request.Tools} {
		for _, item := range items {
			n += len(item)
		}
	}
	return n
}

func (r *conversationRuntime) resolvePersonaGrant(ctx context.Context, id string) (grant.Grant, grant.Environment, error) {
	r.personaMu.Lock()
	g, ok := r.personaGrants[id]
	r.personaMu.Unlock()
	if !ok {
		return grant.Grant{}, grant.Environment{}, grant.ErrGrant
	}
	a, err := r.agents.Get(ctx, g.Identity.Agent)
	if err != nil {
		return grant.Grant{}, grant.Environment{}, err
	}
	return g, grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true, Revision: strconv.FormatInt(a.Revision, 10)}, nil
}

func (r *conversationRuntime) preparePersonaTools(ctx context.Context, turnID string, input ledger.Event, persona agents.Preparation, bindings map[string]loop.Tool) ([]assemble.Skill, assemble.ContextPacket, error) {
	versions, err := r.skills.Catalog(ctx)
	if err != nil {
		return nil, nil, err
	}
	var catalog []assemble.Skill
	var packet assemble.ContextPacket
	for _, v := range versions {
		if v.ID != "persona-change" {
			continue
		}
		body, err := r.skills.Read(ctx, v)
		if err != nil {
			return nil, nil, err
		}
		catalog = append(catalog, assemble.Skill{ID: v.ID, Description: v.Description})
		packet = append(packet, assemble.ContextItem{ID: "skill:" + v.ID, Revision: v.Commit, Kind: assemble.SkillBody, Text: body.Files["SKILL.md"].Text})
	}
	state, _ := json.Marshal(struct {
		Kind     string `json:"kind"`
		Revision int64  `json:"revision"`
	}{"persona_revision", persona.Revision})
	packet = append(packet, assemble.ContextItem{ID: "persona:" + r.agent + ":revision", Revision: persona.Commit, AgentID: r.agent, Kind: assemble.Commitments, Text: string(state)})
	auth := grant.Request{GrantID: turnID, MainID: r.main, Epoch: r.epoch, TaskID: turnID, Host: r.main, OSUser: "core", Person: r.person, Agent: r.agent, Revision: strconv.FormatInt(persona.Revision, 10)}
	target := "persona:" + r.agent
	g := grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: "change_persona", Target: target, Write: true}, {Tool: "undo_persona", Target: target, Write: true}}, Revisions: map[string]string{target: auth.Revision}}
	r.personaMu.Lock()
	r.personaGrants[turnID] = g
	r.personaMu.Unlock()
	registry := tools.NewRegistry(r.personaAuthority, r.activities)
	scope := coreexec.PersonaScope{Agent: r.agent, Person: r.person, Conversation: r.conversation, Input: input.ID, Revision: persona.Revision}
	specs, err := coreexec.RegisterPersona(registry, r.persona, scope)
	if err != nil {
		return nil, nil, err
	}
	for _, spec := range specs {
		bindings[spec.Name] = turn.Tool(registry, spec, tools.Call{Auth: auth}, turnID)
	}
	return catalog, packet, nil
}

// The tool's success receipt has committed before After invokes this method.
// Core admits the notice directly, so it is independent of model wording and
// remains visible even if the follow-up provider request fails.
func (r *conversationRuntime) personaNotice(ctx context.Context, input ledger.Event, call provider.ToolCall, result loop.Result) loop.Result {
	if result.Code != loop.OK || call.Name != "change_persona" && call.Name != "undo_persona" {
		return result
	}
	var receipt coreexec.PersonaReceipt
	if json.Unmarshal(result.Output, &receipt) != nil || receipt.Notice == "" {
		return result
	}
	now := time.Now()
	event, err := r.journal().Admit(ctx, guard.EntrySystem, ledger.Input{ID: "persona-notice:" + input.ID + ":" + call.ID, ConversationID: r.conversation, ParentID: input.ID, AgentID: r.agent, Surface: input.Surface, ScaffoldKind: "persona_change", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: receipt.Notice})
	if err == nil {
		text := event.Body.Text()
		speaker := protocol.ConversationSpeakerSystem
		err = r.publish(protocol.KindItemDone, event.ID, protocol.ItemDone{Text: &text, Speaker: &speaker})
	}
	if err != nil {
		result.Code = loop.Unknown
	}
	return result
}
