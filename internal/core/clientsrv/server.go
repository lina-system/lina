// SPDX-License-Identifier: Apache-2.0

// Package clientsrv serves Core's client 1.0 handshake.
package clientsrv

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
)

const personID = "person-placeholder"

// Identity is immutable for one boot; Recovering reports the admission state.
type Identity struct {
	// DecisionPresence receives authenticated connection facts, never payload claims.
	DecisionPresence func(Session, bool)
	// RefreshTopic projects current authoritative records before a subscription.
	// It is called only after topic admission and never for unsubscribe.
	RefreshTopic func(context.Context, string) error
	PersonID     string
	Agents       []protocol.Agent
	Operations   map[protocol.Kind]Operation
	MainID       string
	Epoch        string
	Recovering   func() bool
	Restart      *lifecycle.Restarter
	// Supported is the build's complete table; a non-nil empty table denies all
	// combinations. Peer and platform always come from hello.
	Supported []Combination
	// Topics is the current epoch's projection. TopicCapabilities lists the
	// installed topic families and the capability each requires. Conversation
	// and result families use "conversation:" and "results:" keys.
	Topics            *Topics
	TopicCapabilities map[string]protocol.Capability
	// AuthorizeTopic rechecks dynamic ownership/visibility within an installed family.
	AuthorizeTopic func(context.Context, string) bool
}

// Listen creates a socket at mode 0600 in a private directory. An existing
// directory must already be private; existing paths are preserved.
func Listen(path string) (*net.UnixListener, error) {
	return ListenConfig{}.Listen(path)
}

type ListenConfig struct {
	// AfterBind observes the socket immediately after binding, before any
	// subsequent setup. It supports deterministic crash tests, not CLI options.
	AfterBind func() error
}

func (cfg ListenConfig) Listen(path string) (*net.UnixListener, error) {
	if err := PrepareSocketDirectory(path); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err != nil {
			return nil, fmt.Errorf("inspect socket path: %w", err)
		}
		return nil, fmt.Errorf("socket path already exists")
	}
	listener, err := listenPrivate(path)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	if cfg.AfterBind != nil {
		if err := cfg.AfterBind(); err != nil {
			listener.Close()
			return nil, err
		}
	}
	return listener, nil
}

// PrepareSocketDirectory creates and validates the private parent of a socket.
// Boot also calls it before reclaiming a previous socket, so an untrusted
// directory is refused before any socket entry is removed.
func PrepareSocketDirectory(path string) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	return validateSocketDirectory(info)
}

func validateSocketDirectory(info os.FileInfo) error {
	if err := ctlfiles.ValidatePrivateDirectory(info); err != nil {
		return fmt.Errorf("socket directory: %w", err)
	}
	return nil
}

func inspectClient(ctx context.Context, listener net.Listener, conn net.Conn) (peerIdentity, error) {
	if tailnet, ok := listener.(*TailnetListener); ok {
		return tailnet.inspect(ctx, conn)
	}
	if local, ok := conn.(*net.UnixConn); ok {
		return inspectPeer(local)
	}
	return peerIdentity{}, errors.New("client transport has no identity authority")
}

// Serve owns the listener and all accepted connections. Cancellation closes
// blocked reads and writes, waits for handlers, and removes the socket.
func Serve(ctx context.Context, listener net.Listener, identity Identity) error {
	ctx, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	connections := make(map[net.Conn]struct{})
	var handlers sync.WaitGroup
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		<-ctx.Done()
		listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for conn := range connections {
			conn.Close()
		}
	}()
	defer func() {
		cancel()
		<-closed
		handlers.Wait()
	}()
	if identity.Supported != nil {
		if err := manifest.ValidateCombinations(identity.Supported); err != nil {
			return err
		}
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept client: %w", err)
		}
		mu.Lock()
		if ctx.Err() != nil {
			mu.Unlock()
			conn.Close()
			return nil
		}
		connections[conn] = struct{}{}
		mu.Unlock()
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer func() {
				conn.Close()
				mu.Lock()
				delete(connections, conn)
				mu.Unlock()
			}()
			peer, err := inspectClient(ctx, listener, conn)
			if err != nil {
				return
			}
			servePeer(ctx, conn, identity, peer)
		}()
	}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id"`
	Result  protocol.Envelope `json:"result"`
}

func servePeer(ctx context.Context, conn net.Conn, identity Identity, peer peerIdentity) {
	serveClientTransport(ctx, conn, identity, peer)
}

func serveMessages(ctx context.Context, conn rpc.Transport, identity Identity, peer peerIdentity) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if identity.Supported == nil {
		identity.Supported = LocalCombinations()
	}
	// The generated codec currently speaks only client 1.0. A manifest row
	// cannot make the binary implement another version.
	identity.Supported = slices.DeleteFunc(slices.Clone(identity.Supported), func(row Combination) bool { return row.ProtocolVersion != "1.0" })
	if identity.PersonID == "" {
		identity.PersonID = personID
	}
	if identity.Agents == nil {
		identity.Agents = []protocol.Agent{}
	}
	epoch, err := EpochNumber(identity.Epoch)
	if err != nil || epoch == 0 {
		return
	}
	c := clientConnection{
		identity: identity,
		peer:     peer,
		session:  Session{ConnectionID: rand.Text(), PersonID: identity.PersonID, Epoch: epoch, Capabilities: []protocol.Capability{}},
	}
	welcomed := false
	// Observe EOF while an operation is blocked in an issuer exchange. A
	// connection that goes away must cancel that exchange before admission.
	frames := make(chan []byte)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer close(frames)
		for {
			line, err := conn.Read(ctx)
			if err != nil {
				cancel()
				return
			}
			select {
			case frames <- slices.Clone(line):
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); conn.Close(); <-readDone }()
	defer func() {
		cancel()
		session := c.session
		if identity.DecisionPresence != nil {
			identity.DecisionPresence(session, false)
		}
		for _, operation := range identity.Operations {
			if operation.Disconnect != nil {
				operation.Disconnect(session)
			}
		}
	}()
	legacy := legacyActivity{}
	topics := newTopicSession(ctx, identity, identity.Topics,
		func(id json.RawMessage, envelope protocol.Envelope) error {
			if !slices.Contains(c.session.Capabilities, TurnActivityCapability) {
				envelope = legacy.project(envelope)
			}
			if id != nil {
				return conn.Write(ctx, response{JSONRPC: "2.0", ID: id, Result: envelope})
			}
			return conn.Write(ctx, struct {
				JSONRPC string            `json:"jsonrpc"`
				Method  protocol.Kind     `json:"method"`
				Params  protocol.Envelope `json:"params"`
			}{"2.0", envelope.Kind, envelope})
		},
		func(topic string) bool {
			capability, installed := identity.topicCapability(topic)
			return installed && slices.Contains(c.session.Capabilities, capability) && (identity.AuthorizeTopic == nil || identity.AuthorizeTopic(ctx, topic))
		}, func() { conn.Close() })
	defer func() { conn.Close(); topics.Close() }()
	for {
		var line []byte
		select {
		case <-ctx.Done():
			return
		case next, ok := <-frames:
			if !ok {
				return
			}
			line = next
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil || req.JSONRPC != "2.0" {
			return
		}
		var id any
		if err := json.Unmarshal(req.ID, &id); err != nil {
			return
		}
		switch id.(type) {
		case string, float64:
		default:
			return
		}
		if !welcomed {
			if messageID, unsupported := unsupportedHello(req.Method, req.Params); unsupported {
				answer := reply(protocol.Envelope{MessageID: messageID}, identity)
				refuse(&answer, protocol.ErrorUnsupportedCombination, "no supported client combination")
				conn.Write(ctx, response{JSONRPC: "2.0", ID: req.ID, Result: answer})
				return
			}
		}
		var envelope protocol.Envelope
		if err := json.Unmarshal(req.Params, &envelope); err != nil {
			return
		}
		answer := reply(envelope, identity)
		closeAfter := false
		finishReply := func() {}
		switch {
		case req.Method != string(envelope.Kind):
			refuse(&answer, protocol.ErrorMalformed, "method and kind differ")
			closeAfter = !welcomed
		case welcomed && !c.admit(envelope, &answer):
			// Admission has already populated the refusal.
		case welcomed && (envelope.Kind == protocol.KindRestartRequest || envelope.Kind == protocol.KindRestartGet):
			if identity.Restart == nil || !slices.Contains(c.session.Capabilities, lifecycleCapability) {
				refuse(&answer, protocol.ErrorCapabilityNotNegotiated, "lifecycle capability was not negotiated")
			} else {
				finishReply = restartReply(ctx, identity, envelope, &answer)
			}
		case welcomed:
			if handled, err := c.dispatch(ctx, topics, req.ID, envelope, &answer); handled {
				if err != nil {
					return
				}
				continue
			}
		default:
			welcomed = c.negotiateHello(envelope, &answer)
			closeAfter = !welcomed
			if welcomed && identity.DecisionPresence != nil {
				identity.DecisionPresence(c.facts(), true)
			}
		}
		err = conn.Write(ctx, response{JSONRPC: "2.0", ID: req.ID, Result: answer})
		finishReply()
		if err != nil || closeAfter {
			return
		}
	}
}

// clientConnection retains authenticated connection facts across message stages.
// Decider eligibility is refreshed where the listener previously checked it.
type clientConnection struct {
	identity Identity
	peer     peerIdentity
	session  Session
}

func (c *clientConnection) facts() Session {
	session := c.session
	session.CanDecide = c.peer.canDecide()
	return session
}

// negotiateHello runs only before welcome and after method/kind validation.
func (c *clientConnection) negotiateHello(envelope protocol.Envelope, answer *protocol.Envelope) bool {
	hello, isHello := envelope.Payload.(protocol.Hello)
	if envelope.Kind != protocol.KindHello || !isHello || envelope.Sender.Party != protocol.PartyClient || envelope.Sender.ID != hello.ID {
		refuse(answer, protocol.ErrorMalformed, "expected a client hello request")
		return false
	}
	var selected string
	var granted []protocol.Capability
	var supported bool
	if hello.Peer != nil && hello.Platform != nil {
		selected, granted, supported = negotiate(c.identity.Supported, *hello.Peer, *hello.Platform, hello.ProtocolVersions, hello.Capabilities)
	}
	granted = slices.DeleteFunc(granted, func(capability protocol.Capability) bool {
		if capability == lifecycleCapability && c.identity.Restart != nil {
			return false
		}
		if c.identity.Topics != nil {
			for _, installed := range c.identity.TopicCapabilities {
				if installed == capability {
					return false
				}
			}
		}
		for _, operation := range c.identity.Operations {
			if operation.Handle != nil && (operation.Capability == capability || slices.Contains(operation.AdditionalCapabilities, capability)) {
				return false
			}
		}
		return true
	})
	if !supported {
		refuse(answer, protocol.ErrorUnsupportedCombination, "no supported client combination")
		return false
	}
	answer.Kind = protocol.KindWelcome
	answer.Payload = protocol.Welcome{
		ProtocolVersion: selected, Capabilities: granted,
		MainID: c.identity.MainID, Epoch: c.identity.Epoch, PersonID: c.identity.PersonID, Agents: c.identity.Agents,
	}
	c.session.ClientID = hello.ID
	c.session.Capabilities = granted
	return true
}

// admit preserves the order of guards for every request after welcome.
func (c *clientConnection) admit(envelope protocol.Envelope, answer *protocol.Envelope) bool {
	switch {
	case envelope.Sender.Party != protocol.PartyClient || envelope.Sender.ID != c.session.ClientID:
		refuse(answer, protocol.ErrorNotAuthorized, "client identity changed")
	case envelope.Epoch != c.identity.Epoch:
		refuse(answer, protocol.ErrorStaleEpoch, "request epoch differs from Core")
	case c.identity.Recovering != nil && c.identity.Recovering() && !readOnlyEnvelope(envelope):
		refuse(answer, protocol.ErrorUnavailable, "Core is recovering")
	case deciderKinds[envelope.Kind] && !c.peer.canDecide():
		refuse(answer, protocol.ErrorNotAuthorized, "client is not an eligible decider")
	default:
		return true
	}
	return false
}

// dispatch runs only for admitted, non-lifecycle requests. A handled topic
// writes its own response; otherwise answer is populated for the message loop.
func (c *clientConnection) dispatch(ctx context.Context, topics *topicSession, id json.RawMessage, envelope protocol.Envelope, answer *protocol.Envelope) (bool, error) {
	if handled, err := topics.handle(id, envelope); handled {
		return true, err
	}
	operation, exists := c.identity.Operations[envelope.Kind]
	required := operation.Capability
	if operation.CapabilityFor != nil {
		required = operation.CapabilityFor(envelope)
	}
	if !exists || operation.Handle == nil || !slices.Contains(c.session.Capabilities, required) {
		refuse(answer, protocol.ErrorCapabilityNotNegotiated, "capability not negotiated")
	} else {
		result, err := operation.Handle(ctx, c.facts(), envelope)
		if err != nil {
			code := protocol.ErrorUnavailable
			var failure *OperationError
			if errors.As(err, &failure) {
				code = failure.Code
			}
			refuse(answer, code, string(code))
		} else {
			answer.Kind, answer.Payload = result.Kind, result.Payload
			if result.Effect != "" {
				answer.EffectState = result.Effect
			}
		}
	}
	return false, nil
}

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// Recognize an unsupported hello before the version-specific decoder rejects
// it. Only inspect the negotiation fields; an unknown version may add fields.
func unsupportedHello(method string, data json.RawMessage) (string, bool) {
	var offer struct {
		Protocol  protocol.Protocol `json:"protocol"`
		Kind      protocol.Kind     `json:"kind"`
		MessageID string            `json:"message_id"`
		Sender    protocol.Sender   `json:"sender"`
		Payload   struct {
			Party            protocol.Party `json:"party"`
			ID               string         `json:"id"`
			ProtocolVersions []string       `json:"protocol_versions"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(data, &offer); err != nil {
		return "", false
	}
	if method != "hello" || offer.Kind != protocol.KindHello || offer.Protocol.Name != "client" ||
		offer.Protocol.Version == "1.0" || !versionPattern.MatchString(offer.Protocol.Version) ||
		offer.MessageID == "" || offer.Sender.Party != protocol.PartyClient ||
		offer.Payload.Party != protocol.PartyClient || offer.Payload.ID == "" || offer.Sender.ID != offer.Payload.ID ||
		len(offer.Payload.ProtocolVersions) == 0 {
		return "", false
	}
	for _, version := range offer.Payload.ProtocolVersions {
		if !versionPattern.MatchString(version) {
			return "", false
		}
	}
	return offer.MessageID, true
}

func reply(request protocol.Envelope, identity Identity) protocol.Envelope {
	return protocol.Envelope{
		Protocol:  protocol.Protocol{Name: "client", Version: "1.0"},
		MessageID: rand.Text(), AttemptID: rand.Text(), ReplyTo: &request.MessageID,
		Sender: protocol.Sender{Party: protocol.PartyCore, ID: identity.MainID}, Epoch: identity.Epoch,
		SentAt: time.Now().UTC().Format(time.RFC3339Nano), EffectState: protocol.EffectNone,
		SourceRefs: []protocol.SourceRef{},
	}
}

func refuse(answer *protocol.Envelope, code protocol.ErrorCode, detail string) {
	answer.Kind = protocol.KindError
	answer.EffectState = protocol.EffectRefused
	answer.Error = &protocol.Error{Code: code, Detail: detail}
	answer.Payload = protocol.ErrorPayload{}
}
