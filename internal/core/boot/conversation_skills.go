// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/thisisjun786/lina/internal/design"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

func (r *conversationRuntime) skillEnvironment(ctx context.Context, g grant.Grant) (grant.Environment, error) {
	env := grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true}
	if !g.Scope[0].Write {
		return env, nil // Immutable reads do not require the current head.
	}
	var revision int64
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT revision FROM skill_heads WHERE id=?", strings.TrimPrefix(g.Scope[0].Target, "skill:")).Scan(&revision)
	})
	// New and built-in skills start at zero.
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	env.Revision = strconv.FormatInt(revision, 10)
	return env, err
}

func (r *conversationRuntime) prepareSkillTools(turnID string, bindings map[string]loop.Tool) error {
	registry := tools.NewRegistry(r.personaAuthority, r.activities)
	specs, err := coreexec.RegisterSkills(registry, r.skills)
	if err != nil {
		return err
	}
	for _, spec := range specs {
		validated := turn.Tool(registry, spec, tools.Call{}, turnID)
		bindings[spec.Name] = loop.Tool{
			Validate: validated.Validate,
			Execute: func(ctx context.Context, call provider.ToolCall) loop.Result {
				if _, err := validated.Validate(call); err != nil {
					return loop.Result{ID: call.ID, Code: loop.Invalid}
				}
				in, err := spec.Schema(json.RawMessage(call.Arguments))
				if err != nil {
					return loop.Result{ID: call.ID, Code: loop.Invalid}
				}
				// Core's instruction-only skill policy permits these four operations
				// in the authenticated turn. Mint only the schema-normalized target,
				// never arbitrary model-supplied identity, scope or permissions.
				auth := grant.Request{GrantID: turn.EffectKey(turnID, spec.Name, in.Target, call.ID) + ":skills", MainID: r.main, Epoch: r.epoch, TaskID: turnID, Host: r.main, OSUser: "core", Person: r.person, Agent: r.agent, Profile: "skills"}
				g := grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: spec.Name, Target: in.Target, Write: in.Write}}}
				if in.Write {
					var version struct{ Expected, Revision int64 }
					if err := json.Unmarshal(in.Arguments, &version); err != nil {
						return loop.Result{ID: call.ID, Code: loop.Invalid}
					}
					revision := version.Expected
					if spec.Name == "undo_skill" {
						revision = version.Revision
					}
					auth.Revision = strconv.FormatInt(revision, 10)
					g.Identity = auth
					g.Revisions = map[string]string{in.Target: auth.Revision}
				}
				r.personaMu.Lock()
				r.personaGrants[auth.GrantID] = g
				r.personaMu.Unlock()
				defer func() { r.personaMu.Lock(); delete(r.personaGrants, auth.GrantID); r.personaMu.Unlock() }()
				return turn.Tool(registry, spec, tools.Call{Auth: auth}, turnID).Execute(ctx, call)
			},
		}
	}
	return nil
}

// Only the normal registry's committed success receipt can produce a notice.
// Present it directly so a failed provider follow-up cannot hide adoption.
func (r *conversationRuntime) skillNotice(ctx context.Context, input ledger.Event, call provider.ToolCall, result loop.Result) loop.Result {
	if result.Code != loop.OK || call.Name != "write_skill" && call.Name != "undo_skill" {
		return result
	}
	var receipt skills.Applied
	if json.Unmarshal(result.Output, &receipt) != nil || receipt.Notice == "" {
		return result
	}
	code := "notice.skill_changed"
	if call.Name == "undo_skill" {
		code = "notice.skill_undone"
	}
	receipt.Notice = fmt.Sprintf(design.Text(eventLanguage(input), code), receipt.Version.Name)
	now := time.Now()
	event, err := r.journal().Admit(ctx, guard.EntrySystem, ledger.Input{ID: "skill-notice:" + input.ID + ":" + call.ID, ConversationID: r.conversation, ParentID: input.ID, AgentID: r.agent, Surface: input.Surface, ScaffoldKind: "skill_change", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: receipt.Notice})
	if err == nil {
		text, speaker := event.Body.Text(), protocol.ConversationSpeakerSystem
		err = r.publish(ctx, protocol.KindItemDone, event.ID, protocol.ItemDone{Text: &text, Speaker: &speaker})
	}
	if err != nil {
		result.Code = loop.Unknown
	}
	return result
}
