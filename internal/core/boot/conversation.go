// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/accounts"
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/assemble/atropos"
	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/ghclient"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/reply"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/core/timers"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	builtin "github.com/thisisjun786/lina/skills"
)

// conversationRuntime is boot's composition of existing stores and flows. The
// model and all accepted work belong to Core, independent of client lifetime.
type acceptedSteering struct {
	event   ledger.Event
	inboxID int64
	runner  *conversationRuntime
}

type conversationRuntime struct {
	timers          *timers.Store
	timerStop       context.CancelFunc
	timerDone       chan struct{}
	completionStop  context.CancelFunc
	completionDone  chan struct{}
	workEngine      *workRuntime
	coding          *codingTurn
	codingDelivery  *conversationRuntime
	workerDispatch  *dispatch.Service
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
	channels        *channelRuntime
	workArea        *work.Area
	channelSurfaces atomic.Int64
	channelBinding  *channels.Binding
	channelAddress  channels.Address
	coordinator     *conversationRuntime

	materials        *conversationMaterials
	assets           *assets.Store
	root             string
	steering         map[string][]acceptedSteering
	ctx              context.Context
	db               *canon.Store
	accounts         *accounts.Store
	vault            *secrets.Store
	webTransport     http.RoundTripper
	signin           *accounts.SignIn
	agents           *agents.Store
	skills           *skills.Store
	records          *records.Store
	recordCaptureMu  sync.Mutex
	recordCaptures   map[string]*recordCapture
	persona          *coreexec.Persona
	personaAuthority *authority.Service
	personaGrants    map[string]grant.Grant
	personaMu        *sync.Mutex
	main             string
	epoch            uint64
	controls         *ctlfiles.Store
	presets          *presetPublisher

	skillMaintenance            *skillMaintenanceRuntime
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

func startConversation(ctx context.Context, root, main string, epoch uint64, controls *ctlfiles.Store, route *modelpath.ResponsesConfig, activities *lifecycle.ActivityRegistry, person string, transport, channelTransport http.RoundTripper) (*conversationRuntime, error) {
	if err := os.MkdirAll(filepath.Dir(conversationPath(root)), 0700); err != nil {
		return nil, err
	}
	db, err := canon.Open(ctx, canon.Config{Path: conversationPath(root), Epoch: epoch, Controls: controls, Migrations: ApplicationMigrations()})
	if err != nil {
		return nil, err
	}
	r := &conversationRuntime{workers: &workerRuntime{sessions: map[string]*workerSession{}}, dispatchMu: &sync.RWMutex{}, root: root, controls: controls, steering: map[string][]acceptedSteering{}, ctx: ctx, db: db, route: route, activities: activities, lanes: lane.New(), person: "person:" + main, main: main, epoch: epoch, personaGrants: map[string]grant.Grant{}, personaMu: &sync.Mutex{}}

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
	r.identity = clientsrv.Identity{PersonID: r.person, Agents: []protocol.Agent{{AgentID: r.agent, Name: first.Agent.Name}}, Operations: clientsrv.ConversationOperations(r.router), Topics: r.topics, AuthorizeTopic: r.topicAuthorized, TopicCapabilities: map[string]protocol.Capability{
		"core": clientsrv.ConversationCapability, "conversation:": clientsrv.ConversationCapability, "results:": clientsrv.ConversationCapability,
	}}
	if r.agent == "" {
		r.identity.Agents = []protocol.Agent{}
	}
	if route == nil {
		r.identity.Topics = nil
		r.identity.TopicCapabilities = nil
		r.identity.Operations = make(map[protocol.Kind]clientsrv.Operation)
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
	for kind, op := range clientsrv.AccountOperations(r.person, r.accounts, relay) {
		r.identity.Operations[kind] = op
	}
	for kind, op := range clientsrv.RecordOperations(r) {
		r.identity.Operations[kind] = op
	}
	for kind, op := range clientsrv.WebOperations(r) {
		r.identity.Operations[kind] = op
	}
	for kind, op := range clientsrv.ReplayOperations(r) {
		r.identity.Operations[kind] = op
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
	r.startWorkerLanes()
	if err = r.startWorkWaiting(ctx); err != nil {
		return nil, err
	}
	if err = r.startRuns(ctx); err != nil {
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
	for kind, op := range clientsrv.ChannelOperations(r.channels) {
		r.identity.Operations[kind] = op
	}
	for kind, op := range clientsrv.TalkOperations(r) {
		r.identity.Operations[kind] = op
	}
	for kind, op := range clientsrv.ManagementOperations(r) {
		r.identity.Operations[kind] = op
	}
	if err = r.startSkillMaintenance(ctx); err != nil {
		return nil, err
	}
	if err = r.startGrowth(ctx, controls); err != nil {
		return nil, err
	}
	if err = r.startOwnerViews(ctx); err != nil {
		return nil, err
	}
	// Producers and the reactor may schedule recovered turns immediately.
	// Start them only after background services and the final lanes are ready.
	if err = r.startWorkerDispatch(ctx); err != nil {
		return nil, err
	}
	if err = r.startCompletions(ctx); err != nil {
		return nil, err
	}
	if err = r.startTimers(ctx); err != nil {
		return nil, err
	}
	if err = r.resumeWorkerCompletions(ctx); err != nil {
		return nil, err
	}
	success = true
	return r, nil
}
func (r *conversationRuntime) Close(ctx context.Context) error {
	var err error
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
func (r *conversationRuntime) authorize(session clientsrv.Session, conversation *string) error {
	if session.PersonID != r.person {
		return &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	if conversation != nil && *conversation != r.conversation {
		return &clientsrv.OperationError{Code: protocol.ErrorOutOfScope}
	}
	return nil
}
func (r *conversationRuntime) History(ctx context.Context, s clientsrv.Session, p protocol.HistoryGet) (protocol.History, error) {
	if err := r.authorize(s, p.ConversationID); err != nil {
		return protocol.History{}, err
	}
	if p.AgentID != nil && *p.AgentID != r.agent {
		return protocol.History{}, &clientsrv.OperationError{Code: protocol.ErrorOutOfScope}
	}
	// Subscriptions own replay and live ordering. History discovers the concrete
	// topic; it does not race a second independently captured history window.
	return protocol.History{ConversationID: &r.conversation, AgentID: &r.agent}, nil
}

func (r *conversationRuntime) Input(ctx context.Context, s clientsrv.Session, p protocol.Input) (protocol.ItemDone, error) {
	if err := r.authorize(s, p.ConversationID); err != nil {
		return protocol.ItemDone{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	masked, err := secrets.Mask(*p.Text)
	if err != nil {
		return protocol.ItemDone{}, err
	}
	digest := sha256.Sum256([]byte(r.person + "\x00" + r.conversation + "\x00" + *p.InputID))
	eventID := "input:" + hex.EncodeToString(digest[:])
	admission := &conversationAdmission{db: r.db, person: r.person, agent: r.agent, input: inbox.Input{Key: inbox.Key{Conversation: r.conversation, Source: "client", NativeID: *p.InputID}, Body: []byte(masked.Text())}}
	language, err := r.inputLanguage(ctx, *p.Text)
	if err != nil {
		return protocol.ItemDone{}, err
	}
	now := time.Now()
	event, err := ledger.New(admission, r.materials).Admit(ctx, guard.EntryOwner, ledger.Input{ID: eventID, ConversationID: r.conversation, PersonID: r.person, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui", Language: string(language)}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Locator: *p.InputID, Body: *p.Text})
	receipt := admission.receipt
	if errors.Is(err, errAdmittedInput) {
		event, err = r.journal().Get(ctx, eventID)
	}
	if err != nil {
		return protocol.ItemDone{}, err
	}
	text := event.Body.Text()
	speaker := protocol.ConversationSpeakerPerson
	answer := protocol.ItemDone{Text: &text, EchoKey: p.InputID, Speaker: &speaker, AgentID: &r.agent, PersonID: &r.person}
	if !receipt.Inserted {
		return answer, nil
	}
	if err = r.publish(ctx, protocol.KindItemDone, eventID, answer); err != nil {
		return protocol.ItemDone{}, err
	}
	err = r.submitInput(event, receipt.ID, p.Mode == nil || *p.Mode == protocol.InputModeSteer, r)
	return answer, err
}

// submitInput runs under the shared conversation's admission mutex. Channel
// adapters reuse this coordinator, including steering during a live turn.
func (r *conversationRuntime) submitInput(event ledger.Event, inboxID int64, steer bool, runner *conversationRuntime) error {
	items, err := assemble.History([]ledger.Event{event}, r.conversation, r.agent)
	if err != nil {
		return err
	}
	if len(items) != 1 {
		return assemble.ErrHistory
	}
	body := items[0]
	if steer && r.lanes.Steer(r.conversation, body) {
		r.steering[string(body)] = append(r.steering[string(body)], acceptedSteering{event: event, inboxID: inboxID, runner: runner})
		return nil
	}
	_, err = r.queueReaction(r.ctx, r.conversation, func(ctx context.Context, inputs *loop.Inputs) error { return runner.run(ctx, inputs, event, inboxID) })
	return err
}

func (r *conversationRuntime) Interrupt(ctx context.Context, s clientsrv.Session, p protocol.Interrupt) error {
	if err := r.authorize(s, p.ConversationID); err != nil {
		return err
	}
	r.activityMu.Lock()
	defer r.activityMu.Unlock()
	if err := r.phaseLocked(ctx, protocol.TurnPhaseStopping, ""); err != nil {
		return err
	}
	r.lanes.Stop(r.conversation, turn.Interrupt{Issuer: turn.Person})
	return nil
}
func (r *conversationRuntime) publish(ctx context.Context, kind protocol.Kind, key string, payload any) error {
	if r.coordinator != nil {
		return r.coordinator.publish(ctx, kind, key, payload)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// Keep durable first appearance and live publication in the same order,
	// including when input admission races the provider observer.
	r.publicationMu.Lock()
	defer r.publicationMu.Unlock()
	positioned := kind == protocol.KindItemDone || kind == protocol.KindOutputDelta || kind == protocol.KindResult || kind == protocol.KindBackgroundNotice
	if positioned {
		if !r.positioned[key] {
			// A committed outcome still publishes after its turn is cancelled.
			saveCtx := context.WithoutCancel(ctx)
			if err := r.db.Write(saveCtx, func(tx *canon.Tx) error {
				_, err := tx.ExecContext(saveCtx, `INSERT INTO conversation_order(conversation_id,item_key) VALUES(?,?) ON CONFLICT DO NOTHING`, r.conversation, key)
				return err
			}); err != nil {
				return err
			}
		}
	}
	agent := r.agent
	if kind == protocol.KindResult || kind == protocol.KindBackgroundNotice {
		// Results carry attribution in provenance, not an agent_id wire field.
		agent = ""
	}
	if err := r.topics.Publish("conversation:"+r.conversation, clientsrv.TopicEvent{Kind: kind, Key: key, Data: data, AgentID: agent}); err != nil {
		return err
	}
	if positioned {
		if r.positioned == nil {
			r.positioned = make(map[string]bool)
		}
		// A remembered position also proves successful live publication. A
		// failed publication must remain retryable even after its SQL commit.
		r.positioned[key] = true
	}
	return nil
}

// The installed conversation currently has no negotiated surface width or
// language setting. Use the Korean ceiling and the acceptance surface's 80
// columns, with the same wrapping as the TUI. Keep policy in the existing filter.
func conversationFilter() (*reply.Filter, error) {
	return reply.NewFilter(reply.Config{Budget: reply.ChatCeiling(), Language: "ko", RenderLines: func(text string) int {
		if text == "" {
			return 0
		}
		stream := render.NewStream("reply", render.Config{Width: 80}, nil)
		stream.Seal(1, text)
		return stream.Len()
	}})
}

// Keep possible credential prefixes private until the admission masker can
// recognize their complete shape. Mask the accumulated prefix so assignment
// and authorization context survives provider delta boundaries. Quoted values,
// structured JSON and PEM blocks wait for the authoritative final item rather
// than exposing an incomplete value or private-key header.
type conversationReplyStream struct {
	filter *reply.Filter
	raw    strings.Builder
	sent   string
}

func (s *conversationReplyStream) Push(delta string) (reply.View, string, error) {
	if !utf8.ValidString(delta) {
		return reply.View{}, "", reply.ErrUTF8
	}
	s.raw.WriteString(delta)
	text := s.raw.String()
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return reply.View{}, "", nil
	}
	if start := strings.Index(text, "-----"); start >= 0 {
		text = text[:start]
	}
	// A quoted assignment can contain whitespace or escaped delimiters. Do not
	// expose any of it before the complete item is available to the masker.
	if start := strings.IndexAny(text, "\"'"); start >= 0 {
		text = text[:start]
	}
	end := len(text)
	// Shape detectors can match inside a word. Hold from any complete or
	// unfinished introducer in the last token, including a split PEM marker.
	// These introducers correspond to secrets.Mask's credential shapes; the
	// masker itself remains the authority for replacing complete values.
	tokenStart := strings.LastIndexAny(text, " \t\r\n") + 1
	for i := tokenStart; i < end; i++ {
		for _, prefix := range []string{"sk-", "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "AIza", "xoxb-", "xoxa-", "xoxp-", "xoxr-", "xoxs-", "AKIA", "ASIA", "eyJ", "-----"} {
			if strings.HasPrefix(text[i:], prefix) || strings.HasPrefix(prefix, text[i:]) {
				end = i
				break
			}
		}
	}
	masked, err := secrets.Mask(text[:end])
	if err != nil {
		return reply.View{}, "", err
	}
	if !strings.HasPrefix(masked.Text(), s.sent) {
		return reply.View{}, "", errors.New("reply masking changed a published prefix")
	}
	next := masked.Text()[len(s.sent):]
	if next == "" {
		return reply.View{}, "", nil
	}
	s.sent = masked.Text()
	view, err := s.filter.Push(next)
	return view, next, err
}

func (r *conversationRuntime) replyCard(key, turnID string, view reply.View, language design.Language) reply.Card {
	return reply.Card{Preview: *view.Card, ResultID: "result:" + key, Revision: 1,
		Title: design.Text(language, "reply.full"), Summary: view.Text, RevisedBy: r.agent,
		Provenance: protocol.ResultProvenance{AgentID: r.agent, ConversationID: r.conversation, TurnID: turnID}}
}

const conversationPreviewBytes = 64 << 10

func (r *conversationRuntime) publishReply(ctx context.Context, key, turnID string, view reply.View, language design.Language) error {
	if view.Card != nil {
		card := r.replyCard(key, turnID, view, language)
		// Bound the wire preview independently of the complete ledger card.
		// Even JSON's worst-case six-byte escapes leave ample envelope space
		// below the RPC writer's 1 MiB limit.
		text := card.Text
		if len(text) > conversationPreviewBytes {
			end := conversationPreviewBytes
			for !utf8.RuneStart(text[end]) {
				end--
			}
			text = text[:end] + "\n… " + design.Text(language, "reply.truncated")
		}
		return r.publish(ctx, protocol.KindResult, key, protocol.Result{
			ResultID: card.ResultID, Revision: "1", Type: card.Type, Status: card.Status,
			Title: card.Title, Summary: card.Summary, Text: &text,
			RevisedBy: card.RevisedBy, Provenance: card.Provenance,
		})
	}
	speaker := protocol.ConversationSpeakerAgent
	return r.publish(ctx, protocol.KindItemDone, key, protocol.ItemDone{Text: &view.Text, Speaker: &speaker})
}

func (r *conversationRuntime) events(ctx context.Context, through string) ([]ledger.Event, error) {
	var ids []string
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		query := `SELECT id FROM conv_event WHERE conversation_id=? AND (speaker_kind IN ('person','agent','forwarded') OR scaffold_kind IN ('interruption','failure','persona_change','run_handoff','background_notice','coding_handoff','work_completion','timer','timer_work','worker_session','worker_completion'))`
		args := []any{r.conversation}
		if through != "" {
			query += ` AND (speaker_kind NOT IN ('person','forwarded') OR rowid <= (SELECT rowid FROM conv_event WHERE id=?))`
			args = append(args, through)
		}
		query += ` ORDER BY rowid DESC LIMIT 200`
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	var events []ledger.Event
	journal := r.journal()
	for i := len(ids) - 1; i >= 0; i-- {
		event, err := journal.Get(ctx, ids[i])
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}
func (r *conversationRuntime) seedHistory(ctx context.Context) error {
	items, err := r.historyItems(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.eventID == "" {
			masked, err := secrets.Mask(item.partial)
			if err != nil {
				return err
			}
			if err = r.publishPartial(ctx, item.key, item.turnID, masked.Text()); err != nil {
				return err
			}
			continue
		}
		e, err := r.journal().Get(ctx, item.eventID)
		if err != nil {
			return err
		}
		text := e.Body.Text()
		if e.ScaffoldKind == "background_notice" {
			var notices []lane.Notice
			if err := json.Unmarshal([]byte(text), &notices); err != nil {
				return err
			}
			if err := r.publishNoticeText(ctx, e.ID, notices); err != nil {
				return err
			}
			continue
		}
		if e.SpeakerKind == guard.Agent {
			filter, err := conversationFilter()
			if err != nil {
				return err
			}
			view, err := filter.Push(text)
			if err != nil {
				return err
			}
			if err = r.publishReply(ctx, e.ID, "turn:"+e.ParentID, view, eventLanguage(e)); err != nil {
				return err
			}
			continue
		}
		speaker := protocol.ConversationSpeaker(e.SpeakerKind)
		p := protocol.ItemDone{Text: &text, Speaker: &speaker}
		if e.SpeakerKind == guard.Person {
			key := e.Origin.Locator
			p.EchoKey = &key
			p.PersonID = &r.person
		}
		if err = r.publish(ctx, protocol.KindItemDone, e.ID, p); err != nil {
			return err
		}
	}
	return r.setActive(ctx, false)
}

func (r *conversationRuntime) publishPartials(ctx context.Context, stopped turn.Stopped) error {
	for _, p := range stopped.Partials {
		// The provider observer numbers rounds from one, the loop from zero.
		key := fmt.Sprintf("%s:%d:%d", stopped.TurnID, p.Round+1, p.Index)
		if err := r.publishPartial(ctx, key, stopped.TurnID, p.Text); err != nil {
			return err
		}
	}
	return nil
}

// Both stopped-turn publication and startup history use the same reply filter,
// including card identity and provenance for a partial that exceeded the ceiling.
func (r *conversationRuntime) publishPartial(ctx context.Context, key, turnID, text string) error {
	filter, err := conversationFilter()
	if err != nil {
		return err
	}
	view, err := filter.Push(text)
	if err != nil {
		return err
	}
	language, err := r.turnLanguage(ctx, turnID)
	if err != nil {
		return err
	}
	return r.publishReply(ctx, key, turnID, view, language)
}

func (r *conversationRuntime) run(ctx context.Context, inputs *loop.Inputs, input ledger.Event, inboxID int64) (err error) {
	turnID := "turn:" + input.ID
	if err = r.setActive(ctx, true); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, r.setActive(ctx, false)) }()
	activity, err := r.activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "conversation"})
	if err != nil {
		return err
	}
	defer activity.Release()
	releaseCredentials := r.vault.BeginTurn()
	defer releaseCredentials()
	bindings := map[string]loop.Tool{}
	if r.channelBinding == nil || r.channelBinding.Scope.Tools {
		if r.router.root.workerDispatch != nil {
			bindings["dispatch_work"] = r.router.root.workerDispatch.Tool(dispatch.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: turnID})
		}
		bindings[runs.StartToolName] = r.background.service.StartTool(r.runOrigin(turnID))
	}
	var promoted budget.Handle
	defer func() {
		r.personaMu.Lock()
		delete(r.personaGrants, turnID)
		delete(r.personaGrants, turnID+":state")
		delete(r.personaGrants, turnID+":expression")
		delete(r.personaGrants, turnID+":relationship")
		delete(r.personaGrants, turnID+":coding")
		r.personaMu.Unlock()

	}()
	var items []provider.Item
	var events []ledger.Event
	var memoryEpoch uint64
	var memoryRefs []turn.Reference

	evidence := turn.EvidenceWriter{Ledger: r.journal(), ConversationID: r.conversation, ParentID: input.ID, Surface: input.Surface, Now: time.Now}
	prep := &turn.Preparation{
		VerifyBuild: r.verifyBuild,
		Account:     func(context.Context) (provider.Credential, error) { return r.route.Request(nil).Credential, nil },
		Resolve: func(context.Context) (turn.ModelPolicy, error) {
			return turn.ModelPolicy{Model: r.route.Model, Revision: "configured", Class: r.route.Adapter.Class()}, nil
		},
		Context: func(ctx context.Context, policy turn.ModelPolicy) (turn.PreparedRequest, error) {
			if err := r.refreshPersona(ctx); err != nil {
				return turn.PreparedRequest{}, err
			}

			persona, err := r.agents.Prepare(ctx, turnID, r.agent)
			if err != nil {
				return turn.PreparedRequest{}, err
			}
			items, memoryRefs, memoryEpoch, events, err = r.modelContext(ctx, input.ID)

			if err != nil {
				return turn.PreparedRequest{}, err
			}
			current, err := assemble.History([]ledger.Event{input}, r.conversation, r.agent)
			if err != nil || len(current) != 1 {
				return turn.PreparedRequest{}, assemble.ErrHistory
			}
			// modelContext includes the current event after its filtered history.
			// Keep the assembly's dedicated current-input slot exactly once, and fail
			// closed if retraction removed it between admission and preparation.
			if len(items) == 0 || !bytes.Equal(items[len(items)-1], current[0]) {
				return turn.PreparedRequest{}, assemble.ErrHistory
			}
			items = items[:len(items)-1]
			at := time.Now().UTC()
			minimum := assemble.ContextPacket{{ID: "clock:" + turnID, Kind: assemble.Clock, Text: at.Format(time.RFC3339Nano)}}
			catalog, personaPacket, err := r.preparePersonaTools(ctx, turnID, input, persona, at, bindings)

			if err != nil {
				return turn.PreparedRequest{}, err
			}
			minimum = append(minimum, personaPacket...)
			methodology, err := r.methodologyContext(ctx)
			if err != nil {
				return turn.PreparedRequest{}, err
			}
			minimum = append(minimum, methodology...)
			if err := r.prepareSkillTools(turnID, bindings); err != nil {
				return turn.PreparedRequest{}, err
			}
			if err := r.prepareWorkTools(turnID, bindings); err != nil {
				return turn.PreparedRequest{}, err
			}
			if err := r.prepareWorkerTools(turnID, input, bindings); err != nil {
				return turn.PreparedRequest{}, err
			}
			if err := r.prepareRecordTools(turnID, bindings); err != nil {
				return turn.PreparedRequest{}, err
			}
			workDeclarations, err := r.prepareGoalWorkTools(turnID, bindings)
			if err != nil {
				return turn.PreparedRequest{}, err
			}
			if err := r.prepareMethodologyTools(turnID, bindings); err != nil {
				return turn.PreparedRequest{}, err
			}
			declarations := append(coreexec.PersonaDeclarations(), coreexec.PersonaSettingDeclaration(), coreexec.WorkMethodDeclaration())
			declarations = append(declarations, coreexec.EmotionDeclarations()...)
			declarations = append(declarations, coreexec.RelationshipDeclaration())
			declarations = append(declarations, coreexec.SkillDeclarations()...)
			declarations = append(declarations, coreexec.RecordDeclarations()...)
			declarations = append(declarations, workDeclarations...)
			declarations = append(declarations, methodologyDeclarations()...)
			declarations = append(declarations, startRunDeclaration())
			webProvider, err := r.selectedWebProvider()
			if err != nil {
				return turn.PreparedRequest{}, err
			}
			webDeclarations, err := r.prepareWebTools(turnID, webProvider, bindings)
			if err != nil {
				return turn.PreparedRequest{}, err
			}
			declarations = append(declarations, webDeclarations...)
			if r.router.root.workerDispatch != nil {
				declarations = append(declarations, dispatch.Declaration())
			}
			declarations = append(declarations, coreexec.WorkDeclarations()...)
			declarations = append(declarations, workerDeclarations()...)
			declarations = append(declarations, r.prepareTimerTools(turnID, bindings)...)
			if r.coding != nil {
				clear(bindings)
				catalog = nil
				declarations, err = r.prepareCodingDecision(turnID, bindings)
				if err != nil {
					return turn.PreparedRequest{}, err
				}
			}
			if r.channelBinding != nil && !r.channelBinding.Scope.Tools {
				clear(bindings)
				declarations = nil
				catalog = nil
			}
			if r.channelBinding != nil {
				for name, tool := range bindings {
					execute := tool.Execute
					tool.Execute = func(ctx context.Context, call provider.ToolCall) loop.Result {
						if err := r.validateChannel(ctx); err != nil || !r.channelBinding.Scope.Tools {
							return loop.Result{ID: call.ID, Code: loop.Denied}
						}
						return execute(ctx, call)
					}
					bindings[name] = tool
				}
			}
			packet, _, err := atropos.Packet(ctx, minimum, 0, func(ctx context.Context) (assemble.ContextPacket, error) { return r.relationshipPacket(ctx, at) })
			if err != nil {
				return turn.PreparedRequest{}, err
			}
			if err := pinPersonaState(packet, r.agent, r.conversation); err != nil {
				return turn.PreparedRequest{}, err
			}
			request, report := assemble.Assemble(assemble.Input{
				Core:    assemble.PersonaCoreV1 + "\n" + assemble.TUIFormatting,
				AgentID: r.agent, PersonaRevision: persona.Commit, Persona: persona.Layers,
				AgentName: persona.Name, OwnerPersonID: persona.OwnerPersonID,
				Catalog: catalog, AlwaysTools: declarations,
				History: items, Current: current[0], Packet: packet,
				Model: policy.Model, Effort: policy.Effort, MaxTokens: 128000,
			}, conversationRequestSize)
			if report.Failure != "" {
				if report.Failure == assemble.TooLarge {
					return turn.PreparedRequest{}, turn.ErrRequestTooLarge

				}
				return turn.PreparedRequest{}, turn.ErrInvalid
			}
			assembly := turn.Assembly{AgentID: r.agent, PersonaRevision: persona.Commit, WorkMethodRevision: persona.WorkMethodRevision, InstructionFormat: "persona-tui-v1", PreparedAt: at.Format(time.RFC3339Nano), Profile: append([]turn.Reference(nil), memoryRefs...), AgentName: persona.Name, OwnerPersonID: persona.OwnerPersonID, Items: append([]turn.Reference(nil), memoryRefs...)}
			assembly.Context, err = r.recordContextSources(ctx, packet, report)
			if err != nil {
				return turn.PreparedRequest{}, err
			}
			for _, event := range events {
				projected, err := assemble.History([]ledger.Event{event}, r.conversation, r.agent)
				if err != nil {
					return turn.PreparedRequest{}, err
				}
				if len(projected) == 0 {
					continue
				}
				digest := sha256.Sum256([]byte(event.Body.Text()))
				assembly.History = append(assembly.History, turn.Reference{ID: event.ID, Revision: hex.EncodeToString(digest[:])})
			}
			for _, ref := range report.Items {
				assembly.Items = append(assembly.Items, turn.Reference{ID: ref.ID, Revision: ref.Revision})
			}
			for _, ref := range report.Skills {
				assembly.Skills = append(assembly.Skills, turn.Reference{ID: ref.ID, Revision: ref.Revision})
			}
			return turn.PreparedRequest{Request: request, Assembly: assembly, Counter: conversationRequestSize, MaxTokens: 128000}, nil
		},
		Acknowledge: evidence.Acknowledge, RecordRequest: evidence.RecordRequest,
		Digest: assemble.Digest,
	}
	round := 0
	filters := map[string]*conversationReplyStream{}
	var completed []ledger.Admission
	var saved []ledger.Event
	complete := func(ctx context.Context, db turn.Canon) error {
		if promoted.ID != "" {
			now := time.Now()
			completed = append(completed, ledger.Admission{Entry: guard.EntrySystem, Input: ledger.Input{
				ID: "handoff:" + turnID, ConversationID: r.conversation, ParentID: input.ID, AgentID: r.agent, Surface: input.Surface,
				OccurredAt: now, RecordedAt: now, TimeQuality: "recorded", ScaffoldKind: "run_handoff",
				Body: design.Text(eventLanguage(input), "notice.handoff"),
			}})
		}
		if len(completed) == 0 {
			return db.Write(ctx, func(*canon.Tx) error { return nil })
		}
		// Adapt the existing filter's generated draft into the same batch as all
		// completed items. Turn completion, sources and cards commit together.
		var batch []ledger.Admission
		for _, item := range completed {
			batch = append(batch, item)
			masked, err := secrets.Mask(item.Input.Body)
			if err != nil {
				return err
			}
			filter, err := conversationFilter()
			if err != nil {
				return err
			}
			view, err := filter.Push(masked.Text())
			if err != nil {
				return err
			}
			if view.Card != nil {
				card := r.replyCard(item.Input.ID, turnID, view, eventLanguage(input))
				body, err := json.Marshal(card.Reference(item.Input.ID))
				if err != nil {
					return err
				}
				source := item.Input
				batch = append(batch, ledger.Admission{Entry: guard.EntrySystem, Input: ledger.Input{
					ID: card.ResultID, ParentID: source.ID, ConversationID: source.ConversationID, AgentID: source.AgentID,
					Surface: source.Surface, OccurredAt: source.OccurredAt, RecordedAt: source.RecordedAt, TimeQuality: source.TimeQuality,
					ScaffoldKind: "result", Body: string(body),
					Relations: []ledger.Relation{{TargetID: source.ID, Kind: "view_of"}},
				}})
			}
		}
		var err error
		saved, err = ledger.New(db, r.materials).AdmitBatch(ctx, batch)
		return err
	}
	inputQueue := &conversationInputs{runtime: r, inputs: inputs}
	out, err := turn.New(r.db).Run(ctx, turn.Options{ID: turnID, Conversation: r.conversation, InboxID: inboxID, InputEvent: input.ID, Preparation: prep, Complete: complete,
		StartFromTurn: func(ctx context.Context, h budget.Handoff) (budget.Handle, error) {
			var err error
			promoted, err = r.promoteRun(ctx, input, h)
			return promoted, err
		},
		ResultCommitted: r.background.notify, ToolActivity: r.toolActivity, Loop: loop.Config{
			RuntimeTools: r.route.Adapter.Class() == provider.AgentRuntime,
			Tools:        bindings,
			After: func(ctx context.Context, call provider.ToolCall, result loop.Result) loop.Result {
				return r.skillNotice(ctx, input, call, r.personaNotice(ctx, input, call, result))
			},
			Inputs: inputQueue,
			Stream: func(ctx context.Context, request provider.Request) (provider.Stream, error) {
				if inputQueue.failure != nil {
					return nil, inputQueue.failure
				}
				round++
				if err := r.setPhase(ctx, protocol.TurnPhaseWaitingModel); err != nil {
					return nil, err
				}
				r.dispatchMu.RLock()
				defer r.dispatchMu.RUnlock()
				// Never dispatch a cached preparation after a committed forget. This also
				// fences follow-up rounds; the existing turn flow records the refusal.
				if err := r.validateChannel(ctx); err != nil {
					return nil, err
				}
				if err := r.validateModelContext(ctx, memoryEpoch, memoryRefs); err != nil {
					return nil, err
				}
				if _, err := r.agents.Get(ctx, r.agent); err != nil {
					return nil, err
				}
				return r.route.Adapter.Stream(ctx, activity, request)
			},
			Observe: func(ctx context.Context, e provider.Event) error {
				key := fmt.Sprintf("%s:%d:%d", turnID, round, e.Index)
				switch e.Kind {
				case provider.Delta:
					if err := r.setPhase(ctx, protocol.TurnPhaseStreaming); err != nil {
						return err
					}
					filter := filters[key]
					if filter == nil {
						base, err := conversationFilter()
						if err != nil {
							return err
						}
						filter = &conversationReplyStream{filter: base}
						filters[key] = filter
					}
					view, delta, err := filter.Push(e.Delta)
					if err != nil {
						return err
					}
					if delta == "" {
						return nil
					}
					// Persist exactly the safe visible prefix before publishing it.
					// Deferred loop cleanup cannot run after SIGKILL.
					if err := turn.New(r.db).CheckpointPartial(ctx, turnID, turn.Partial{Round: round - 1, Index: e.Index, Text: filter.sent}); err != nil {
						return err
					}
					if view.Cut {
						// A result replaces the same stream row, retracting the unfinished
						// sentence while retaining all subsequent text in its preview.
						return r.publishReply(ctx, key, turnID, view, eventLanguage(input))
					}
					return r.publish(ctx, protocol.KindOutputDelta, key, delta)
				case provider.ItemDone:
					var item struct {
						Type, Role string
						Content    []struct{ Type, Text string }
					}
					if err := json.Unmarshal(e.Item, &item); err != nil {
						return err
					}
					if item.Type != "message" || item.Role != "assistant" {
						return nil
					}
					if err := r.setPhase(ctx, protocol.TurnPhaseStreaming); err != nil {
						return err
					}
					var text strings.Builder
					for _, part := range item.Content {
						if part.Type == "output_text" {
							text.WriteString(part.Text)
						}
					}
					now := time.Now()
					completed = append(completed, ledger.Admission{Entry: guard.EntryAgent, Input: ledger.Input{ID: key, ConversationID: r.conversation, ParentID: input.ID, AgentID: r.agent, Surface: input.Surface, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text.String()}})
					return nil
				case provider.ToolCallReady, provider.Terminal:
					return nil
				}
				return nil
			},
		}})
	if promoted.ID != "" {
		// The handle is durable before execution, and the run outlives this turn.
		err = errors.Join(err, r.background.service.Release(context.WithoutCancel(ctx), promoted.ID))
	}
	// Provider ItemDone stays provisional until completion and reply admission
	// commit together. Publication can be recovered from history after a crash.
	if err == nil {
		for _, item := range saved {
			if item.ScaffoldKind == "result" {
				continue
			}
			if item.SpeakerKind == guard.System {
				text, speaker := item.Body.Text(), protocol.ConversationSpeakerSystem
				err = r.publish(ctx, protocol.KindItemDone, item.ID, protocol.ItemDone{Text: &text, Speaker: &speaker})
				if err != nil {
					break
				}
				continue
			}
			filter, filterErr := conversationFilter()
			if filterErr != nil {
				err = filterErr
				break
			}
			view, filterErr := filter.Push(item.Body.Text())
			if filterErr != nil {
				err = filterErr
				break
			}
			if publishErr := r.publishReply(ctx, item.ID, turnID, view, eventLanguage(input)); publishErr != nil {
				err = publishErr
				break
			}
		}
	}
	// Late steering remains durable. A follow-up starts through the same lane and
	// turn store with the originating input's runner, retaining its channel scope
	// and binding checks even when the preceding turn began in the app.
	for _, body := range out.FollowUps {
		if pending, ok := r.takeSteering(body); ok {
			_, submitErr := r.queueReaction(r.ctx, r.conversation, func(ctx context.Context, inputs *loop.Inputs) error {
				return pending.runner.run(ctx, inputs, pending.event, pending.inboxID)
			})
			err = errors.Join(err, submitErr)
		}
	}
	if err != nil {
		// A stop or failure is published from its durable outcome. Seal the same
		// streamed block IDs before publishing the stored notice, matching
		// startup history and keeping partials outside future model context.
		stopped, readErr := turn.New(r.db).Stopped(context.WithoutCancel(ctx), input.ID)
		if readErr == nil && stopped.NoticeID != "" {
			if publishErr := r.publishPartials(ctx, stopped); publishErr != nil {
				return errors.Join(err, publishErr)
			}
			notice, noticeErr := r.journal().Get(context.WithoutCancel(ctx), stopped.NoticeID)
			if noticeErr != nil {
				return errors.Join(err, noticeErr)
			}
			text := notice.Body.Text()
			speaker := protocol.ConversationSpeakerSystem
			return errors.Join(err, r.publish(ctx, protocol.KindItemDone, notice.ID, protocol.ItemDone{Text: &text, Speaker: &speaker}))
		}
		err = errors.Join(err, readErr)
		text := design.Text(eventLanguage(input), "notice.failed")
		speaker := protocol.ConversationSpeakerSystem
		_ = r.publish(ctx, protocol.KindItemDone, turnID+":status", protocol.ItemDone{Text: &text, Speaker: &speaker})
	}
	return err
}

// Drained steering belongs to this turn; only the queue's final undrained
// items are resubmitted as follow-ups, with their original durable identity.
type conversationInputs struct {
	runtime *conversationRuntime
	inputs  *loop.Inputs
	failure error
}

func (q *conversationInputs) Drain() []provider.Item {
	items := q.inputs.Drain()
	for _, body := range items {
		if accepted, ok := q.runtime.takeSteering(body); ok && accepted.event.Surface.Kind == "channel" {
			if err := q.runtime.db.Write(q.runtime.ctx, func(tx *canon.Tx) error {
				_, err := tx.ExecContext(q.runtime.ctx, "INSERT INTO channel_steering(inbox_id) VALUES(?) ON CONFLICT DO NOTHING", accepted.inboxID)
				return err
			}); err != nil {
				q.failure = err
			}
		}
	}
	return items
}
func (q *conversationInputs) Close() []provider.Item { return q.inputs.Close() }
func (r *conversationRuntime) takeSteering(body provider.Item) (acceptedSteering, bool) {
	if r.coordinator != nil {
		return r.coordinator.takeSteering(body)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pending := r.steering[string(body)]
	if len(pending) == 0 {
		return acceptedSteering{}, false
	}
	first := pending[0]
	if len(pending) == 1 {
		delete(r.steering, string(body))
	} else {
		r.steering[string(body)] = pending[1:]
	}
	return first, true
}
func (r *conversationRuntime) verifyBuild(ctx context.Context) error {
	m, err := manifest.Read(filepath.Join(r.root, "system/bin/manifest.json"))
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	file, err := os.Open(executable)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, file)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	for _, binary := range m.Binaries {
		if binary.Name == "lina" && binary.Size == size && binary.SHA256 == hex.EncodeToString(digest.Sum(nil)) {
			return nil
		}
	}
	return errors.New("installed binary verification failed")
}
