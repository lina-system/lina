// SPDX-License-Identifier: Apache-2.0

// Package clientapi owns the typed client connection to Core. It has no access
// to Core state and never starts Core or retries a request.
package clientapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

const maxFrameBytes = 1 << 20

// ErrUnavailable means the client could not open the supplied socket.
var ErrUnavailable = errors.New("Core unavailable")

// Refusal preserves the Core's typed cause. Detail is diagnostic data, not
// terminal output; Error deliberately returns only the schema's closed code.
type Refusal struct {
	Cause protocol.Error
}

func (r *Refusal) Error() string { return string(r.Cause.Code) }

// Client is a welcomed connection. Its owner must Close it on every exit path.
// Welcome is the negotiated identity and capability snapshot for later screens.
type Client struct {
	conn    net.Conn
	Welcome protocol.Welcome
}

func (c *Client) Close() error { return c.conn.Close() }

// Dial connects once and completes the client 1.0 handshake. This increment
// offers no capabilities. The timeout bounds both connecting and negotiation;
// the caller's earlier deadline or cancellation takes precedence.
func Dial(ctx context.Context, socket, productVersion string) (*Client, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	client, err := handshake(ctx, conn, productVersion)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return client, nil
}

func handshake(ctx context.Context, conn net.Conn, productVersion string) (*Client, error) {
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		conn.SetDeadline(time.Now())
		close(interrupted)
	})
	defer func() {
		if stop != nil && !stop() {
			<-interrupted
		}
	}()
	clientID := rand.Text()
	hello := protocol.Envelope{
		Protocol: protocol.Protocol{Name: "client", Version: "1.0"},
		Kind:     protocol.KindHello, MessageID: rand.Text(), AttemptID: rand.Text(),
		Sender: protocol.Sender{Party: protocol.PartyClient, ID: clientID}, Epoch: "0",
		SentAt: time.Now().UTC().Format(time.RFC3339Nano), EffectState: protocol.EffectNone,
		SourceRefs: []protocol.SourceRef{},
		Payload: protocol.Hello{
			Party: protocol.PartyClient, ID: clientID, ProductVersion: productVersion,
			ProtocolVersions: []string{"1.0"}, Capabilities: []protocol.Capability{},
		},
	}
	request := struct {
		JSONRPC string            `json:"jsonrpc"`
		ID      string            `json:"id"`
		Method  string            `json:"method"`
		Params  protocol.Envelope `json:"params"`
	}{"2.0", hello.MessageID, "hello", hello}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return nil, fmt.Errorf("send hello: %w", err)
	}
	// Require the newline and cap allocation even when a peer never sends it.
	line, err := bufio.NewReaderSize(conn, maxFrameBytes).ReadSlice('\n')
	if err != nil {
		return nil, fmt.Errorf("read handshake: %w", err)
	}
	var response struct {
		JSONRPC string            `json:"jsonrpc"`
		ID      string            `json:"id"`
		Result  protocol.Envelope `json:"result"`
		Error   json.RawMessage   `json:"error"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		return nil, fmt.Errorf("decode handshake: %w", err)
	}
	envelope := response.Result
	if response.JSONRPC != "2.0" || response.ID != request.ID || response.Error != nil ||
		envelope.ReplyTo == nil || *envelope.ReplyTo != hello.MessageID || envelope.Sender.Party != protocol.PartyCore {
		return nil, errors.New("invalid handshake response")
	}
	if envelope.Kind == protocol.KindError && envelope.EffectState == protocol.EffectRefused && envelope.Error != nil {
		return nil, &Refusal{Cause: *envelope.Error}
	}
	welcome, ok := envelope.Payload.(protocol.Welcome)
	if !ok || envelope.Kind != protocol.KindWelcome || envelope.Error != nil || envelope.EffectState != protocol.EffectNone ||
		welcome.ProtocolVersion != "1.0" || welcome.MainID != envelope.Sender.ID || welcome.Epoch != envelope.Epoch || len(welcome.Capabilities) != 0 {
		return nil, errors.New("invalid welcome")
	}
	// Stop and join cancellation before clearing the handshake deadline. A
	// successful connection must not inherit a timer that can later expire it.
	if !stop() {
		<-interrupted
	}
	stop = nil
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return &Client{conn: conn, Welcome: welcome}, nil
}
