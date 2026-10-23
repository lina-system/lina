// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/jobs"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/tools"
)

type growthRuntime struct {
	ready     bool
	mu        sync.Mutex
	noticeMu  sync.Mutex
	delivered map[int64]bool
	jobs      *jobs.Service
	tools     *tools.Registry
	auth      grant.Request
	id        string
	stop      context.CancelFunc
	done      chan error
}

func (r *conversationRuntime) startGrowth(ctx context.Context, controls *ctlfiles.Store) error {
	// Register the capability and socket even when every agent is in the trash.
	// The router binds each request/job to a currently installed agent.
	r.growth = &growthRuntime{}
	rows, err := r.agents.List(ctx)
	if err != nil {
		return err
	}
	for _, a := range rows {
		view, err := r.router.view(ctx, clientsrv.Session{PersonID: r.person}, a.ID)
		if err != nil {
			return err
		}
		if err = view.ensureGrowth(ctx); err != nil {
			return err
		}
	}
	listener, err := jobs.Listen(filepath.Join(r.root, "run", "jobs.sock"), controls)
	if err != nil {
		return err
	}
	jobCtx, cancel := context.WithCancel(ctx)
	r.growth.stop, r.growth.done = cancel, make(chan error, 1)
	go func() { r.growth.done <- jobs.Serve(jobCtx, listener, growthJobAPI{r}) }()
	management := r.identity.Operations[protocol.KindAgentRequest]
	growthCapability := protocol.Capability{Name: "growth", Version: "1.0"}
	required := func(e protocol.Envelope) protocol.Capability {
		if p, ok := e.Payload.(protocol.AgentRequest); ok && p.Operation != nil {
			switch *p.Operation {
			case "growth_adopt", "growth_notices", "growth_undo":
				return growthCapability
			}
		}
		return management.Capability
	}
	r.identity.Operations[protocol.KindAgentRequest] = clientsrv.Operation{
		Capability:             management.Capability,
		AdditionalCapabilities: []protocol.Capability{growthCapability},
		CapabilityFor:          required,
		Handle: func(ctx context.Context, session clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
			if required(e) == growthCapability {
				p := e.Payload.(protocol.AgentRequest)
				if p.AgentID == nil {
					return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
				}
				if session.PersonID != r.person {
					return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorOutOfScope}
				}
				view, err := r.router.view(ctx, session, *p.AgentID)
				if err != nil {
					return clientsrv.Result{}, err
				}
				if err = view.ensureGrowth(ctx); err != nil {
					return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
				}
				return view.growthOperation(ctx, session, e)
			}
			return management.Handle(ctx, session, e)
		},
	}
	return nil
}

// ensureGrowth binds immutable job policy and tools to this conversation view.
// The socket and its lifecycle remain owned by the root runtime.
func (r *conversationRuntime) ensureGrowth(ctx context.Context) error {
	r.growthMu.Lock()
	defer r.growthMu.Unlock()
	if r.growth == nil {
		r.growth = &growthRuntime{}
	}
	g := r.growth
	if g.ready {
		return nil
	}
	g.id, g.delivered = "growth:"+r.agent, map[int64]bool{}
	d, err := jobs.GrowthDefinition(r.agents, r.agent, r.person)
	if err != nil {
		return err
	}
	g.jobs, err = jobs.New(jobs.Config{Store: r.db, Epoch: r.epoch, Definitions: map[string]jobs.Definition{g.id: d}, AfterApply: r.publishGrowth})
	if err != nil {
		return err
	}
	if err = g.jobs.Ensure(ctx, jobapi.Job{ID: g.id, Kind: jobapi.Memory, Enabled: true, Deterministic: true, Every: time.Nanosecond, Timeout: 10 * time.Second, Adoption: "baseline"}); err != nil {
		return err
	}
	// The owner-control policy is bound by Core, never supplied by tool arguments.
	g.auth = grant.Request{GrantID: g.id, MainID: r.main, Epoch: r.epoch, TaskID: "growth-controls", Host: r.main, OSUser: strconv.Itoa(os.Geteuid()), Person: r.person, Agent: r.agent}
	target := "agent:" + r.agent
	gate, err := authority.Open(ctx, r.db, authority.Config{Epoch: r.epoch, Resolve: func(ctx context.Context, id string) (grant.Grant, grant.Environment, error) {
		if id != g.id {
			return grant.Grant{}, grant.Environment{}, tools.ErrInvalid
		}
		a, err := r.agents.Get(ctx, r.agent)
		if err != nil {
			return grant.Grant{}, grant.Environment{}, err
		}
		auth := g.auth
		auth.Revision = strconv.FormatInt(a.Revision, 10)
		now := time.Now()
		return grant.Grant{Identity: auth, Expires: now.Add(time.Minute), Scope: []grant.Permission{{Tool: "growth_notices", Target: target}, {Tool: "undo_growth", Target: target, Write: true}}, Revisions: map[string]string{target: auth.Revision}}, grant.Environment{Epoch: r.epoch, Now: now, OSPermitted: true, Revision: auth.Revision}, nil
	}})
	if err != nil {
		return err
	}
	g.tools = tools.NewRegistry(gate, r.activities)
	if _, err = coreexec.RegisterGrowth(g.tools, r.agents, r.agent, r.person); err != nil {
		return err
	}
	// seedHistory already projected existing ledger notices in this boot.
	notices, err := r.agents.GrowthNotices(ctx, r.agent)
	if err != nil {
		return err
	}
	for _, n := range notices {
		_, err := r.journal().Get(ctx, "growth:"+r.agent+":"+strconv.FormatInt(n.Revision, 10))
		if err == nil {
			g.delivered[n.Revision] = true
		} else if !errors.Is(err, ledger.ErrNotFound) {
			return err
		}
	}

	g.ready = true
	// Recovery failures stay local to the optional job. Durable notices remain
	// available for another delivery attempt and for the next boot.
	_ = g.jobs.DrainDeterministic(ctx, g.id)
	_ = r.publishGrowth(ctx)
	return nil
}

// growthJobAPI keeps a single worker socket while resolving jobs against the
// live agent store. A deleted or unknown agent never yields a write callback.
type growthJobAPI struct{ root *conversationRuntime }

func (api growthJobAPI) service(ctx context.Context, id string) (*jobs.Service, error) {
	if id == jobs.SkillMaintenanceJob && api.root.skillMaintenance != nil {
		return api.root.skillMaintenance.jobs, nil
	}
	if strings.HasPrefix(id, "planning:") {
		view, err := api.root.router.view(ctx, clientsrv.Session{PersonID: api.root.person}, strings.TrimPrefix(id, "planning:"))
		if err != nil {
			return nil, jobapi.ErrUnavailable
		}
		if err = view.ensurePlanning(ctx); err != nil {
			return nil, err
		}
		return view.planning.jobs, nil
	}
	if !strings.HasPrefix(id, "growth:") {
		return nil, jobapi.ErrUnavailable
	}
	view, err := api.root.router.view(ctx, clientsrv.Session{PersonID: api.root.person}, strings.TrimPrefix(id, "growth:"))
	if err != nil {
		return nil, jobapi.ErrUnavailable
	}
	if err = view.ensureGrowth(ctx); err != nil {
		return nil, err
	}
	return view.growth.jobs, nil
}
func (api growthJobAPI) Aux(ctx context.Context, p jobapi.AuxRequest) (jobapi.AuxResult, error) {
	s, err := api.service(ctx, p.JobID)
	if err != nil {
		return jobapi.AuxResult{}, err
	}
	return s.Aux(ctx, p)
}
func (api growthJobAPI) Apply(ctx context.Context, p jobapi.Proposal) (jobapi.Applied, error) {
	s, err := api.service(ctx, p.JobID)
	if err != nil {
		return jobapi.Applied{}, err
	}
	return s.Apply(ctx, p)
}
func (api growthJobAPI) Report(ctx context.Context, id string, state jobapi.JobState) error {
	s, err := api.service(ctx, id)
	if err != nil {
		return err
	}
	return s.Report(ctx, id, state)
}

func (r *conversationRuntime) growthOperation(ctx context.Context, session clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
	refuse := func(code protocol.ErrorCode) (clientsrv.Result, error) {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: code}
	}
	p, ok := e.Payload.(protocol.AgentRequest)
	if !ok || p.Operation == nil || p.AgentID == nil || p.Name != nil {
		return refuse(protocol.ErrorMalformed)
	}
	if session.PersonID != r.person || *p.AgentID != r.agent {
		return refuse(protocol.ErrorOutOfScope)
	}
	if *p.Operation != "growth_notices" && !session.CanDecide {
		return refuse(protocol.ErrorNotAuthorized)
	}
	g := r.growth
	g.mu.Lock()
	defer g.mu.Unlock()
	a, err := r.agents.Get(ctx, r.agent)
	if err != nil {
		return refuse(protocol.ErrorUnavailable)
	}
	effect := protocol.EffectNone
	switch *p.Operation {
	case "growth_adopt":
		if p.Candidate == nil || p.Revision != nil {
			return refuse(protocol.ErrorMalformed)
		}
		expected, err := strconv.ParseInt(p.Candidate.Expected, 10, 64)
		if err != nil {
			return refuse(protocol.ErrorMalformed)
		}
		candidate := agents.GrowthCandidate{ID: p.Candidate.ID, Expected: expected, Snapshot: p.Candidate.Snapshot, Notice: p.Candidate.Notice}
		raw, _ := json.Marshal(candidate)
		err = g.jobs.SubmitDeterministic(ctx, g.id, raw)
		if errors.Is(err, jobapi.ErrConflict) {
			return refuse(protocol.ErrorStaleRevision)
		}
		if errors.Is(err, jobapi.ErrInvalid) {
			return refuse(protocol.ErrorMalformed)
		}
		if err != nil {
			return refuse(protocol.ErrorUnavailable)
		}
		effect = protocol.EffectApplied
	case "growth_notices":
		if p.Candidate != nil || p.Revision != nil {
			return refuse(protocol.ErrorMalformed)
		}
	case "growth_undo":
		if p.Candidate != nil || p.Revision == nil {
			return refuse(protocol.ErrorMalformed)
		}
		revision, err := strconv.ParseInt(*p.Revision, 10, 64)
		if err != nil {
			return refuse(protocol.ErrorMalformed)
		}
		auth := g.auth
		auth.Revision = strconv.FormatInt(a.Revision, 10)
		raw, _ := json.Marshal(agents.GrowthUndo{Revision: revision})
		result, err := g.tools.Dispatch(ctx, tools.Call{Key: "growth-undo:" + session.ClientID + ":" + e.MessageID, Name: "undo_growth", Auth: auth, Args: raw})
		if err != nil || result.State != tools.Applied {
			return refuse(protocol.ErrorStaleRevision)
		}
		effect = protocol.EffectApplied
	default:
		return refuse(protocol.ErrorMalformed)
	}
	_ = r.publishGrowth(ctx)
	a, err = r.agents.Get(ctx, r.agent)
	if err != nil {
		return refuse(protocol.ErrorUnavailable)
	}
	auth := g.auth
	auth.Revision = strconv.FormatInt(a.Revision, 10)
	result, err := g.tools.Dispatch(ctx, tools.Call{Key: "growth-notices:" + session.ClientID + ":" + e.MessageID, Name: "growth_notices", Auth: auth, Args: json.RawMessage(`{}`)})
	if err != nil || result.State != tools.Applied {
		return refuse(protocol.ErrorUnavailable)
	}
	var notices []agents.GrowthNotice
	if err = json.Unmarshal(result.Body, &notices); err != nil {
		return refuse(protocol.ErrorUnavailable)
	}
	rows := []protocol.GrowthNotice{}
	for _, n := range notices {
		rows = append(rows, protocol.GrowthNotice{Revision: strconv.FormatInt(n.Revision, 10), Notice: n.Notice, UndoRevision: strconv.FormatInt(n.Undo.Revision, 10)})
	}
	revision := strconv.FormatInt(a.Revision, 10)
	return clientsrv.Result{Kind: protocol.KindAgentResult, Effect: effect, Payload: protocol.AgentResult{AgentID: &r.agent, Revision: &revision, Notices: &rows}}, nil
}

// The committed notice is an outbox: startup fills any gap after Apply committed
// but before delivery. Stable ledger/stream ids prevent duplicate learning lines.
func (r *conversationRuntime) publishGrowth(ctx context.Context) error {
	r.growth.noticeMu.Lock()
	defer r.growth.noticeMu.Unlock()
	notices, err := r.agents.GrowthNotices(ctx, r.agent)
	if err != nil {
		return err
	}
	for _, n := range notices {
		if r.growth.delivered[n.Revision] {
			continue
		}
		id := "growth:" + r.agent + ":" + strconv.FormatInt(n.Revision, 10)
		event, err := r.journal().Get(ctx, id)
		if errors.Is(err, ledger.ErrNotFound) {
			now := time.Now()
			event, err = r.journal().Admit(ctx, guard.EntryAgent, ledger.Input{ID: id, ConversationID: r.conversation, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: n.Notice})
		}
		if err != nil {
			return err
		}
		text, speaker := event.Body.Text(), protocol.ConversationSpeakerAgent
		if err = r.publish(ctx, protocol.KindItemDone, id, protocol.ItemDone{Text: &text, Speaker: &speaker}); err != nil {
			return err
		}
		r.growth.delivered[n.Revision] = true
	}
	return nil
}
