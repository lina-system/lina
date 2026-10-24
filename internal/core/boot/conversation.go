// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/thisisjun786/lina/internal/core/a2a"
	"github.com/thisisjun786/lina/internal/core/accounts"
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/ghclient"
	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/results"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/core/timers"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/protocol"
	builtin "github.com/thisisjun786/lina/skills"
)

// conversationRuntime is boot's composition of existing stores and flows. The
// model and all accepted work belong to Core, independent of client lifetime.
type conversationRuntime struct {
	publications       *publicationRuntime
	externalTask       string
	summaryPreparation conversationPreparation
	// Installed only by the isolated intake replay command, never by Core boot.
	intakeReplay    func(context.Context, ledger.Event) error
	intakeSnapshot  bool
	artifactInit    sync.Once
	artifactMu      *sync.Mutex
	exchangeStop    context.CancelFunc
	exchangeDone    chan struct{}
	timers          *timers.Store
	timerStop       context.CancelFunc
	timerDone       chan struct{}
	completionStop  context.CancelFunc
	completionDone  chan struct{}
	workEngine      *workRuntime
	coding          *codingTurn
	codingDelivery  *conversationRuntime
	workerDispatch  *dispatch.Service
	liveWorkers     *worker.Live
	workerGitHub    *ghclient.Client
	work            *work.Service
	waitingStop     context.CancelFunc
	waitingDone     chan struct{}
	workWaiting     *waiting.Rounds
	ownerViews      *ownerViews
	background      *backgroundRuntime
	workers         *workerRuntime
	router          *conversationRouter
	decisions       *clientsrv.Decisions
	replaySocket    string
	dispatchMu      *sync.RWMutex
	removals        *retract.Store
	messenger       *messenger.Service
	messengerStop   context.CancelFunc
	messengerDone   chan struct{}
	channels        *channelRuntime
	workArea        *work.Area
	delegation      *delegationRuntime
	channelSurfaces atomic.Int64
	channelBinding  *channels.Binding
	channelAddress  channels.Address
	coordinator     *conversationRuntime

	materials        *conversationMaterials
	miniApps         *miniAppRuntime
	recallStop       context.CancelFunc
	recallDone       chan struct{}
	assets           *assets.Store
	root             string
	steering         map[string][]acceptedSteering
	ctx              context.Context
	db               *canon.Store
	accounts         *accounts.Store
	vault            *secrets.Store
	remoteAgents     *a2a.Service
	a2aStop          context.CancelFunc
	a2aDone          chan struct{}
	webTransport     http.RoundTripper
	signin           *accounts.SignIn
	agents           *agents.Store
	skills           *skills.Store
	records          *records.Store
	recordCaptureMu  sync.Mutex
	recordCaptures   map[string]*recordCapture
	planResults      *results.Store
	plans            *results.Planning
	planning         *planningRuntime
	planningMu       sync.Mutex
	persona          *coreexec.Persona
	personaAuthority *authority.Service
	personaGrants    map[string]grant.Grant
	personaMu        *sync.Mutex
	main             string
	epoch            uint64
	controls         *ctlfiles.Store
	presets          *presetPublisher

	skillMaintenance            *skillMaintenanceRuntime
	memoryExtraction            *memoryExtractionRuntime
	growthMu                    sync.Mutex
	growth                      *growthRuntime
	lanes                       *lane.Manager
	topics                      *clientsrv.Topics
	route                       *modelpath.ResponsesConfig
	activities                  *lifecycle.ActivityRegistry
	person, agent, conversation string
	identity                    clientsrv.Identity
	mu                          sync.Mutex
	publicationMu               sync.Mutex
	activityMu                  sync.Mutex
	turnActive                  bool
	turnPhase                   protocol.TurnPhase
	currentTool                 string
	toolNames                   map[string]string
	toolOrder                   []string
	positioned                  map[string]bool
}

func conversationPath(root string) string { return filepath.Join(root, "canon", "canon.sqlite") }

func startConversation(ctx context.Context, root, main string, epoch uint64, controls *ctlfiles.Store, route *modelpath.ResponsesConfig, activities *lifecycle.ActivityRegistry, person string, transport, channelTransport http.RoundTripper, tailnetIP string) (*conversationRuntime, error) {
	if err := os.MkdirAll(filepath.Dir(conversationPath(root)), 0700); err != nil {
		return nil, err
	}
	db, err := canon.Open(ctx, canon.Config{Path: conversationPath(root), Epoch: epoch, Controls: controls, Migrations: ApplicationMigrations()})
	if err != nil {
		return nil, err
	}
	r := &conversationRuntime{artifactMu: &sync.Mutex{}, workers: &workerRuntime{sessions: map[string]*workerSession{}}, liveWorkers: worker.NewLive(), dispatchMu: &sync.RWMutex{}, root: root, controls: controls, steering: map[string][]acceptedSteering{}, ctx: ctx, db: db, route: route, activities: activities, lanes: lane.New(), person: "person:" + main, main: main, epoch: epoch, personaGrants: map[string]grant.Grant{}, personaMu: &sync.Mutex{}}

	if person != "" {
		r.person = person
	}
	success := false
	defer func() {
		if !success {
			r.Close(context.Background())
		}
	}()
	r.agents, err = agents.Open(root, db)
	if err != nil {
		return nil, err
	}
	if err = r.agents.RegisterMethodologies(ctx); err != nil {
		return nil, err
	}
	if err := r.refreshPersona(ctx); err != nil {
		return nil, err
	}
	first, err := r.agents.FirstRun(ctx, r.person, true)
	if errors.Is(err, agents.ErrNotFound) {
		rows, e := r.agents.List(ctx)
		err = e
		if len(rows) > 0 {
			first.Agent = rows[0]
		}
	}
	if err != nil {
		return nil, err
	}
	if selected, e := r.agents.Selected(ctx, r.person); e == nil {
		first.Agent = selected
	} else if !errors.Is(e, agents.ErrNotFound) {
		return nil, e
	}
	r.removals, err = retract.Open(ctx, retract.Config{Canon: db, Root: root})
	if err != nil {
		return nil, err
	}
	r.agent = first.Agent.ID
	r.conversation = "main:" + r.agent
	r.assets, err = assets.Open(ctx, root, db)
	if err != nil {
		return nil, err
	}
	writer, err := assets.OpenMaterialWriter(ctx, r.assets)
	if err != nil {
		return nil, err
	}
	r.materials = &conversationMaterials{store: r.assets, writer: writer, actor: assets.Actor{PersonID: r.person, AgentID: r.agent}}
	r.skills, err = skills.Open(root, db, builtin.Builtins())
	if err != nil {
		return nil, err
	}
	r.records, err = records.Open(root, db)
	if err != nil {
		return nil, err
	}
	r.records.ReadCandidateMaterial = func(ctx context.Context, id string, revision int64) (string, error) {
		return r.materials.Read(ctx, ledger.Material{ID: id, Revision: revision})
	}
	if err = r.initPlanning(); err != nil {
		return nil, err
	}
	r.persona = coreexec.NewPersona(db, r.agents, r.materials)
	r.personaAuthority, err = authority.Open(ctx, db, authority.Config{Epoch: epoch, Resolve: r.resolvePersonaGrant,
		CanPresent: func() bool { return r.decisions != nil && r.decisions.CanPresent() || r.channelSurfaces.Load() > 0 },
		ApprovalsChanged: func(ctx context.Context) {
			if r.decisions != nil {
				r.decisions.ApprovalsChanged(ctx)
				r.workers.deliverAnswers(ctx)
			}
		},
	})
	if err != nil {
		return nil, err
	}

	if err = recoverConversationTurns(ctx, db); err != nil {
		return nil, err
	}
	r.topics, err = clientsrv.NewTopics(strconv.FormatUint(epoch, 10), clientsrv.TopicOptions{})
	if err != nil {
		return nil, err
	}
	r.router = &conversationRouter{root: r, views: map[string]*conversationRuntime{r.agent: r}}
	r.identity = clientsrv.Identity{PersonID: r.person, Agents: []protocol.Agent{{AgentID: r.agent, Name: first.Agent.Name}}, Topics: r.topics, AuthorizeTopic: r.topicAuthorized, TopicCapabilities: map[string]protocol.Capability{
		"core": clientsrv.ConversationCapability, "conversation:": clientsrv.ConversationCapability, "results:": clientsrv.ConversationCapability,
		"launcher": clientsrv.LauncherCapability,
	}}
	if r.agent == "" {
		r.identity.Agents = []protocol.Agent{}
	}
	if route == nil {
		r.identity.Topics = nil
		r.identity.TopicCapabilities = nil
	} else if err := addOperations(&r.identity, clientsrv.ConversationOperations(r.router), clientsrv.LauncherOperations(r.router), r.resultOperations()); err != nil {
		return nil, err
	}
	r.vault, err = secrets.OpenFiles(root)
	if err != nil {
		return nil, err
	}
	r.accounts, err = accounts.Open(ctx, db, r.vault, accounts.ChatGPTRefresher(activities, transport))
	if err != nil {
		return nil, err
	}
	r.signin, err = accounts.NewSignIn(ctx, accounts.SignInConfig{Store: r.accounts, Person: r.person, Leaser: activities, Transport: transport})
	if err != nil {
		return nil, err
	}
	relay := clientsrv.NewSignInRelay(r.person, r.signin)
	if err = r.initA2A(relay); err != nil {
		return nil, err
	}
	if err := addOperations(&r.identity, clientsrv.AccountOperations(r.person, r.accounts, relay)); err != nil {
		return nil, err
	}
	if err := addOperations(&r.identity, clientsrv.RecordOperations(r)); err != nil {
		return nil, err
	}
	if err := addOperations(&r.identity, clientsrv.MiniAppOperations(r)); err != nil {
		return nil, err
	}
	if err := addOperations(&r.identity, clientsrv.WebOperations(r)); err != nil {
		return nil, err
	}
	if err := addOperations(&r.identity, clientsrv.ReplayOperations(r)); err != nil {
		return nil, err
	}
	if err = r.startDecisions(ctx); err != nil {
		return nil, err
	}
	git, _ := proc.LookPath("git")
	r.workArea, err = work.OpenArea(root, git)
	if err != nil {
		return nil, err
	}
	r.work = work.NewWithArea(r.db, r.workArea, workMaterials{writer: writer, store: r.assets, person: r.person})
	if err = r.work.Recover(ctx, strconv.FormatUint(r.epoch, 10)); err != nil {
		return nil, err
	}
	if err = r.recoverWorkAuthority(ctx); err != nil {
		return nil, err
	}
	if err := addOperations(&r.identity, clientsrv.WorkOperations(r)); err != nil {
		return nil, err
	}
	if err := addOperations(&r.identity, clientsrv.WorkControlOperations(r)); err != nil {
		return nil, err
	}
	if err := addOperations(&r.identity, clientsrv.WorkLimitOperations(r)); err != nil {
		return nil, err
	}
	if err = r.startWorkerLanes(); err != nil {
		return nil, err
	}
	if err = r.startWorkWaiting(ctx); err != nil {
		return nil, err
	}

	if err = r.startRuns(ctx); err != nil {
		return nil, err
	}

	if err = r.recoverA2A(ctx); err != nil {
		return nil, err
	}
	if r.agent != "" {
		if err = r.seedHistory(ctx); err != nil {
			return nil, err
		}
	}
	r.channels, err = startChannels(r, epoch, channelTransport)
	if err != nil {
		return nil, err
	}
	if err := addOperations(&r.identity, clientsrv.ChannelOperations(r.channels)); err != nil {
		return nil, err
	}
	if err := addOperations(&r.identity, clientsrv.TalkOperations(r)); err != nil {
		return nil, err
	}
	if err := addOperations(&r.identity, clientsrv.ManagementOperations(r)); err != nil {
		return nil, err
	}
	if err = r.startSkillMaintenance(ctx); err != nil {
		return nil, err
	}
	if err = r.startMemoryExtraction(ctx); err != nil {
		return nil, err
	}
	if err = r.startPlanning(ctx); err != nil {
		return nil, err
	}
	if err = r.startGrowth(ctx, controls); err != nil {
		return nil, err
	}
	r.startA2AObserver(ctx)
	if err = r.startOwnerViews(ctx); err != nil {
		return nil, err
	}
	if err = r.initPublications(ctx); err != nil {
		return nil, err
	}
	// Producers and the reactor may schedule recovered turns immediately.
	// Start them only after background services and the final lanes are ready.
	if err = r.startWorkerDispatch(ctx); err != nil {
		return nil, err
	}
	if err = r.startWorkerRecovery(ctx); err != nil {
		return nil, err
	}
	if delegationErr := r.startDelegation(ctx); delegationErr != nil {
		// Optional worker readiness cannot take down conversation or channels.
		if r.delegation != nil {
			_ = r.delegation.close(ctx)
			r.delegation = nil
		}
		// Retain the failed readiness stage for clients arriving after boot.
		// Worker error text can contain private paths or provider messages.
		var failure *delegationReadinessError
		if errors.As(delegationErr, &failure) {
			data, _ := json.Marshal(protocol.ReadinessFailure{Step: failure.step})
			if publishErr := r.topics.Publish("core", clientsrv.TopicEvent{Kind: protocol.KindReadinessFailure, Key: "delegation", Data: data}); publishErr != nil {
				fmt.Fprintln(os.Stderr, "Delegation readiness could not be published.")
			}
		}
		fmt.Fprintln(os.Stderr, "Delegation is unavailable; other Core capabilities remain available.")
	}
	if err = r.startCompletions(ctx); err != nil {
		return nil, err
	}
	if err = r.startExchanges(ctx); err != nil {
		return nil, err
	}
	if err = r.startTimers(ctx); err != nil {
		return nil, err
	}
	if err = r.startMiniApps(ctx, tailnetIP); err != nil {
		return nil, err
	}
	if err = r.resumeWorkerCompletions(ctx); err != nil {
		return nil, err
	}
	r.publications.start(ctx)
	success = true
	return r, nil
}
func (r *conversationRuntime) Close(ctx context.Context) error {
	var err error
	if r.miniApps != nil {
		r.miniApps.host.Close()
		r.miniApps = nil
	}
	if r.messengerStop != nil {
		r.messengerStop()
		select {
		case <-r.messengerDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if r.publications != nil && r.publications.stop != nil {
		r.publications.stop()
		select {
		case <-r.publications.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if r.recallStop != nil {
		r.recallStop()
		select {
		case <-r.recallDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if r.a2aStop != nil {
		r.a2aStop()
		select {
		case <-r.a2aDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if r.exchangeStop != nil {
		r.exchangeStop()
		select {
		case <-r.exchangeDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if r.timerStop != nil {
		r.timerStop()
		select {
		case <-r.timerDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if r.ownerViews != nil && r.ownerViews.stop != nil {
		r.ownerViews.stop()
		select {
		case <-r.ownerViews.done:
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		}
	}
	if r.delegation != nil {
		err = errors.Join(err, r.delegation.close(ctx))
		r.delegation = nil
	}
	if r.completionStop != nil {
		r.completionStop()
		select {
		case <-r.completionDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if r.workEngine != nil {
		waitErr := r.workEngine.close(ctx)
		select {
		case <-r.workEngine.done:
			// A joined engine's failure must not skip the remaining cleanup.
			// Check completion itself: a wait timeout leaves its stores in use.
			err = r.workEngine.err
		default:
			return waitErr
		}
	}
	if r.liveWorkers != nil {
		// End kept worker processes before their adapters' services close.
		if closeErr := r.liveWorkers.Close(ctx); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	if r.workerDispatch != nil {
		if closeErr := r.workerDispatch.Close(ctx); closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	if r.workerGitHub != nil {
		if closeErr := r.workerGitHub.Close(ctx); closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	if m := r.skillMaintenance; m != nil && m.stop != nil {
		m.stop()
		select {
		case <-m.done:
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		}
		m.stop = nil
	}
	if m := r.memoryExtraction; m != nil && m.stop != nil {
		m.stop()
		select {
		case <-m.done:
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		}
		m.stop = nil
	}
	if r.waitingStop != nil {
		r.waitingStop()
		select {
		case <-r.waitingDone:
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		}
		r.waitingStop = nil
	}
	if r.growth != nil && r.growth.stop != nil {
		r.growth.stop()
		select {
		case result := <-r.growth.done:
			err = errors.Join(err, result)
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		}
		r.growth.stop = nil
	}
	if r.channels != nil {
		r.channels.Close()
	}
	if r.lanes != nil {
		if closeErr := r.lanes.Close(ctx); closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	if r.workers != nil {
		r.workers.close()
	}
	if b := r.background; b != nil {
		if b.stop != nil {
			b.stop()
			select {
			case <-b.done:
			case <-ctx.Done():
				return errors.Join(err, ctx.Err())
			}
		}
		if b.service != nil {
			err = errors.Join(err, b.service.Close())
		}
	}
	if r.signin != nil {
		r.signin.Close()
	}
	if r.accounts != nil {
		r.accounts.Close()
	}
	if r.vault != nil {
		r.vault.Close()
	}
	if r.topics != nil {
		r.topics.Close()
	}
	if r.removals != nil {
		err = errors.Join(err, r.removals.Close())
	}
	if r.agents != nil {
		err = errors.Join(err, r.agents.Close())
	}
	if r.records != nil {
		err = errors.Join(err, r.records.Close())
	}
	if r.skills != nil {
		err = errors.Join(err, r.skills.Close())
	}
	if r.workArea != nil {
		err = errors.Join(err, r.workArea.Close(ctx))
	}
	if r.assets != nil {
		err = errors.Join(err, r.assets.Close())
	}
	if r.db != nil {
		err = errors.Join(err, r.db.Close(ctx))
	}
	return err
}
