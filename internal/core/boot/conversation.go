// SPDX-License-Identifier: Apache-2.0

package boot

import (
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
	"time"

	"github.com/thisisjun786/lina/internal/core/accounts"
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

// conversationRuntime is boot's composition of existing stores and flows. The
// model and all accepted work belong to Core, independent of client lifetime.
type acceptedSteering struct {
	event   ledger.Event
	inboxID int64
}

type conversationRuntime struct {
	materials                   *conversationMaterials
	assets                      *assets.Store
	root                        string
	steering                    map[string][]acceptedSteering
	ctx                         context.Context
	db                          *canon.Store
	accounts                    *accounts.Store
	vault                       *secrets.Store
	signin                      *accounts.SignIn
	agents                      *agents.Store
	growth                      *growthRuntime
	lanes                       *lane.Manager
	topics                      *clientsrv.Topics
	route                       *modelpath.ResponsesConfig
	activities                  *lifecycle.ActivityRegistry
	person, agent, conversation string
	identity                    clientsrv.Identity
	mu                          sync.Mutex
}

func conversationPath(root string) string { return filepath.Join(root, "canon", "canon.sqlite") }

func startConversation(ctx context.Context, root, main string, epoch uint64, controls *ctlfiles.Store, route *modelpath.ResponsesConfig, activities *lifecycle.ActivityRegistry, person string, transport http.RoundTripper) (*conversationRuntime, error) {
	if err := os.MkdirAll(filepath.Dir(conversationPath(root)), 0700); err != nil {
		return nil, err
	}
	db, err := canon.Open(ctx, canon.Config{Path: conversationPath(root), Epoch: epoch, Controls: controls, Migrations: ApplicationMigrations()})
	if err != nil {
		return nil, err
	}
	r := &conversationRuntime{root: root, steering: map[string][]acceptedSteering{}, ctx: ctx, db: db, route: route, activities: activities, lanes: lane.New(), person: "person:" + main}
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
	first, err := r.agents.FirstRun(ctx, r.person, true)
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

	if err = turn.New(db).Recover(ctx); err != nil {
		return nil, err
	}
	r.topics, err = clientsrv.NewTopics(strconv.FormatUint(epoch, 10), clientsrv.TopicOptions{})
	if err != nil {
		return nil, err
	}
	r.identity = clientsrv.Identity{PersonID: r.person, Agents: []protocol.Agent{{AgentID: r.agent, Name: first.Agent.Name}}, Operations: clientsrv.ConversationOperations(r), Topics: r.topics, TopicCapabilities: map[string]protocol.Capability{
		"core": clientsrv.ConversationCapability, "conversation:" + r.conversation: clientsrv.ConversationCapability, "results:" + r.conversation: clientsrv.ConversationCapability,
	}}
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
	if err = r.seedHistory(ctx); err != nil {
		return nil, err
	}
	if err = r.startGrowth(ctx, main, epoch, controls); err != nil {
		return nil, err
	}
	success = true
	return r, nil
}
func (r *conversationRuntime) Close(ctx context.Context) error {
	var err error
	if r.growth != nil && r.growth.stop != nil {
		r.growth.stop()
		select {
		case result := <-r.growth.done:
			err = errors.Join(err, result)
		case <-ctx.Done():
			return ctx.Err()
		}
		r.growth.stop = nil
	}
	if r.lanes != nil {
		err = r.lanes.Close(ctx)
		if err != nil {
			return err
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
	if r.agents != nil {
		err = errors.Join(err, r.agents.Close())
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
	admission := &conversationAdmission{db: r.db, input: inbox.Input{Key: inbox.Key{Conversation: r.conversation, Source: "client", NativeID: *p.InputID}, Body: []byte(masked.Text())}}
	now := time.Now()
	event, err := ledger.New(admission, r.materials).Admit(ctx, guard.EntryOwner, ledger.Input{ID: eventID, ConversationID: r.conversation, PersonID: r.person, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Locator: *p.InputID, Body: *p.Text})
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
	if err = r.publish(protocol.KindItemDone, eventID, answer); err != nil {
		return protocol.ItemDone{}, err
	}
	body, _ := json.Marshal(map[string]string{"role": "user", "content": event.Body.DerivedText()})
	if (p.Mode == nil || *p.Mode == protocol.InputModeSteer) && r.lanes.Steer(r.conversation, body) {
		r.steering[string(body)] = append(r.steering[string(body)], acceptedSteering{event, receipt.ID})
		return answer, nil
	}
	_, err = r.lanes.SubmitLoop(r.ctx, r.conversation, func(ctx context.Context, inputs *loop.Inputs) error { return r.run(ctx, inputs, event, receipt.ID) })
	return answer, err
}
func (r *conversationRuntime) Interrupt(ctx context.Context, s clientsrv.Session, p protocol.Interrupt) error {
	if err := r.authorize(s, p.ConversationID); err != nil {
		return err
	}
	r.lanes.Stop(r.conversation, turn.Interrupt{Issuer: turn.Person})
	return nil
}
func (r *conversationRuntime) publish(kind protocol.Kind, key string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return r.topics.Publish("conversation:"+r.conversation, clientsrv.TopicEvent{Kind: kind, Key: key, Data: data, AgentID: r.agent})
}
func (r *conversationRuntime) setActive(active bool) error {
	return r.publish(protocol.KindTurnState, "turn", protocol.TurnState{Active: &active})
}

func (r *conversationRuntime) events(ctx context.Context, through string) ([]ledger.Event, error) {
	var ids []string
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		query := `SELECT id FROM conv_event WHERE conversation_id=? AND (speaker_kind IN ('person','agent') OR scaffold_kind IN ('interruption','failure'))`
		args := []any{r.conversation}
		if through != "" {
			query += ` AND (speaker_kind<>'person' OR rowid <= (SELECT rowid FROM conv_event WHERE id=?))`
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
	events, err := r.events(ctx, "")
	if err != nil {
		return err
	}
	for _, e := range events {
		if e.ScaffoldKind == "interruption" || e.ScaffoldKind == "failure" {
			stopped, err := turn.New(r.db).Stopped(ctx, e.ParentID)
			if err != nil {
				return err
			}
			if err = r.publishPartials(stopped); err != nil {
				return err
			}
		}
		text := e.Body.Text()
		speaker := protocol.ConversationSpeaker(e.SpeakerKind)
		p := protocol.ItemDone{Text: &text, Speaker: &speaker}
		if e.SpeakerKind == guard.Person {
			key := e.Origin.Locator
			p.EchoKey = &key
			p.PersonID = &r.person
		}
		if err = r.publish(protocol.KindItemDone, e.ID, p); err != nil {
			return err
		}
	}
	return r.setActive(false)
}

func (r *conversationRuntime) publishPartials(stopped turn.Stopped) error {
	for _, p := range stopped.Partials {
		// The provider observer numbers rounds from one, the loop from zero.
		key := fmt.Sprintf("%s:%d:%d", stopped.TurnID, p.Round+1, p.Index)
		speaker := protocol.ConversationSpeakerAgent
		if err := r.publish(protocol.KindItemDone, key, protocol.ItemDone{Text: &p.Text, Speaker: &speaker}); err != nil {
			return err
		}
	}
	return nil
}

func (r *conversationRuntime) run(ctx context.Context, inputs *loop.Inputs, input ledger.Event, inboxID int64) (err error) {
	turnID := "turn:" + input.ID
	if err = r.setActive(true); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, r.setActive(false)) }()
	activity, err := r.activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "conversation"})
	if err != nil {
		return err
	}
	defer activity.Release()
	var items []provider.Item
	evidence := turn.EvidenceWriter{Ledger: r.journal(), ConversationID: r.conversation, ParentID: input.ID, Surface: ledger.Surface{Kind: "tui"}, Now: time.Now}
	prep := &turn.Preparation{
		VerifyBuild: r.verifyBuild,
		Account:     func(context.Context) (provider.Credential, error) { return r.route.Request(nil).Credential, nil },
		Resolve: func(context.Context) (turn.ModelPolicy, error) {
			return turn.ModelPolicy{Model: r.route.Model, Revision: "configured", Class: provider.ModelAPI}, nil
		},
		Context: func(ctx context.Context, _ turn.ModelPolicy) (turn.PreparedRequest, error) {
			persona, err := r.agents.Prepare(ctx, turnID, r.agent)
			if err != nil {
				return turn.PreparedRequest{}, err
			}
			events, err := r.events(ctx, input.ID)
			if err != nil {
				return turn.PreparedRequest{}, err
			}
			items, err = assemble.History(events, r.conversation, r.agent)
			if err != nil {
				return turn.PreparedRequest{}, err
			}
			request := r.route.Request(items)
			request.Instructions, err = assemble.Instructions(persona, request.Tools)
			if err != nil {
				return turn.PreparedRequest{}, err
			}
			return turn.PreparedRequest{Request: request, Assembly: turn.Assembly{AgentID: r.agent, PersonaRevision: persona.Commit}, Counter: func(request provider.Request) int {
				n := len(request.Instructions)
				for _, item := range request.Items {
					n += len(item)
				}
				for _, tool := range request.Tools {
					n += len(tool)
				}
				return n
			}, MaxTokens: 128000}, nil
		},
		Acknowledge: evidence.Acknowledge, RecordRequest: evidence.RecordRequest,
		Digest: assemble.Digest,
	}
	round := 0
	var completed []ledger.Admission
	var saved []ledger.Event
	complete := func(ctx context.Context, db turn.Canon) error {
		if len(completed) == 0 {
			return db.Write(ctx, func(*canon.Tx) error { return nil })
		}
		var err error
		saved, err = ledger.New(db, r.materials).AdmitBatch(ctx, completed)
		return err
	}
	out, err := turn.New(r.db).Run(ctx, turn.Options{ID: turnID, Conversation: r.conversation, InboxID: inboxID, InputEvent: input.ID, Preparation: prep, Complete: complete, Loop: loop.Config{
		Inputs: &conversationInputs{runtime: r, inputs: inputs},
		Stream: func(ctx context.Context, request provider.Request) (provider.Stream, error) {
			round++
			return r.route.Adapter.Stream(ctx, activity, request)
		},
		Observe: func(ctx context.Context, e provider.Event) error {
			key := fmt.Sprintf("%s:%d:%d", turnID, round, e.Index)
			switch e.Kind {
			case provider.Delta:
				return r.publish(protocol.KindOutputDelta, key, e.Delta)
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
				var text strings.Builder
				for _, part := range item.Content {
					if part.Type == "output_text" {
						text.WriteString(part.Text)
					}
				}
				now := time.Now()
				completed = append(completed, ledger.Admission{Entry: guard.EntryAgent, Input: ledger.Input{ID: key, ConversationID: r.conversation, ParentID: input.ID, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text.String()}})
				return nil
			case provider.ToolCallReady, provider.Terminal:
				return nil
			}
			return nil
		},
	}})
	// Provider ItemDone stays provisional until completion and reply admission
	// commit together. Publication can be recovered from history after a crash.
	if err == nil {
		for _, item := range saved {
			value := item.Body.Text()
			speaker := protocol.ConversationSpeakerAgent
			if publishErr := r.publish(protocol.KindItemDone, item.ID, protocol.ItemDone{Text: &value, Speaker: &speaker}); publishErr != nil {
				err = publishErr
				break
			}
		}
	}
	// Late steering remains durable. A follow-up starts through the same lane and
	// turn store, never by retrying a completed model request.
	for _, body := range out.FollowUps {
		if pending, ok := r.takeSteering(body); ok {
			_, submitErr := r.lanes.SubmitLoop(r.ctx, r.conversation, func(ctx context.Context, inputs *loop.Inputs) error {
				return r.run(ctx, inputs, pending.event, pending.inboxID)
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
			if publishErr := r.publishPartials(stopped); publishErr != nil {
				return errors.Join(err, publishErr)
			}
			notice, noticeErr := r.journal().Get(context.WithoutCancel(ctx), stopped.NoticeID)
			if noticeErr != nil {
				return errors.Join(err, noticeErr)
			}
			text := notice.Body.Text()
			speaker := protocol.ConversationSpeakerSystem
			return errors.Join(err, r.publish(protocol.KindItemDone, notice.ID, protocol.ItemDone{Text: &text, Speaker: &speaker}))
		}
		err = errors.Join(err, readErr)
		text := "The reply could not be completed. Send a new message to continue."
		_ = r.publish(protocol.KindItemDone, turnID+":status", protocol.ItemDone{Text: &text})
	}
	return err
}

// Drained steering belongs to this turn; only the queue's final undrained
// items are resubmitted as follow-ups, with their original durable identity.
type conversationInputs struct {
	runtime *conversationRuntime
	inputs  *loop.Inputs
}

func (q *conversationInputs) Drain() []provider.Item {
	items := q.inputs.Drain()
	for _, body := range items {
		q.runtime.takeSteering(body)
	}
	return items
}
func (q *conversationInputs) Close() []provider.Item { return q.inputs.Close() }
func (r *conversationRuntime) takeSteering(body provider.Item) (acceptedSteering, bool) {
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
