// SPDX-License-Identifier: Apache-2.0

// Package clientsrv serves Core's client 1.0 handshake.
package clientsrv

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
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
	"github.com/thisisjun786/lina/kit/protocol"
)

const (
	personID      = "person-placeholder"
	maxFrameBytes = 1 << 20
)

// Identity is immutable for one boot; Recovering reports the admission state.
type Identity struct {
	MainID     string
	Epoch      string
	Recovering func() bool
	Restart    *lifecycle.Restarter
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
			serveConn(ctx, conn, identity)
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

func serveConn(ctx context.Context, conn net.Conn, identity Identity) {
	reader := bufio.NewScanner(conn)
	reader.Buffer(make([]byte, 4096), maxFrameBytes)
	writer := json.NewEncoder(conn)
	welcomed := false
	clientID := ""
	restartGranted := false
	for reader.Scan() {
		var req request
		if err := json.Unmarshal(reader.Bytes(), &req); err != nil || req.JSONRPC != "2.0" {
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
				refuse(&answer, protocol.ErrorUnsupportedCombination, "Core supports client 1.0 only")
				writer.Encode(response{JSONRPC: "2.0", ID: req.ID, Result: answer})
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
		hello, isHello := envelope.Payload.(protocol.Hello)
		switch {
		case welcomed && (envelope.Kind == protocol.KindRestartRequest || envelope.Kind == protocol.KindRestartGet):
			switch {
			case req.Method != string(envelope.Kind) || envelope.Sender.Party != protocol.PartyClient || envelope.Sender.ID != clientID:
				refuse(&answer, protocol.ErrorMalformed, "request identity or method differs from the connection")
			case envelope.Epoch != identity.Epoch:
				refuse(&answer, protocol.ErrorStaleEpoch, "request belongs to another Core epoch")
			case !restartGranted:
				refuse(&answer, protocol.ErrorCapabilityNotNegotiated, "lifecycle capability was not negotiated")
			default:
				finishReply = restartReply(ctx, identity, envelope, &answer)
			}
		case welcomed && identity.Recovering != nil && identity.Recovering():
			refuse(&answer, protocol.ErrorUnavailable, "Core is recovering")
		case welcomed:
			refuse(&answer, protocol.ErrorCapabilityNotNegotiated, "Core grants no capabilities")
		case req.Method != "hello" || envelope.Kind != protocol.KindHello || !isHello || envelope.Sender.Party != protocol.PartyClient || envelope.Sender.ID != hello.ID:
			refuse(&answer, protocol.ErrorMalformed, "expected a client hello request")
			closeAfter = true
		case !slices.Contains(hello.ProtocolVersions, "1.0"):
			refuse(&answer, protocol.ErrorUnsupportedCombination, "Core supports client 1.0 only")
			closeAfter = true
		default:
			caps := []protocol.Capability{}
			restartGranted = identity.Restart != nil && slices.Contains(hello.Capabilities, lifecycleCapability)
			if restartGranted {
				caps = append(caps, lifecycleCapability)
			}
			answer.Kind = protocol.KindWelcome
			answer.Payload = protocol.Welcome{
				ProtocolVersion: "1.0", Capabilities: caps,
				MainID: identity.MainID, Epoch: identity.Epoch, PersonID: personID, Agents: []protocol.Agent{},
			}
			welcomed = true
			clientID = hello.ID
		}
		err := writer.Encode(response{JSONRPC: "2.0", ID: req.ID, Result: answer})
		finishReply()
		if err != nil || closeAfter {
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
