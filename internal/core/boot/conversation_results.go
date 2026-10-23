// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/reply"
	"github.com/thisisjun786/lina/internal/core/results"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/result"
	"github.com/thisisjun786/lina/kit/tools"
)

// The source, result head and its measurement commit in the same transaction.
// The ledger reader checks native identity; model arguments never carry it.
type measuredResultJournal struct {
	results.LedgerJournal
	config reply.Config
}
type resultTransaction struct{ tx *canon.Tx }

func (s resultTransaction) Read(_ context.Context, f func(*canon.Tx) error) error  { return f(s.tx) }
func (s resultTransaction) Write(_ context.Context, f func(*canon.Tx) error) error { return f(s.tx) }
func (j measuredResultJournal) Append(ctx context.Context, tx *canon.Tx, scope result.Scope, value result.Result) (string, error) {
	id, err := j.LedgerJournal.Append(ctx, tx, scope, value)
	if err != nil {
		return "", err
	}
	view, err := reply.ProjectResult(j.config, value)
	if err != nil {
		return "", err
	}
	journal := ledger.New(resultTransaction{tx}, nil)
	source, err := journal.Get(ctx, id)
	if err != nil {
		return "", err
	}
	_, err = journal.AdmitBatch(ctx, []ledger.Admission{view.Check.Admission(ledger.Input{
		ID: id, ConversationID: source.ConversationID, AgentID: source.AgentID, Surface: source.Surface,
		OccurredAt: source.OccurredAt, RecordedAt: source.RecordedAt, TimeQuality: source.TimeQuality,
	})})
	return id, err
}

func (r *conversationRuntime) resultEnvironment(ctx context.Context, g grant.Grant) (grant.Environment, error) {
	env := grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true, Revision: "0"}
	var conversation string
	if err := r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT conversation FROM turn_runs WHERE id=?", g.Identity.TaskID).Scan(&conversation)
	}); err != nil {
		return env, err
	}
	target := g.Scope[0].Target
	if target == "result:new:"+conversation {
		return env, nil
	}
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT revision FROM result_heads WHERE id=? AND person=? AND agent=? AND conversation=?`, strings.TrimPrefix(target, "result:"), g.Identity.Person, g.Identity.Agent, conversation).Scan(&env.Revision)
	})
	return env, err
}
func (r *conversationRuntime) prepareResultTools(turnID string, preparation *toolPreparation, bindings map[string]loop.Tool) ([]provider.ToolDecl, error) {
	input, shapes := preparation.event, preparation.replyShapes
	journal := measuredResultJournal{LedgerJournal: results.LedgerJournal{Surface: input.Surface, ParentID: input.ID}, config: conversationReplyConfig(&shapes)}
	store, err := results.New(r.db, results.Config{Journal: journal})
	if err != nil {
		return nil, err
	}
	scope := result.Scope{PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, TurnID: turnID}
	bound, err := coreexec.ResultTools(tools.NewRegistry(r.personaAuthority, r.activities), store, scope, func(ctx context.Context, name string, in tools.Input) (tools.Call, error) {
		auth := grant.Request{GrantID: turnID + ":result:" + rand.Text(), MainID: r.main, Epoch: r.epoch, TaskID: turnID, Host: r.main, OSUser: "core", Person: r.person, Agent: r.agent, Profile: "results"}
		g := grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: name, Target: in.Target, Write: in.Write}}}
		if in.Write {
			var p result.PresentRequest
			if err := json.Unmarshal(in.Arguments, &p); err != nil {
				return tools.Call{}, err
			}
			auth.Revision = strconv.Itoa(p.BaseRevision)
			g.Identity = auth
			g.Revisions = map[string]string{in.Target: auth.Revision}
		}
		r.personaMu.Lock()
		r.personaGrants[auth.GrantID] = g
		r.personaMu.Unlock()
		return tools.Call{Auth: auth}, nil
	}, nil)
	if err != nil {
		return nil, err
	}
	bindings["present_result"], bindings["read_result"], bindings["present_surface"] = bound["present_result"], bound["read_result"], bound["present_surface"]
	return coreexec.ResultDeclarations(), nil
}

func (r *conversationRuntime) resultNotice(ctx context.Context, call provider.ToolCall, out loop.Result) loop.Result {
	if (call.Name != "present_result" && call.Name != "present_surface") || out.Code != loop.OK {
		return out
	}
	var card result.Card
	if err := json.Unmarshal(out.Output, &card); err != nil {
		out.Code = loop.Unknown
		return out
	}
	var id string
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT event_id FROM result_heads WHERE id=? AND person=? AND agent=? AND conversation=? AND revision=?`, card.ID, r.person, r.agent, r.conversation, card.Revision).Scan(&id)
	})
	if err == nil {
		var source ledger.Event
		source, err = r.journal().Get(ctx, id)
		if err == nil {
			err = r.publishTypedResult(ctx, source)
		}
	}
	if err != nil {
		out.Code = loop.Unknown
	}
	return out
}

func (r *conversationRuntime) typedResultView(ctx context.Context, source ledger.Event) (result.Result, reply.ResultView, error) {
	var value result.Result
	if source.AgentID != r.agent || source.ConversationID != r.conversation {
		return value, reply.ResultView{}, results.ErrOutOfScope
	}
	if err := json.Unmarshal([]byte(source.Body.Text()), &value); err != nil {
		return value, reply.ResultView{}, err
	}
	cfg := conversationReplyConfig(nil)
	measurement, err := r.journal().Get(ctx, "reply-check:"+source.ID)
	if err == nil {
		var check reply.Check
		if err := json.Unmarshal([]byte(measurement.Body.Text()), &check); err != nil {
			return value, reply.ResultView{}, err
		}
		cfg.Shapes, cfg.Budget = check.Shapes, check.Budget
	} else if !errors.Is(err, ledger.ErrNotFound) {
		return value, reply.ResultView{}, err
	}
	view, err := reply.ProjectResult(cfg, value)
	return value, view, err
}

func (r *conversationRuntime) publishTypedResult(ctx context.Context, source ledger.Event) error {
	value, view, err := r.typedResultView(ctx, source)
	if err != nil {
		return err
	}
	key := "result-card:" + value.ID
	if view.Inline {
		speaker := protocol.ConversationSpeakerAgent
		return r.publish(ctx, protocol.KindItemDone, key, protocol.ItemDone{Text: &view.Text, Speaker: &speaker})
	}
	p := authoredResultWire(value, view.Text)
	if value.Type == result.SurfaceType {
		return r.publish(ctx, protocol.KindResult, key, p)
	}
	text, next, err := resultTextPage(*p.Text, 0)
	if err != nil {
		return err
	}
	p.Text, p.Next = &text, next
	return r.publish(ctx, protocol.KindResult, key, p)
}

// A channel uses the same pinned shape as the app. The existing conversation
// link opens the source card; only an eligible short table is sent inline.
func (r *conversationRuntime) typedChannelText(ctx context.Context, source ledger.Event) (string, error) {
	value, view, err := r.typedResultView(ctx, source)
	if err != nil {
		return "", err
	}
	if value.Type == result.SurfaceType {
		return value.Text + "\n" + "lina://conversation/" + r.conversation, nil
	}
	if view.Inline {
		return view.Text, nil
	}
	cfg := conversationReplyConfig(view.Check.Shapes)
	cfg.Budget = view.Check.Budget
	filter, err := reply.NewFilter(cfg)
	if err != nil {
		return "", err
	}
	caption, err := filter.Push(strings.TrimSpace(view.Text + " " + value.Title))
	if err != nil {
		return "", err
	}
	label := ""
	if value.Completion != nil {
		label = value.Completion.Label + "\n"
	}
	return strings.TrimSpace(caption.Text + "\n" + label + "lina://conversation/" + r.conversation), nil
}

// Planning uses the same stored result boundary, including its records writer.
func (r *conversationRuntime) resultStore(surface ledger.Surface) (*results.Store, error) {
	return results.New(r.db, results.Config{Journal: results.LedgerJournal{Surface: surface}, Writer: results.NewRecordsWriter(r.records, r.assets, nil)})
}

func (r *conversationRuntime) publishResult(ctx context.Context, value result.Result) error {
	var id string
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT event_id FROM result_heads WHERE id=? AND agent=? AND conversation=? AND revision=?`, value.ID, r.agent, r.conversation, value.Revision).Scan(&id)
	})
	if err != nil {
		return err
	}
	source, err := r.journal().Get(ctx, id)
	if err != nil {
		return err
	}
	return r.publishTypedResult(ctx, source)
}
