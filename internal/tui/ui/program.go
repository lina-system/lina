// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Offer only rendered families; the conversation discovers and maintains its
// concrete conversation/results subscriptions after welcome.
var shellCapabilities = []protocol.Capability{{Name: "records", Version: "1.0"}, {Name: "worker_lanes", Version: "1.0"}, {Name: "web", Version: "1.0"}, {Name: "agents", Version: "1.0"}, {Name: "memory", Version: "1.0"}, {Name: "exchanges", Version: "1.0"}, {Name: "channels", Version: "1.0"}, {Name: "accounts", Version: "1.0"}, {Name: "sign_in_relay", Version: "1.0"}, {Name: "conversation", Version: "1.0"}, {Name: "turn_activity", Version: "1.0"}, {Name: "running", Version: "1.0"}, {Name: "progress", Version: "1.0"}, {Name: "waiting", Version: "1.0"}}

// shellClient is the request surface used by the shell and its sessions.
// Transport negotiation and validated receives remain in clientapi.
type shellClient interface {
	Close() error
	Subscribe(context.Context, protocol.Subscribe) (string, error)
	Agent(context.Context, protocol.AgentRequest) (string, error)
	History(context.Context, protocol.HistoryGet) (string, error)
	Talk(context.Context, protocol.AgentTalkGet) (string, error)
	ExpandTalk(context.Context, protocol.AgentTalkExpand) (string, error)
	Web(context.Context, protocol.WebRequest) (string, error)
	Channel(context.Context, protocol.ChannelRequest) (string, error)
	Input(context.Context, protocol.Input) (string, error)
	Interrupt(context.Context, protocol.Interrupt) (string, error)
	Account(context.Context, protocol.AccountRequest) (string, error)
	SignInListening(context.Context, protocol.SignInListening) (string, error)
	SignInRedirect(context.Context, protocol.SignInRedirect) (string, error)
	Control(context.Context, protocol.Payload) (string, error)
	Progress(context.Context, protocol.ProgressGet) (string, error)
	Waiting(context.Context, protocol.WaitingGet) (string, error)
	Running(context.Context, protocol.RunningGet) (string, error)
	Record(context.Context, protocol.RecordRequest) (string, error)
	Memory(context.Context, protocol.MemoryRequest) (string, error)
}

type clientPeer struct{ *clientapi.Client }

func (p clientPeer) Welcome() protocol.Welcome { return p.Client.Welcome }

var _ shellClient = clientPeer{}

type programEvent struct {
	message      tea.Msg
	acknowledged chan error
}

func runClientShell(parent context.Context, dial connectionDial, input io.Reader, display *display, terminal bool) (result error) {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(context.Canceled)
	model := newShell(environmentOptions(os.Getenv), time.Now)
	if display.interactive {
		model.styles = display.styles
		model.size.Width = display.styles.options.Width
	}
	defer func() {
		if display.renderer != nil {
			if err := display.renderer.close(); err != nil {
				result = errors.Join(result, err)
			}
			display.renderer = nil
		}
	}()
	events := make(chan programEvent, 64)
	if terminal {
		file := input.(terminalInput)
		inputDone := make(chan error, 1)
		go func() {
			err := readConversationInput(ctx, file, func(message tea.Msg) bool {
				select {
				case events <- programEvent{message: message, acknowledged: make(chan error, 1)}:
					return true
				case <-ctx.Done():
					return false
				}
			})
			if err != nil {
				cancel(err)
			} else {
				cancel(errQuit)
			}
			inputDone <- err
		}()
		defer func() {
			cancel(context.Canceled)
			if err := <-inputDone; err != nil {
				result = errors.Join(result, err)
			}
		}()
	}
	done := make(chan error, 1)
	go func() {
		done <- connectionLoop(ctx, dial, waitRetry, func(message tea.Msg) error {
			event := programEvent{message: message, acknowledged: make(chan error, 1)}
			select {
			case events <- event:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case err := <-event.acknowledged:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	joined := false
	defer func() {
		cancel(context.Canceled)
		if !joined {
			<-done
		}
	}()
	signin := &signInSession{ctx: ctx, model: model, remote: remoteTerminal(), open: openSignInBrowser, publish: func(flow context.Context, message tea.Msg) {
		select {
		case events <- programEvent{message: message, acknowledged: make(chan error, 1)}:
		case <-flow.Done():
		}
	}}
	defer signin.close()
	management := &managementSession{model: model, pending: make(map[string]protocol.Payload)}
	var peer connectionPeer
	var client shellClient
	pending := make(map[string]state.SendRequest)
	channelPending := make(map[string]uint64)
	webPending := make(map[string]uint64)
	talkPending := make(map[string]uint64)
	var agentListID, reconnectAgent, historyID string
	var stopInput func() error
	defer func() {
		if stopInput != nil {
			result = errors.Join(result, stopInput())
		}
	}()
	inputStarted := terminal
	var handle func(tea.Msg) error
	handle = func(message tea.Msg) error {
		if begin, ok := message.(shellBegin); ok && begin.Generation > model.generation {
			signin.close()
			clear(pending)
			clear(management.pending)
			clear(channelPending)
			clear(webPending)
			clear(talkPending)
			agentListID, reconnectAgent, historyID = "", "", ""
		}
		if attached, ok := message.(shellPeer); ok {
			peer = attached.Peer
			client, _ = peer.(shellClient)
			signin.peer = client
			w := peer.Welcome()
			// Welcome may advertise only the default. Retain the selected
			// display name while list/history discovery validates its identity.
			if selected := model.activeAgent(); model.conversation.agentPending && !slices.ContainsFunc(w.Agents, func(a protocol.Agent) bool { return a.AgentID == selected }) {
				for _, a := range model.content.Agents {
					if a.AgentID == selected {
						w.Agents = append(slices.Clone(w.Agents), a)
						break
					}
				}
			}
			envelope := protocol.Envelope{Protocol: protocol.Protocol{Name: "client", Version: "1.0"}, Kind: protocol.KindWelcome, MessageID: "local-welcome", AttemptID: "local-welcome", Sender: protocol.Sender{Party: protocol.PartyCore, ID: w.MainID}, Epoch: w.Epoch, SentAt: time.Now().UTC().Format(time.RFC3339Nano), EffectState: protocol.EffectNone, SourceRefs: []protocol.SourceRef{}, Payload: w}
			data, err := json.Marshal(envelope)
			if err != nil {
				return err
			}
			_, cmd := model.Update(shellData{Generation: attached.Generation, Message: state.Frame{Data: data}})
			if err := runShellCommand(cmd, handle); err != nil {
				return err
			}
			if model.hasCapability("conversation") || model.hasCapability("waiting") || model.hasCapability("running") || model.hasCapability("progress") {
				if err := handle(shellSubscription{Generation: attached.Generation, Request: protocol.Subscribe{Topic: stringPointer("core")}}); err != nil {
					return err
				}
				for _, cap := range w.Capabilities {
					switch cap.Name {
					case "waiting", "progress", "running":
						if err := handle(shellSubscription{Generation: attached.Generation, Request: protocol.Subscribe{Topic: stringPointer(cap.Name)}}); err != nil {
							return err
						}
					}
				}
			}
			if model.hasCapability("accounts") {
				if err := signin.start(protocol.AccountRequest{Operation: stringPointer("list")}); err != nil {
					return err
				}
			}
			if model.hasComposer() {
				if terminal && display.interactive && display.styles.options.Profile != Monochrome && display.renderer == nil {
					display.renderer = startTerminalRenderer(parent, rawTerminalWriter{display.output}, model.size.Width, model.size.Height, display.styles.options.Profile)
				}
				// Nonterminal input only reads quit after the handshake; it has
				// no draft destination to discover before reporting live.
				if client != nil && terminal {
					// Initial history can restore an agent absent from Welcome,
					// just as reconnect history can. Refresh names before either.
					if model.hasCapability("agents") && (model.conversation.agentPending || model.hasCapability("conversation")) {
						model.conversation.agentPending = true
						reconnectAgent = model.activeAgent()
						agentListID, err = client.Agent(ctx, protocol.AgentRequest{Operation: stringPointer("list")})
					} else if history := model.discoveryHistory(); history != nil && model.hasCapability("conversation") {
						reconnectAgent = model.activeAgent()
						historyID, err = client.History(ctx, *history)
					}
					if err != nil {
						return err
					}
				}
			}
			return nil
		}
		if request, ok := message.(shellManagement); ok {
			if request.Generation != model.generation {
				return nil
			}
			if client != nil {
				return management.request(ctx, client, request.Request)
			}
			return nil
		}
		if request, ok := message.(shellTalkRequest); ok {
			if request.Generation != model.generation || request.Attempt != model.talk.attempt {
				return nil
			}
			if client != nil {
				var id string
				var err error
				if request.ExchangeID == "" {
					id, err = client.Talk(ctx, protocol.AgentTalkGet{})
				} else {
					id, err = client.ExpandTalk(ctx, protocol.AgentTalkExpand{ExchangeID: &request.ExchangeID, Cursor: request.Cursor})
				}
				if err != nil {
					return err
				}
				talkPending[id] = request.Attempt
			}
			return display.showShell(model)
		}
		if request, ok := message.(shellWebRequest); ok {
			if request.Generation != model.generation || request.Attempt != model.web.attempt {
				return nil
			}
			if client != nil {
				id, err := client.Web(ctx, request.Request)
				if err != nil {
					return err
				}
				webPending[id] = request.Attempt
			}
			return display.showShell(model)
		}
		if request, ok := message.(shellChannelRequest); ok {
			if request.Generation != model.generation || request.Attempt != model.channels.attempt {
				return nil
			}
			if client != nil {
				id, err := client.Channel(ctx, request.Request)
				if err != nil {
					return err
				}
				channelPending[id] = request.Attempt
			}
			return display.showShell(model)
		}
		if consumed, err := signin.handle(message); consumed {
			if err != nil {
				return err
			}
			return display.showShell(model)
		}
		if effect, ok := message.(shellEffect); ok && effect.Generation == model.generation {
			if bell, ok := effect.Message.(state.CardBell); ok {
				return display.notifyCard(model, bell)
			}
			if client != nil {
				switch request := effect.Message.(type) {
				case state.SendRequest:
					if model.conversation.id == "" || model.conversation.agentMissing || model.conversation.agentPending {
						model.reduceInput(state.SendResult{Gen: request.Gen, Key: request.Key, Failed: true})
						return nil
					}
					mode := protocol.InputModeSteer
					if request.Mode == state.InputQueue {
						mode = protocol.InputModeQueue
					}
					id, err := client.Input(ctx, protocol.Input{ConversationID: &model.conversation.id, InputID: &request.Key, Text: &request.Draft.Text, Mode: &mode})
					if err != nil {
						model.reduceInput(state.SendResult{Gen: request.Gen, Key: request.Key, Failed: true})
						return err
					}
					pending[id] = request
				case state.InterruptRequest:
					_, err := client.Interrupt(ctx, protocol.Interrupt{ConversationID: &model.conversation.id})
					return err
				}
			}
		}
		if data, ok := message.(shellData); ok && data.Generation == model.generation {
			if frame, ok := data.Message.(state.Frame); ok {
				var decoder protocol.ClientDecoder
				e, err := decoder.Decode(frame.Data)
				if err == nil && e != nil {
					if e.ReplyTo != nil && *e.ReplyTo == historyID {
						historyID = ""
						if model.conversation.agentPending && model.activeAgent() == reconnectAgent && e.Error != nil {
							if model.discoveryFailed(e.Error.Code) {
								peer.Close()
							}
							return display.showShell(model)
						}
					}
					if e.ReplyTo != nil && *e.ReplyTo == agentListID {
						agentListID = ""
						if !model.conversation.agentPending || model.activeAgent() != reconnectAgent {
							return nil
						}
						p, ok := e.Payload.(protocol.AgentResult)
						if e.Error != nil {
							if model.discoveryFailed(e.Error.Code) {
								peer.Close()
							}
						} else if !ok || p.Agents == nil || p.Operation == nil || *p.Operation != "list" {
							return errStream
						} else {
							model.content.Agents = append([]protocol.Agent(nil), (*p.Agents)...)
							for i := range model.content.Agents {
								model.content.Agents[i].Name = sanitize.Display(model.content.Agents[i].Name)
							}
							if history := model.reconnectHistory(model.content.Agents); history != nil {
								if client != nil && model.hasCapability("conversation") {
									reconnectAgent = model.activeAgent()
									historyID, err = client.History(ctx, *history)
									if err != nil {
										return err
									}
								} else {
									model.agentDiscoveryDone()
								}
							}
						}
						return display.showShell(model)
					}
					if client != nil {
						if consumed, err := management.response(ctx, client, *e); consumed {
							if err != nil {
								return err
							}
							return display.showShell(model)
						}
					}
					if e.ReplyTo != nil {
						if attempt, ok := talkPending[*e.ReplyTo]; ok {
							delete(talkPending, *e.ReplyTo)
							if model.talk.open && attempt == model.talk.attempt {
								model.talk.busy = false
								if e.Error != nil {
									model.talk.code = string(e.Error.Code)
								} else if p, ok := e.Payload.(protocol.AgentTalk); ok {
									model.talkResponse(p)
								}
							}
							return display.showShell(model)
						}
					}
					if e.ReplyTo != nil {
						if attempt, ok := webPending[*e.ReplyTo]; ok {
							delete(webPending, *e.ReplyTo)
							if model.web.open && attempt == model.web.attempt {
								model.web.busy = false
								if e.Error != nil {
									model.web.code = string(e.Error.Code)
								} else if p, ok := e.Payload.(protocol.WebResult); ok {
									model.web.configured = p.Configured
								}
							}
							return display.showShell(model)
						}
					}
					if e.ReplyTo != nil {
						if attempt, ok := channelPending[*e.ReplyTo]; ok {
							delete(channelPending, *e.ReplyTo)
							if model.channels.open && attempt == model.channels.attempt {
								model.channels.busy = false
								if e.Error != nil {
									model.channels.code = string(e.Error.Code)
								} else if p, ok := e.Payload.(protocol.ChannelResult); ok {
									model.channelResponse(p)
								}
							}
							return display.showShell(model)
						}
					}
					if consumed, err := signin.response(*e); consumed {
						if err != nil {
							return err
						}
						return display.showShell(model)
					}
					if e.ReplyTo != nil {
						if sent, ok := pending[*e.ReplyTo]; ok {
							model.reduceInput(state.SendResult{Gen: sent.Gen, Key: sent.Key, Failed: e.Error != nil})
							delete(pending, *e.ReplyTo)
						}
					}
					if history, ok := e.Payload.(protocol.History); ok && history.ConversationID != nil {
						if model.conversation.agentMissing || model.conversation.id != "" && model.conversation.id != *history.ConversationID {
							return nil
						}
						model.conversation.id = *history.ConversationID
						model.agentDiscoveryDone()
						for _, topic := range []string{"conversation:" + model.conversation.id, "results:" + model.conversation.id} {
							if err := handle(shellSubscription{Generation: model.generation, Request: protocol.Subscribe{Topic: &topic}}); err != nil {
								return err
							}
						}
					}
				}
			}
		}
		if _, ok := message.(tea.QuitMsg); ok {
			cancel(errQuit)
			return nil
		}
		if request, ok := message.(shellSubscription); ok {
			if request.Generation != model.generation {
				return nil
			}
			if client, ok := peer.(interface {
				Subscribe(context.Context, protocol.Subscribe) (string, error)
			}); ok {
				_, err := client.Subscribe(ctx, request.Request)
				return err
			}
			return nil
		}
		if answer, ok := message.(shellCardAnswer); ok {
			if answer.Generation != model.generation || !model.mutationsAllowed() {
				return nil
			}
			client, ok := peer.(cardPeer)
			if !ok {
				return clientapi.ErrUnavailable
			}
			id, err := sendCard(ctx, client, answer.Request)
			if err != nil {
				return err
			}
			model.cardRequests[id] = answer.Request
			return nil
		}
		_, command := model.Update(message)
		if err := runShellCommand(command, handle); err != nil {
			return err
		}
		if !inputStarted && model.status.State == Live {
			inputStarted = true
			if file, ok := input.(*os.File); ok {
				inputContext, stop, err := watchTerminalInput(ctx, file)
				if err != nil {
					return err
				}
				forwarded := make(chan struct{})
				stopInput = func() error { err := stop(); <-forwarded; return err }
				go func() { defer close(forwarded); <-inputContext.Done(); cancel(context.Cause(inputContext)) }()
			} else {
				go func() {
					if err := readQuit(input); err != nil {
						cancel(err)
					} else {
						cancel(errQuit)
					}
				}()
			}
		}
		return display.showShell(model)
	}
	// A one-shot timer redraws the hint even when Core and input are idle.
	// Commands run synchronously here, so a tea.Tick command would block input.
	quitTimer := time.NewTimer(0)
	quitTimer.Stop()
	defer quitTimer.Stop()
	var activity activityClock
	defer activity.stop()
	for {
		if display.interactive {
			activity.sync(model.activityInterval())
		}
		var quitExpiry <-chan time.Time
		quitTimer.Stop()
		if !model.quitDeadline.IsZero() {
			quitTimer.Reset(time.Until(model.quitDeadline))
			quitExpiry = quitTimer.C
		}
		select {
		case <-quitExpiry:
			if err := handle(quitHintExpired{}); err != nil {
				return err
			}
		case now := <-activity.C:
			model.Update(activityTick(now))
			if err := display.showShell(model); err != nil {
				return err
			}
			activity.next()
		case <-ctx.Done():
			cause := context.Cause(ctx)
			if errors.Is(cause, errQuit) {
				return display.show(Quit)
			}
			return cause
		case err := <-done:
			joined = true
			if ctx.Err() != nil {
				if cause := context.Cause(ctx); errors.Is(cause, errQuit) {
					return display.show(Quit)
				}
				return context.Cause(ctx)
			}
			if err != nil && display.interactive {
				return &presentedError{err}
			}
			return err
		case event := <-events:
			err := handle(event.message)
			// A request write can notice a disconnect before Receive does.
			// clientapi has closed that peer; acknowledge this event so the
			// connection owner can publish its retry generation and restore
			// the view. Never replay the request or hide display/protocol errors.
			if ctx.Err() == nil && errors.Is(err, clientapi.ErrDisconnected) {
				err = nil
			}
			event.acknowledged <- err
			if err != nil {
				return err
			}
		}
	}
}

func stringPointer(s string) *string { return &s }
func runShellCommand(command tea.Cmd, handle func(tea.Msg) error) error {
	if command == nil {
		return nil
	}
	message := command()
	if batch, ok := message.(tea.BatchMsg); ok {
		for _, command := range batch {
			if err := runShellCommand(command, handle); err != nil {
				return err
			}
		}
		return nil
	}
	return handle(message)
}

func (d *display) showShell(model *shellModel) error {
	if d.renderer != nil {
		return d.renderer.show(model.View(), tea.WindowSizeMsg{Width: model.size.Width, Height: model.size.Height})
	}
	if d.interactive && d.styles.options.Profile != Monochrome {
		frame := model.View().Content + "\n"
		if frame == d.frame {
			return nil
		}
		output := frame
		if d.rows > 0 {
			output = ansi.CursorUp(d.rows) + ansi.EraseScreenBelow + output
		}
		if err := d.write(output); err != nil {
			return err
		}
		d.frame = frame
		d.rows = strings.Count(frame, "\n")
		d.last, d.shown = model.status, true
		return nil
	}
	// Notices are local, code-driven status, never ledger items or peer detail.
	for d.notices < len(model.content.Notices) {
		notice := model.content.Notices[d.notices]
		if d.interactive {
			if err := d.write(design.Text(model.styles.options.Language, notice.Code) + "\n"); err != nil {
				return err
			}
		}
		d.notices++
	}
	if err := d.showStatus(model.status); err != nil {
		return err
	}
	if !d.interactive || len(model.content.Blocks) == 0 && !model.hasComposer() && !model.accounts.open && model.quitHint() == "" && d.frame == "" {
		return nil
	}
	// Plain terminals already received each notice once above. Transcript
	// refreshes must not append that same explanation again.
	frame := model.viewContent(false)
	if frame == d.frame {
		return nil
	}
	if err := d.write(frame + "\n"); err != nil {
		return err
	}
	d.frame = frame
	return nil
}
