// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type workMaterials struct {
	writer *assets.MaterialWriter
	store  *assets.Store
	person string
}
type workMaterial struct {
	*assets.PreparedMaterial
	path string
}

func (m workMaterials) Open(ctx context.Context, material work.Material) (io.ReadCloser, error) {
	return m.store.OpenRead(ctx, material.ID, material.Revision)
}
func (m workMaterials) Prepare(ctx context.Context, r io.Reader, path, agent string) (work.MaterialWrite, error) {
	p, err := m.writer.Prepare(ctx, r, "work-result:"+path, assets.Actor{PersonID: m.person, AgentID: agent})
	if err != nil {
		return nil, err
	}
	return workMaterial{p, path}, nil
}
func (m workMaterial) Insert(ctx context.Context, tx *canon.Tx) (work.Material, error) {
	a, err := m.PreparedMaterial.Insert(ctx, tx)
	return work.Material{ID: a.ID, Revision: a.Revision, Path: m.path, SHA256: a.Hash}, err
}

func (r *conversationRuntime) recoverWorkAuthority(ctx context.Context) error {
	return r.personaAuthority.CancelInterrupted(ctx, func(ctx context.Context, tx *canon.Tx, e authority.Effect) (tools.Result, bool, error) {
		if e.Tool != "land_work_result" || e.Plugin != "" || e.Account != "" || e.Outward || !e.Write || e.Person != r.person || !strings.HasPrefix(e.Target, "work:") {
			return tools.Result{}, false, nil
		}
		c, revision, err := work.RecoveredFileResult(ctx, tx, strings.TrimPrefix(e.Target, "work:"))
		if errors.Is(err, work.ErrNotFound) || errors.Is(err, work.ErrInvalid) {
			return tools.Result{}, false, nil
		}
		if err != nil {
			return tools.Result{}, false, err
		}
		if c.Task.AgentID != e.Agent || (c.Task.PersonID != "" && c.Task.PersonID != e.Person) {
			return tools.Result{}, false, nil
		}
		body, err := json.Marshal(map[string]string{
			"error":   "file landing call interrupted; inspect the recovered work result before retrying",
			"task_id": c.Task.ID, "next_action": "retry_land_work_result",
		})
		return tools.Result{State: tools.Cancelled, Body: body, Revision: revision}, true, err
	})
}
func (r *conversationRuntime) prepareWorkTools(turnID string, bindings map[string]loop.Tool) error {
	registry := tools.NewRegistry(r.personaAuthority, r.activities)
	specs, err := coreexec.RegisterWork(registry, r.work, r.agent, r.conversation)
	if err != nil {
		return err
	}
	for _, spec := range specs {
		validated := turn.Tool(registry, spec, tools.Call{}, turnID)
		bindings[spec.Name] = loop.Tool{Validate: validated.Validate, Execute: func(ctx context.Context, call provider.ToolCall) loop.Result {
			in, err := spec.Schema(json.RawMessage(call.Arguments))
			if err != nil {
				return loop.Result{ID: call.ID, Code: loop.Invalid}
			}
			owner, conversation, err := r.workOwner(ctx, turnID, spec.Name, in)
			if err != nil {
				return loop.Result{ID: call.ID, Code: loop.Denied, Output: workerError(err)}
			}
			selected := registry
			if owner != r.agent || conversation != r.conversation {
				selected = tools.NewRegistry(r.personaAuthority, r.activities)
				if _, err = coreexec.RegisterWork(selected, r.work, owner, conversation); err != nil {
					return loop.Result{ID: call.ID, Code: loop.Failed}
				}
			}
			revision, err := r.work.Revision(ctx, strings.TrimPrefix(in.Target, "work:"))
			if err != nil {
				return loop.Result{ID: call.ID, Code: loop.Failed}
			}
			key := turn.EffectKey(turnID, spec.Name, in.Target, call.ID)
			// Reusing the original basis preserves the immutable effect fingerprint.
			// The gate still checks actor/arguments and the current store revision.
			if prior, e := r.personaAuthority.Effect(ctx, key); e == nil {
				revision = prior.Revision
			}
			auth := grant.Request{GrantID: turn.EffectKey(turnID, spec.Name, in.Target, call.ID) + ":work", MainID: r.main, Epoch: r.epoch, TaskID: turnID, Host: r.main, OSUser: "core", Person: r.person, Agent: r.agent, Profile: "work", Revision: revision}
			g := grant.Grant{Revisions: map[string]string{in.Target: revision}, Identity: auth, Expires: time.Now().Add(time.Minute), Scope: []grant.Permission{{Tool: spec.Name, Target: in.Target, Write: true}}}
			r.personaMu.Lock()
			r.personaGrants[auth.GrantID] = g
			r.personaMu.Unlock()
			defer func() { r.personaMu.Lock(); delete(r.personaGrants, auth.GrantID); r.personaMu.Unlock() }()
			return turn.Tool(selected, spec, tools.Call{Auth: auth}, turnID).Execute(ctx, call)
		}}
	}
	return nil
}

func (r *conversationRuntime) workOwner(ctx context.Context, turnID, name string, in tools.Input) (string, string, error) {
	if name == "prepare_work" {
		var request coreexec.PrepareWork
		if err := json.Unmarshal(in.Arguments, &request); err != nil {
			return "", "", err
		}
		if request.Workspace.RepositoryID == "" {
			return r.agent, r.conversation, nil
		}
		if !filepath.IsAbs(request.Workspace.Source) {
			return "", "", work.ErrInvalid
		}
		repository, err := filepath.EvalSymlinks(request.Workspace.Source)
		if err != nil {
			return "", "", err
		}
		a, err := r.assignCoding(ctx, dispatch.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: turnID}, request.TaskID, name, repository, in.Arguments)
		return a.Owner, a.Conversation, err
	}
	id := strings.TrimPrefix(in.Target, "work:")
	var a codingAssignment
	err := r.db.Read(ctx, func(tx *canon.Tx) error { var err error; a, err = readCoding(ctx, tx, id); return err })
	if errors.Is(err, sql.ErrNoRows) {
		return r.agent, r.conversation, nil
	}
	if err != nil {
		return "", "", err
	}
	if a.Origin.Person != r.person || a.Origin.Agent != r.agent || a.Origin.Conversation != r.conversation {
		return "", "", work.ErrInvalid
	}
	return a.Owner, a.Conversation, nil
}
