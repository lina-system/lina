// SPDX-License-Identifier: Apache-2.0

// Package clientsrv serves the empty Core's client 1.0 handshake.
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
	"slices"
	"sync"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

const (
	mainID        = "main-placeholder"
	epoch         = "0"
	personID      = "person-placeholder"
	maxFrameBytes = 1 << 20
)

// Listen creates a socket in a private directory. An existing directory must
// already be private; existing paths (including stale sockets) are preserved.
func Listen(path string) (*net.UnixListener, error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("socket directory must be a directory at mode 0700")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err != nil {
			return nil, fmt.Errorf("inspect socket path: %w", err)
		}
		return nil, fmt.Errorf("socket path already exists")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("set socket permissions: %w", err)
	}
	return listener, nil
}

// Serve owns the listener and all accepted connections. Cancellation closes
// blocked reads and writes, waits for handlers, and removes the socket.
func Serve(ctx context.Context, listener *net.UnixListener) error {
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
			serveConn(conn)
		}()
	}
}

type request struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id"`
	Method  string            `json:"method"`
	Params  protocol.Envelope `json:"params"`
}

type response struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id"`
	Result  protocol.Envelope `json:"result"`
}

func serveConn(conn net.Conn) {
	reader := bufio.NewScanner(conn)
	reader.Buffer(make([]byte, 4096), maxFrameBytes)
	writer := json.NewEncoder(conn)
	welcomed := false
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
		answer := reply(req.Params)
		closeAfter := false
		hello, isHello := req.Params.Payload.(protocol.Hello)
		switch {
		case welcomed:
			refuse(&answer, protocol.ErrorCapabilityNotNegotiated, "the empty Core grants no capabilities")
		case req.Method != "hello" || req.Params.Kind != protocol.KindHello || !isHello || req.Params.Sender.Party != protocol.PartyClient || req.Params.Sender.ID != hello.ID:
			refuse(&answer, protocol.ErrorMalformed, "expected a client hello request")
			closeAfter = true
		case !slices.Contains(hello.ProtocolVersions, "1.0"):
			refuse(&answer, protocol.ErrorUnsupportedCombination, "the empty Core supports client 1.0 only")
			closeAfter = true
		default:
			answer.Kind = protocol.KindWelcome
			answer.Payload = protocol.Welcome{
				ProtocolVersion: "1.0", Capabilities: []protocol.Capability{},
				MainID: mainID, Epoch: epoch, PersonID: personID, Agents: []protocol.Agent{},
			}
			welcomed = true
		}
		if err := writer.Encode(response{JSONRPC: "2.0", ID: req.ID, Result: answer}); err != nil || closeAfter {
			return
		}
	}
}

func reply(request protocol.Envelope) protocol.Envelope {
	return protocol.Envelope{
		Protocol:  protocol.Protocol{Name: "client", Version: "1.0"},
		MessageID: rand.Text(), AttemptID: rand.Text(), ReplyTo: &request.MessageID,
		Sender: protocol.Sender{Party: protocol.PartyCore, ID: mainID}, Epoch: epoch,
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
