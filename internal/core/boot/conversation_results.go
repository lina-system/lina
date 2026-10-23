// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/canon"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/results"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/result"
	"github.com/thisisjun786/lina/kit/tools"
)

func (r *conversationRuntime) resultStore(surface ledger.Surface) (*results.Store, error) {
	return results.New(r.db, results.Config{Journal: results.LedgerJournal{Surface: surface}, Writer: results.NewRecordsWriter(r.records, r.assets, nil)})
}

func (r *conversationRuntime) prepareResultTools(turnID string, input ledger.Event, bindings map[string]loop.Tool) error {
	store, err := r.resultStore(input.Surface)
	if err != nil {
		return err
	}
	scope := result.Scope{PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, TurnID: turnID}
	registry := tools.NewRegistry(r.personaAuthority, r.activities)

	bound, err := coreexec.ResultTools(registry, store, scope, func(ctx context.Context, name string, in tools.Input) (tools.Call, error) {
		grantID := fmt.Sprintf("%s:results:%s:%x", turnID, name, sha256.Sum256(in.Arguments))
		auth := grant.Request{GrantID: grantID, MainID: r.main, Epoch: r.epoch, TaskID: turnID, Host: r.main, OSUser: "core", Person: r.person, Agent: r.agent, Profile: "results"}
		if name == "present_result" {
			var p result.PresentRequest
			_ = json.Unmarshal(in.Arguments, &p)
			auth.Revision = strconv.Itoa(p.BaseRevision)
		}
		g := grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: name, Target: in.Target, Write: in.Write}}}
		if in.Write {
			g.Revisions = map[string]string{in.Target: auth.Revision}
		}
		r.personaMu.Lock()
		r.personaGrants[grantID] = g
		r.personaMu.Unlock()
		return tools.Call{Auth: auth}, nil
	}, nil)
	if err != nil {
		return err
	}
	for _, name := range []string{"present_result", "read_result"} {
		tool := bound[name]
		execute := tool.Execute
		tool.Execute = func(ctx context.Context, call provider.ToolCall) loop.Result {
			out := execute(ctx, call)
			if name == "present_result" && out.Code == loop.OK {
				var card result.Card
				if json.Unmarshal(out.Output, &card) == nil {
					value, err := store.Read(ctx, scope, result.ReadRequest{ID: card.ID})
					if err == nil {
						// The committed ledger is also the history source if publication
						// is interrupted; a delivery error cannot undo the completion.
						_ = r.publishResult(ctx, value)
					}
				}
			}
			return out
		}
		bindings[name] = tool
	}
	return nil
}

func (r *conversationRuntime) resultEnvironment(ctx context.Context, g grant.Grant) (grant.Environment, error) {
	env := grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true, Revision: "0"}
	if !g.Scope[0].Write || strings.HasPrefix(g.Scope[0].Target, "result:new:") {
		return env, nil
	}
	var revision int
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT revision FROM result_heads WHERE id=? AND person=? AND agent=? AND conversation=(SELECT conversation FROM turn_runs WHERE id=?)`, strings.TrimPrefix(g.Scope[0].Target, "result:"), g.Identity.Person, g.Identity.Agent, g.Identity.TaskID).Scan(&revision)
	})
	env.Revision = strconv.Itoa(revision)
	return env, err
}

func (r *conversationRuntime) publishResult(ctx context.Context, value result.Result) error {
	text := value.Text
	if len(text) > conversationPreviewBytes {
		end := conversationPreviewBytes
		for !utf8.RuneStart(text[end]) {
			end--
		}
		text = text[:end] + "\n…"
	}
	p := protocol.Result{ResultID: value.ID, Revision: json.Number(strconv.Itoa(value.Revision)), Type: protocol.ResultType(value.Type), Title: value.Title, Summary: value.Summary, Text: &text, Status: protocol.ResultStatusPreview, RevisedBy: value.RevisedBy, Provenance: protocol.ResultProvenance{AgentID: value.Provenance.AgentID, ConversationID: value.Provenance.ConversationID, TurnID: value.Provenance.TurnID}}
	if value.Completion != nil {
		p.Completion = &protocol.CompletionStatus{State: protocol.CompletionState(value.Completion.State), Label: value.Completion.Label}
	}
	return r.publish(ctx, protocol.KindResult, "result-card:"+value.ID, p)
}
