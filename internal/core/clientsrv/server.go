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
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
)

const personID = "person-placeholder"

// Identity is immutable for one boot; Recovering reports the admission state.
type Identity struct {
	PersonID   string
	Agents     []protocol.Agent
	Operations map[protocol.Kind]Operation
	MainID     string
	Epoch      string
	Recovering func() bool
	// Supported is the build's complete table; a non-nil empty table denies all
	// combinations. Peer and platform always come from hello.
	Supported []Combination
	// Topics is the current epoch's projection. TopicCapabilities lists the
	// installed topic families and the capability each requires. Conversation
	// and result families use "conversation:" and "results:" keys.
	Topics            *Topics
	TopicCapabilities map[string]protocol.Capability
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

// Serve owns the listener and all accepted connections. Cancellation closes
// blocked reads and writes, waits for handlers, and removes the socket.
func Serve(ctx context.Context, listener *net.UnixListener, identity Identity) error {
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
		conn, err := listener.AcceptUnix()
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
			peer, err := inspectPeer(conn)
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	connectionID := rand.Text()
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
	reader := rpc.NewReader(conn)
	welcomed := false
	clientID := ""
	capabilities := []protocol.Capability{}
	// Observe EOF while an operation is blocked in an issuer exchange. A
	// connection that goes away must cancel that exchange before admission.
	frames := make(chan []byte)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer close(frames)
		for {
			line, err := reader.Read()
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
		session := Session{ConnectionID: connectionID, PersonID: identity.PersonID, Epoch: epoch, ClientID: clientID, Capabilities: capabilities}
		for _, operation := range identity.Operations {
			if operation.Disconnect != nil {
				operation.Disconnect(session)
			}
		}
	}()
	writer := rpc.NewWriter(conn)
	topics := newTopicSession(ctx, identity, identity.Topics,
		func(id json.RawMessage, envelope protocol.Envelope) error {
			if id != nil {
				return writer.Write(ctx, response{JSONRPC: "2.0", ID: id, Result: envelope})
			}
			return writer.Write(ctx, struct {
				JSONRPC string            `json:"jsonrpc"`
				Method  protocol.Kind     `json:"method"`
				Params  protocol.Envelope `json:"params"`
			}{"2.0", envelope.Kind, envelope})
		},
		func(topic string) bool {
			capability, installed := identity.topicCapability(topic)
			return installed && slices.Contains(capabilities, capability)
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
				writer.Write(ctx, response{JSONRPC: "2.0", ID: req.ID, Result: answer})
				return
			}
		}
		var envelope protocol.Envelope
		if err := json.Unmarshal(req.Params, &envelope); err != nil {
			return
		}
		answer := reply(envelope, identity)
		closeAfter := false
		hello, isHello := envelope.Payload.(protocol.Hello)
		var selected string
		var granted []protocol.Capability
		var supported bool
		if hello.Peer != nil && hello.Platform != nil {
			selected, granted, supported = negotiate(identity.Supported, *hello.Peer, *hello.Platform, hello.ProtocolVersions, hello.Capabilities)
		}
		granted = slices.DeleteFunc(granted, func(capability protocol.Capability) bool {
			if identity.Topics != nil {
				for _, installed := range identity.TopicCapabilities {
					if installed == capability {
						return false
					}
				}
			}
			for _, operation := range identity.Operations {
				if operation.Handle != nil && operation.Capability == capability {
					return false
				}
			}
			return true
		})
		switch {
		case req.Method != string(envelope.Kind):
			refuse(&answer, protocol.ErrorMalformed, "method and kind differ")
			closeAfter = !welcomed
		case welcomed && (envelope.Sender.Party != protocol.PartyClient || envelope.Sender.ID != clientID):
			refuse(&answer, protocol.ErrorNotAuthorized, "client identity changed")
		case welcomed && envelope.Epoch != identity.Epoch:
			refuse(&answer, protocol.ErrorStaleEpoch, "request epoch differs from Core")
		case welcomed && (envelope.Kind == protocol.KindApprovalAnswer || envelope.Kind == protocol.KindQuestionAnswer || envelope.Kind == protocol.KindWaitingAnswer) && !peer.canDecide():
			refuse(&answer, protocol.ErrorNotAuthorized, "client is not an eligible decider")
		case welcomed && identity.Recovering != nil && identity.Recovering() && !readOnlyKinds[envelope.Kind]:
			refuse(&answer, protocol.ErrorUnavailable, "Core is recovering")
		case welcomed:
			if handled, err := topics.handle(req.ID, envelope); handled {
				if err != nil {
					return
				}
				continue
			}
			operation, exists := identity.Operations[envelope.Kind]
			if !exists || operation.Handle == nil || !slices.Contains(capabilities, operation.Capability) {
				refuse(&answer, protocol.ErrorCapabilityNotNegotiated, "capability not negotiated")
			} else {
				session := Session{ConnectionID: connectionID, PersonID: identity.PersonID, Epoch: epoch, ClientID: clientID, CanDecide: peer.canDecide(), Capabilities: capabilities}
				result, err := operation.Handle(ctx, session, envelope)
				if err != nil {
					code := protocol.ErrorUnavailable
					var failure *OperationError
					if errors.As(err, &failure) {
						code = failure.Code
					}
					refuse(&answer, code, string(code))
				} else {
					answer.Kind, answer.Payload = result.Kind, result.Payload
					if result.Effect != "" {
						answer.EffectState = result.Effect
					}
				}
			}
		case req.Method != "hello" || envelope.Kind != protocol.KindHello || !isHello || envelope.Sender.Party != protocol.PartyClient || envelope.Sender.ID != hello.ID:
			refuse(&answer, protocol.ErrorMalformed, "expected a client hello request")
			closeAfter = true
		case !supported:
			refuse(&answer, protocol.ErrorUnsupportedCombination, "no supported client combination")
			closeAfter = true
		default:
			answer.Kind = protocol.KindWelcome
			answer.Payload = protocol.Welcome{
				ProtocolVersion: selected, Capabilities: granted,
				MainID: identity.MainID, Epoch: identity.Epoch, PersonID: identity.PersonID, Agents: identity.Agents,
			}
			welcomed = true
			clientID = hello.ID
			capabilities = granted
		}
		if err := writer.Write(ctx, response{JSONRPC: "2.0", ID: req.ID, Result: answer}); err != nil || closeAfter {
			return
		}
	}
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
