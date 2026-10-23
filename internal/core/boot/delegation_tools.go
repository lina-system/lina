// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type dispatchDefinition struct {
	Key       string   `json:"key"`
	What      string   `json:"what"`
	Why       string   `json:"why"`
	Done      string   `json:"done"`
	Areas     []string `json:"areas"`
	DependsOn []string `json:"depends_on"`
	Design    []string `json:"design"`
}
type dispatchWork struct {
	Repository string               `json:"repository"`
	Tasks      []dispatchDefinition `json:"tasks"`
}
type refreshWork struct {
	Tasks []string `json:"tasks"`
}

func decodeDelegation(raw string, out any) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}
func (r *conversationRuntime) prepareDelegationTools(turnID string, bindings map[string]loop.Tool) []provider.ToolDecl {
	if r.delegation == nil {
		return nil
	}
	bindings["schedule_work"] = loop.Tool{Validate: func(c provider.ToolCall) (string, error) {
		var in dispatchWork
		if err := decodeDelegation(c.Arguments, &in); err != nil {
			return "", err
		}
		if _, ok := r.delegation.config.Repositories[in.Repository]; !ok || len(in.Tasks) == 0 || len(in.Tasks) > 256 {
			return "", work.ErrInvalid
		}
		return in.Repository, nil
	}, Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
		var in dispatchWork
		if err := decodeDelegation(c.Arguments, &in); err != nil {
			return loop.Result{ID: c.ID, Code: loop.Invalid}
		}
		if err := authority.CheckPreset(ctx, r.db, r.epoch, authority.PolicyRequest{
			Key: "preset:schedule:" + delegatedID(turnID, c.ID, "policy"), Person: r.person, Agent: r.agent, TaskID: turnID,
			Tool: "schedule_work", Target: in.Repository, Class: tools.WorkerEffect, Arguments: json.RawMessage(c.Arguments),
		}); err != nil {
			return delegationResult(c.ID, nil, err)
		}
		tasks, err := r.delegation.tasks(ctx, r, turnID, c.ID, in)
		if err != nil {
			return delegationResult(c.ID, nil, err)
		}
		if err = work.ValidateSchedule(tasks); err != nil {
			return delegationResult(c.ID, nil, err)
		}
		// Keep every task non-runnable until all manager judgments are durable.
		origin := dispatch.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: turnID}
		for i := range tasks {
			definition, err := json.Marshal(map[string]any{"repository": in.Repository, "definition": in.Tasks[i]})
			if err != nil {
				return delegationResult(c.ID, nil, err)
			}
			a, err := r.assignCoding(ctx, origin, tasks[i].ID, "schedule_work", in.Repository, definition)
			if err != nil {
				return delegationResult(c.ID, nil, err)
			}
			if err = r.runCodingManager(ctx, a, "assignment", nil); err != nil {
				return delegationResult(c.ID, nil, err)
			}
			tasks[i].AgentID, tasks[i].ConversationID = a.Owner, a.Conversation
		}
		for _, task := range tasks {
			if err := r.personaAuthority.BindDelegation(ctx, r.person, r.agent, turnID, task.AgentID, task.ID); err != nil {
				return delegationResult(c.ID, nil, err)
			}
		}
		err = r.delegation.reactor.Schedule(ctx, tasks)
		var ids []string
		for _, t := range tasks {
			ids = append(ids, t.ID)
		}
		return delegationResult(c.ID, ids, err)
	}}
	bindings["refresh_work"] = loop.Tool{Validate: func(c provider.ToolCall) (string, error) {
		var in refreshWork
		err := decodeDelegation(c.Arguments, &in)
		if len(in.Tasks) == 0 || len(in.Tasks) > 256 {
			return "", work.ErrInvalid
		}
		return "work", err
	}, Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
		var in refreshWork
		if err := decodeDelegation(c.Arguments, &in); err != nil {
			return loop.Result{ID: c.ID, Code: loop.Invalid}
		}
		for _, id := range in.Tasks {
			card, err := r.work.Get(ctx, id)
			if err != nil || !r.scheduledDeliveryMatches(ctx, card.Task) {
				return loop.Result{ID: c.ID, Code: loop.Denied}
			}
		}
		var result error
		for _, id := range in.Tasks {
			result = errors.Join(result, r.delegation.reactor.Observe(ctx, id))
		}
		var cards []work.Card
		for _, id := range in.Tasks {
			card, err := r.work.Get(ctx, id)
			if err != nil {
				return delegationResult(c.ID, nil, err)
			}
			cards = append(cards, card)
		}
		return delegationResult(c.ID, cards, result)
	}}
	return []provider.ToolDecl{
		json.RawMessage(`{"type":"function","name":"schedule_work","description":"Delegate a dependency graph to the installed coding worker. Use a configured repository, one checkable done condition and repository-relative write globs per task. Dependencies name keys in this graph. Design references are file#section at the repository base. Core creates each branch/worktree and starts all eligible tasks. Returns durable task ids; finishing a worker is not verified completion.","parameters":{"type":"object","properties":{"repository":{"type":"string"},"tasks":{"type":"array","items":{"type":"object","properties":{"key":{"type":"string"},"what":{"type":"string"},"why":{"type":"string"},"done":{"type":"string"},"areas":{"type":"array","items":{"type":"string"}},"depends_on":{"type":"array","items":{"type":"string"}},"design":{"type":"array","items":{"type":"string"}}},"required":["key","what","why","done","areas","depends_on","design"],"additionalProperties":false}}},"required":["repository","tasks"],"additionalProperties":false}}`),
		json.RawMessage(`{"type":"function","name":"refresh_work","description":"Read current task cards and observe their pull requests through Core. A confirmed merge releases dependent tasks. This never merges or declares a worker result verified.","parameters":{"type":"object","properties":{"tasks":{"type":"array","items":{"type":"string"}}},"required":["tasks"],"additionalProperties":false}}`),
	}
}
func delegationResult(id string, value any, err error) loop.Result {
	code := loop.OK
	body := map[string]any{"result": value}
	if err != nil {
		code = loop.Failed
		body["code"] = "work_unavailable"
		if errors.Is(err, work.ErrInvalid) || errors.Is(err, work.ErrState) {
			code = loop.Invalid
			body["code"] = "invalid_work_graph"
		}
		if errors.Is(err, authority.ErrPresetLimit) {
			code = loop.Denied
			body["code"] = authority.ErrPresetLimit.Error()
		}
		if errors.Is(err, errDevUnavailable) {
			body["code"] = errDevUnavailable.Error()
		}
	}
	raw, _ := json.Marshal(body)
	return loop.Result{ID: id, Code: code, Output: raw}
}
func (d *delegationRuntime) tasks(ctx context.Context, r *conversationRuntime, turn, call string, in dispatchWork) ([]work.Task, error) {
	repo, ok := d.config.Repositories[in.Repository]
	if !ok || len(in.Tasks) == 0 || len(in.Tasks) > 256 {
		return nil, work.ErrInvalid
	}
	keys := map[string]string{}
	for _, t := range in.Tasks {
		if t.Key == "" || len(t.Key) > 128 || keys[t.Key] != "" {
			return nil, work.ErrInvalid
		}
		keys[t.Key] = delegatedID(turn, call, t.Key)
	}
	var tasks []work.Task
	for _, definition := range in.Tasks {
		if strings.TrimSpace(definition.What) == "" || strings.TrimSpace(definition.Why) == "" || strings.TrimSpace(definition.Done) == "" || len(definition.Design) == 0 {
			return nil, work.ErrInvalid
		}
		for _, ref := range definition.Design {
			file, anchor, ok := strings.Cut(ref, "#")
			if !ok || anchor == "" || filepath.IsAbs(file) || filepath.Clean(file) != file || strings.HasPrefix(file, "../") || strings.HasPrefix(file, "-") {
				return nil, work.ErrInvalid
			}
			raw, err := d.command(ctx, d.config.Git, repo.Clone, "show", repo.Base+":"+file)
			if err != nil {
				return nil, work.ErrInvalid
			}
			found := false
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(line, "#") {
					heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
					slug := strings.ToLower(strings.ReplaceAll(heading, " ", "-"))
					if slug == anchor {
						found = true
						break
					}
				}
			}
			if !found {
				return nil, work.ErrInvalid
			}
		}
		id := keys[definition.Key]
		var deps []string
		for _, dep := range definition.DependsOn {
			if keys[dep] == "" {
				return nil, work.ErrInvalid
			}
			deps = append(deps, keys[dep])
		}
		area, err := work.ScheduledAreasOverlap(definition.Areas, definition.Areas)
		if err != nil || !area {
			return nil, work.ErrInvalid
		}
		raw, _ := json.MarshalIndent(definition, "", "  ")
		var packet bytes.Buffer
		fmt.Fprintf(&packet, "Complete this delegated work in the current worktree and branch. Do not merge or write the base branch. Deliver your changes in one pull request into %s and report its result. Keep to the declared write areas.\nWork definition:\n%s\n", repo.Base, raw)
		dir := filepath.Join(d.root.root, "work", "tasks", id)
		tasks = append(tasks, work.Task{ID: id, PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: definition.What, Done: definition.Done, ArtifactRoots: []string{dir}, RequiresApply: true, Schedule: &work.Schedule{InputID: strings.TrimPrefix(turn, "turn:"), Group: delegatedID(turn, call, "graph"), Repository: in.Repository, Areas: definition.Areas, DependsOn: deps, Worker: "codex", Host: "local", Workdir: dir, Packet: packet.String()}})
	}
	return tasks, nil
}
