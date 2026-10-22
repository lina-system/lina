// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

var ErrPendingLimit = errors.New("client pending subscription limit")
var ErrMalformed = errors.New("malformed client frame")

// ErrDisconnected marks a request whose transport failed and was closed. Its
// outcome is unknown: reconnecting must never automatically replay the request.
// Local validation, pending limits and caller cancellation do not carry it.
var ErrDisconnected = errors.New("client request disconnected")

// Subscribe sends one typed subscription/unsubscription and returns its message
// id. The correlated response and all subsequent notifications come through
// Receive, in wire order. At most 64 unanswered requests can be outstanding.
// This method may run concurrently with the single Receive consumer. It never
// retries, automatically resubscribes or discards another topic's notifications.
func (c *Client) Subscribe(ctx context.Context, subscription protocol.Subscribe) (string, error) {
	return c.request(ctx, protocol.KindSubscribe, subscription)
}

// Input submits one durable input. Receive owns its acknowledgement and streams.
func (c *Client) Input(ctx context.Context, input protocol.Input) (string, error) {
	return c.request(ctx, protocol.KindInput, input)
}

func (c *Client) Interrupt(ctx context.Context, interrupt protocol.Interrupt) (string, error) {
	return c.request(ctx, protocol.KindInterrupt, interrupt)
}

func (c *Client) History(ctx context.Context, history protocol.HistoryGet) (string, error) {
	return c.request(ctx, protocol.KindHistoryGet, history)
}

func (c *Client) request(ctx context.Context, kind protocol.Kind, payload protocol.Payload) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	id := rand.Text()
	envelope := protocol.Envelope{
		Protocol: protocol.Protocol{Name: "client", Version: c.Welcome.ProtocolVersion},
		Kind:     kind, MessageID: id, AttemptID: rand.Text(),
		Sender: protocol.Sender{Party: protocol.PartyClient, ID: c.clientID}, Epoch: c.Welcome.Epoch,
		SentAt: time.Now().UTC().Format(time.RFC3339Nano), EffectState: protocol.EffectNone,
		SourceRefs: []protocol.SourceRef{}, Payload: payload,
	}
	if _, err := json.Marshal(envelope); err != nil {
		return "", err
	}
	c.pendingMu.Lock()
	if len(c.pending) >= 64 {
		c.pendingMu.Unlock()
		return "", ErrPendingLimit
	}
	c.pending[id] = struct{}{}
	c.pendingMu.Unlock()
	request := struct {
		JSONRPC string            `json:"jsonrpc"`
		ID      string            `json:"id"`
		Method  protocol.Kind     `json:"method"`
		Params  protocol.Envelope `json:"params"`
	}{"2.0", id, kind, envelope}
	if err := c.writer.Write(ctx, request); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		c.Close() // A failed physical write may be partial.
		var network net.Error
		if ctx.Err() == nil && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) ||
			errors.Is(err, io.ErrShortWrite) || errors.Is(err, net.ErrClosed) || errors.As(err, &network)) {
			return "", fmt.Errorf("%w: %w", ErrDisconnected, err)
		}
		return "", err
	}
	return id, nil
}

// Receive returns the next validated reply or notification. One consumer owns
// receive ordering. Unknown kinds are ignored/counted by the generated boundary;
// malformed framing, uncorrelated replies and identity/epoch changes terminate
// the connection. Cancellation during a read closes it, preserving no partial
// frame for a later call. The handshake's reader is retained to avoid losing
// notifications received in the same socket read as welcome.
func (c *Client) Receive(ctx context.Context) (protocol.Envelope, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Envelope{}, err
	}
	c.receiveMu.Lock()
	defer c.receiveMu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.Envelope{}, err
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { c.Close(); close(interrupted) })
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	for {
		line, err := c.reader.Read()
		if err != nil {
			c.Close()
			if ctx.Err() != nil {
				return protocol.Envelope{}, ctx.Err()
			}
			return protocol.Envelope{}, err
		}
		envelope, err := c.decodeStream(line)
		if err != nil {
			c.Close()
			return protocol.Envelope{}, err
		}
		if envelope != nil {
			return *envelope, nil
		}
	}
}

func (c *Client) decodeStream(line []byte) (*protocol.Envelope, error) {
	if !json.Valid(line) {
		return nil, ErrMalformed
	}
	var frame struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      *string         `json:"id"`
		Method  string          `json:"method"`
		Result  json.RawMessage `json:"result"`
		Params  json.RawMessage `json:"params"`
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&frame); err != nil || frame.JSONRPC != "2.0" {
		return nil, ErrMalformed
	}
	notification := frame.ID == nil
	data := frame.Result
	if notification {
		if frame.Method == "" || frame.Result != nil || frame.Params == nil {
			return nil, ErrMalformed
		}
		data = frame.Params
	} else if frame.Method != "" || frame.Params != nil || frame.Result == nil {
		return nil, ErrMalformed
	}
	envelope, err := c.decoder.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if !notification {
		c.pendingMu.Lock()
		_, exists := c.pending[*frame.ID]
		delete(c.pending, *frame.ID)
		c.pendingMu.Unlock()
		if !exists {
			return nil, ErrMalformed
		}
	}
	if envelope == nil {
		return nil, nil
	}
	if envelope.Sender.Party != protocol.PartyCore || envelope.Sender.ID != c.Welcome.MainID || envelope.Epoch != c.Welcome.Epoch {
		return nil, ErrMalformed
	}
	if notification {
		if envelope.ReplyTo != nil || string(envelope.Kind) != frame.Method {
			return nil, ErrMalformed
		}
	} else if envelope.ReplyTo == nil || *envelope.ReplyTo != *frame.ID {
		return nil, ErrMalformed
	}
	return envelope, nil
}
