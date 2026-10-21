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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
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
	root                        string
	steering                    map[string][]acceptedSteering
	ctx                         context.Context
	db                          *canon.Store
	agents                      *agents.Store
	lanes                       *lane.Manager
	topics                      *clientsrv.Topics
	route                       *modelpath.ResponsesConfig
	activities                  *lifecycle.ActivityRegistry
	person, agent, conversation string
	identity                    clientsrv.Identity
	mu                          sync.Mutex
}

func conversationPath(root string) string { return filepath.Join(root, "canon", "canon.sqlite") }

func startConversation(ctx context.Context, root, main string, epoch uint64, controls *ctlfiles.Store, route *modelpath.ResponsesConfig, activities *lifecycle.ActivityRegistry) (*conversationRuntime, error) {
	migrations := canon.Migrations()
	for _, schema := range []string{turn.Schema, turn.ControlSchema, turn.OutcomeSchema} {
		migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: schema})
	}
	db, err := canon.Open(ctx, canon.Config{Path: conversationPath(root), Epoch: epoch, Controls: controls, Migrations: migrations})
	if err != nil {
		return nil, err
	}
	r := &conversationRuntime{root: root, steering: map[string][]acceptedSteering{}, ctx: ctx, db: db, route: route, activities: activities, lanes: lane.New(), person: "person:" + main}
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
	if err = r.seedHistory(ctx); err != nil {
		return nil, err
	}
	success = true
	return r, nil
}
func (r *conversationRuntime) Close(ctx context.Context) error {
	var err error
	if r.lanes != nil {
		err = r.lanes.Close(ctx)
		if err != nil {
			return err
		}
	}
	if r.topics != nil {
		r.topics.Close()
	}
	if r.agents != nil {
		err = errors.Join(err, r.agents.Close())
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

type conversationTx struct{ tx *canon.Tx }

func (t conversationTx) Write(_ context.Context, f func(*canon.Tx) error) error { return f(t.tx) }
func (t conversationTx) Read(_ context.Context, f func(*canon.Tx) error) error  { return f(t.tx) }

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
	var receipt inbox.Receipt
	var event ledger.Event
	err = r.db.Write(ctx, func(tx *canon.Tx) error {
		var err error
		receipt, err = inbox.Insert(ctx, tx, inbox.Input{Key: inbox.Key{Conversation: r.conversation, Source: "client", NativeID: *p.InputID}, Body: []byte(masked.Text())})
		if err != nil {
			return err
		}
		journal := ledger.New(conversationTx{tx}, nil)
		if !receipt.Inserted {
			event, err = journal.Get(ctx, eventID)
			return err
		}
		now := time.Now()
		event, err = journal.Admit(ctx, guard.EntryOwner, ledger.Input{ID: eventID, ConversationID: r.conversation, PersonID: r.person, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Locator: *p.InputID, Body: *p.Text})
		return err
	})
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
		query := `SELECT id FROM conv_event WHERE conversation_id=? AND (speaker_kind IN ('person','agent') OR scaffold_kind='interruption')`
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
	journal := ledger.New(r.db, nil)
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
	evidence := turn.EvidenceWriter{Ledger: ledger.New(r.db, nil), ConversationID: r.conversation, ParentID: input.ID, Surface: ledger.Surface{Kind: "tui"}, Now: time.Now}
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
			request.Instructions = strings.Join([]string{persona.Layers.Core, persona.Layers.DefaultVoice, persona.Layers.UserVoice, persona.Layers.GrowthSnapshot}, "\n")
			return turn.PreparedRequest{Request: request, Assembly: turn.Assembly{AgentID: r.agent, PersonaRevision: persona.Commit}, Counter: func(request provider.Request) int {
				n := len(request.Instructions)
				for _, item := range request.Items {
					n += len(item)
				}
				return n
			}, MaxTokens: 128000}, nil
		},
		Acknowledge: evidence.Acknowledge, RecordRequest: evidence.RecordRequest,
		Digest: func(request provider.Request) string {
			data, _ := json.Marshal(struct {
				Model        provider.ModelRef
				Instructions string
				Items        []provider.Item
			}{request.Model, request.Instructions, request.Items})
			digest := sha256.Sum256(data)
			return hex.EncodeToString(digest[:])
		},
	}
	round := 0
	out, err := turn.New(r.db).Run(ctx, turn.Options{ID: turnID, Conversation: r.conversation, InboxID: inboxID, InputEvent: input.ID, Preparation: prep, Loop: loop.Config{
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
				saved, err := ledger.New(r.db, nil).Admit(ctx, guard.EntryAgent, ledger.Input{ID: key, ConversationID: r.conversation, ParentID: input.ID, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text.String()})
				if err != nil {
					return err
				}
				value := saved.Body.Text()
				speaker := protocol.ConversationSpeakerAgent
				return r.publish(protocol.KindItemDone, key, protocol.ItemDone{Text: &value, Speaker: &speaker})
			case provider.ToolCallReady, provider.Terminal:
				return nil
			}
			return nil
		},
	}})
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
		text := "The reply could not be completed. Send a new message to continue."
		var interrupted turn.Interrupt
		if errors.As(err, &interrupted) {
			text = interrupted.Text()
		}
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
