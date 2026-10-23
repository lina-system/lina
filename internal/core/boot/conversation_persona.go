// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/reply"
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
	if g.Identity.Profile == "coding-manager" || g.Identity.Profile == "code-goal" {
		return g, grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true, Revision: g.Identity.Revision}, nil
	}
	if g.Identity.Profile == "methodology" {
		env := grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true}
		if g.Scope[0].Write {
			h, err := r.observeRecordsHealth(ctx)
			env.Revision = h.Revision
			return g, env, err
		}
		return g, env, nil
	}
	if g.Identity.Profile == "worker-session" {
		return g, grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true, Revision: strconv.FormatUint(r.epoch, 10)}, nil
	}
	if g.Identity.Profile == "records" {
		env, err := r.recordEnvironment(ctx, g)
		return g, env, err
	}
	if g.Identity.Profile == "web" {
		return g, grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true}, nil
	}
	if g.Identity.Profile == "skills" {
		env, err := r.skillEnvironment(ctx, g)
		return g, env, err
	}
	if g.Identity.Profile == "work" {
		revision, err := r.work.Revision(ctx, strings.TrimPrefix(g.Scope[0].Target, "work:"))
		return g, grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true, Revision: revision}, err
	}
	if g.Identity.Profile == "relationship" || g.Identity.Profile == "names" || g.Identity.Profile == "planning" {
		return g, grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true, Revision: g.Identity.Revision}, nil
	}
	if g.Identity.Profile == "persona-state" || g.Identity.Profile == "persona-expression" {
		state, err := r.agents.State(ctx, g.Identity.Agent)
		if err != nil {
			return grant.Grant{}, grant.Environment{}, err
		}
		revision := agents.StateRevision(state)
		if g.Identity.Profile == "persona-expression" {
			revision = agents.ExpressionRevision(state)
		}
		return g, grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true, Revision: revision}, nil
	}
	a, err := r.agents.Get(ctx, g.Identity.Agent)
	if err != nil {
		return grant.Grant{}, grant.Environment{}, err
	}
	return g, grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true, Revision: strconv.FormatInt(a.Revision, 10)}, nil
}

func (r *conversationRuntime) preparePersonaTools(ctx context.Context, turnID string, input ledger.Event, persona agents.Preparation, at time.Time, bindings map[string]loop.Tool, questions *questionTurn) ([]assemble.Skill, assemble.ContextPacket, error) {
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
	settings, err := r.agents.State(ctx, r.agent)
	if err != nil {
		return nil, nil, err
	}
	scope.StateRevision = agents.StateRevision(settings)
	questionPreference, err := r.prepareQuestionTool(ctx, turnID, input, agents.ProjectWorkMethod(persona, settings), bindings, questions)
	if err != nil {
		return nil, nil, err
	}
	packet = append(packet, questionPreference)
	scope.ExpressionRevision = agents.ExpressionRevision(settings)
	stateAuth := auth
	stateAuth.GrantID = turnID + ":state"
	stateAuth.Profile = "persona-state"
	stateAuth.Revision = scope.StateRevision
	stateTarget := "persona-state:" + r.agent
	stateGrant := grant.Grant{Identity: stateAuth, Expires: g.Expires, Scope: []grant.Permission{{Tool: "fill_persona_setting", Target: stateTarget, Write: true}, {Tool: "fill_work_method", Target: stateTarget, Write: true}, {Tool: "record_emotion", Target: stateTarget, Write: true}}, Revisions: map[string]string{stateTarget: scope.StateRevision}}
	expressionAuth := auth
	expressionAuth.GrantID = turnID + ":expression"
	expressionAuth.Profile = "persona-expression"
	expressionAuth.Revision = scope.ExpressionRevision
	expressionTarget := "persona-expression:" + r.agent
	expressionGrant := grant.Grant{Identity: expressionAuth, Expires: g.Expires, Scope: []grant.Permission{{Tool: "set_emotion_expression", Target: expressionTarget, Write: true}}, Revisions: map[string]string{expressionTarget: scope.ExpressionRevision}}
	r.personaMu.Lock()
	r.personaGrants[stateAuth.GrantID] = stateGrant
	r.personaGrants[expressionAuth.GrantID] = expressionGrant
	r.personaMu.Unlock()

	settingSpec, err := coreexec.RegisterPersonaSettings(registry, r.persona, scope)
	if err != nil {
		return nil, nil, err
	}
	bindings[settingSpec.Name] = turn.Tool(registry, settingSpec, tools.Call{Auth: stateAuth}, turnID)
	methodSpec, err := coreexec.RegisterWorkMethod(registry, r.persona, scope)
	if err != nil {
		return nil, nil, err
	}
	bindings[methodSpec.Name] = turn.Tool(registry, methodSpec, tools.Call{Auth: stateAuth}, turnID)
	emotionSpecs, err := coreexec.RegisterEmotion(registry, r.persona, scope)
	if err != nil {
		return nil, nil, err
	}
	for _, spec := range emotionSpecs {
		binding := stateAuth
		if spec.Name == "set_emotion_expression" {
			binding = expressionAuth
		}
		bindings[spec.Name] = turn.Tool(registry, spec, tools.Call{Auth: binding}, turnID)
	}
	emotion, err := agents.ProjectEmotion(settings, r.conversation, persona.Revision, at, agents.DefaultEmotionPolicy(), coreexec.SensitivePersonaInput(input))
	if err != nil {
		return nil, nil, err
	}
	shape, err := reply.ShapeEmotion(emotion, reply.ChatBudget())
	if err != nil {
		return nil, nil, err
	}
	shapeData, _ := json.Marshal(shape)
	packet = append(packet, assemble.ContextItem{ID: "persona-expression:" + r.agent, Revision: scope.ExpressionRevision + ":" + scope.StateRevision, AgentID: r.agent, Kind: assemble.Commitments, Text: string(shapeData)})
	emotionData, _ := json.Marshal(struct {
		Kind  string                 `json:"kind"`
		State agents.EmotionSnapshot `json:"state"`
	}{"persona_emotion", emotion})
	packet = append(packet, assemble.ContextItem{ID: "persona-emotion:" + r.agent + ":" + r.conversation, Revision: scope.StateRevision, AgentID: r.agent, Kind: assemble.Clock, Text: string(emotionData)})
	settingData, _ := json.Marshal(struct {
		Kind       string                    `json:"kind"`
		Values     map[string]string         `json:"values"`
		WorkMethod agents.WorkMethodSettings `json:"work_method"`
	}{"persona_settings", settings.Settings, agents.ProjectWorkMethod(persona, settings)})
	var sourceIDs []string
	for _, id := range settings.SettingSources {
		sourceIDs = append(sourceIDs, id)
	}
	for _, fill := range settings.WorkMethodFills {
		sourceIDs = append(sourceIDs, fill.Source)
	}
	slices.Sort(sourceIDs)
	var refs []assemble.Ref
	for _, id := range slices.Compact(sourceIDs) {
		refs = append(refs, assemble.Ref{ID: id})
	}
	packet = append(packet, assemble.ContextItem{ID: "persona-settings:" + r.agent, Revision: scope.StateRevision, AgentID: r.agent, Kind: assemble.Commitments, Origin: "persona-record:" + r.agent, Text: string(settingData), Evidence: refs})
	relationshipAuth := auth
	relationshipAuth.GrantID = turnID + ":relationship"
	relationshipAuth.Profile = "relationship"
	relationshipAuth.Revision = "1"
	relationshipTarget := "relationship:" + r.agent
	relationshipGrant := grant.Grant{Identity: relationshipAuth, Expires: g.Expires, Scope: []grant.Permission{{Tool: "record_relationship", Target: relationshipTarget, Write: true}}, Revisions: map[string]string{relationshipTarget: "1"}}
	r.personaMu.Lock()
	r.personaGrants[relationshipAuth.GrantID] = relationshipGrant
	r.personaMu.Unlock()
	relationshipSpec, err := coreexec.RegisterRelationship(registry, r.persona, scope)
	if err != nil {
		return nil, nil, err
	}
	bindings[relationshipSpec.Name] = turn.Tool(registry, relationshipSpec, tools.Call{Auth: relationshipAuth}, turnID)
	if r.channelBinding == nil || r.channelBinding.Scope.Memory {
		nameAuth := auth
		nameAuth.GrantID, nameAuth.Profile, nameAuth.Revision = turnID+":names", "names", "1"
		nameTarget := "names:" + r.person
		nameGrant := grant.Grant{Identity: nameAuth, Expires: g.Expires, Scope: []grant.Permission{{Tool: "remember_name", Target: nameTarget, Write: true}}, Revisions: map[string]string{nameTarget: "1"}}
		r.personaMu.Lock()
		r.personaGrants[nameAuth.GrantID] = nameGrant
		r.personaMu.Unlock()
		spec, err := coreexec.RegisterName(registry, r.persona, scope)
		if err != nil {
			return nil, nil, err
		}
		bindings[spec.Name] = turn.Tool(registry, spec, tools.Call{Auth: nameAuth}, turnID)
	}
	return catalog, packet, nil
}

// The tool's success receipt has committed before After invokes this method.
// Core admits the notice directly, so it is independent of model wording and
// remains visible even if the follow-up provider request fails.
func (r *conversationRuntime) personaNotice(ctx context.Context, input ledger.Event, call provider.ToolCall, result loop.Result) loop.Result {
	if result.Code != loop.OK || call.Name != "change_persona" && call.Name != "undo_persona" && call.Name != "set_emotion_expression" {
		return result
	}
	var receipt struct {
		Notice string `json:"notice"`
	}
	if json.Unmarshal(result.Output, &receipt) != nil || receipt.Notice == "" {
		return result
	}
	now := time.Now()
	event, err := r.journal().Admit(ctx, guard.EntrySystem, ledger.Input{ID: "persona-notice:" + input.ID + ":" + call.ID, ConversationID: r.conversation, ParentID: input.ID, AgentID: r.agent, Surface: input.Surface, ScaffoldKind: "persona_change", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: receipt.Notice})
	if err == nil {
		text := event.Body.Text()
		speaker := protocol.ConversationSpeakerSystem
		err = r.publish(ctx, protocol.KindItemDone, event.ID, protocol.ItemDone{Text: &text, Speaker: &speaker})
	}
	if err != nil {
		result.Code = loop.Unknown
	}
	return result
}
