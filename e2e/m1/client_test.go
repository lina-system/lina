// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

// This client crosses the public socket boundary and uses the generated wire
// types. It does not call Core handlers or inspect Core's in-memory state.
type client struct {
	conn    net.Conn
	reader  *bufio.Reader
	seq     int
	welcome protocol.Welcome
}

func (h *harness) client(core *process) *client {
	h.t.Helper()
	conn := h.connect(core)
	c := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
	answer := c.call(h.t, protocol.KindHello, clientHello([]string{"1.0"}))
	welcome, ok := answer.Payload.(protocol.Welcome)
	if !ok || answer.Kind != protocol.KindWelcome || answer.EffectState != protocol.EffectNone || answer.Error != nil {
		h.t.Fatalf("handshake: kind=%s effect=%s error=%v", answer.Kind, answer.EffectState, answer.Error)
	}
	if welcome.ProtocolVersion != "1.0" || welcome.MainID != "main-e2e" || welcome.Epoch == "0" ||
		welcome.Epoch != answer.Epoch || welcome.MainID != answer.Sender.ID || len(welcome.Capabilities) != 0 {
		h.t.Fatalf("unexpected handshake identity or capabilities: %+v", welcome)
	}
	c.welcome = welcome
	return c
}

func (c *client) call(t *testing.T, kind protocol.Kind, payload protocol.Payload) protocol.Envelope {
	t.Helper()
	return c.callWithin(t, kind, payload, operationTimeout)
}

func (c *client) callWithin(t *testing.T, kind protocol.Kind, payload protocol.Payload, timeout time.Duration) protocol.Envelope {
	t.Helper()
	c.seq++
	epoch := c.welcome.Epoch
	if epoch == "" {
		epoch = "0"
	}
	id := fmt.Sprintf("request-%d", c.seq)
	envelope := protocol.Envelope{
		Protocol: protocol.Protocol{Name: "client", Version: "1.0"},
		Kind:     kind, MessageID: id, AttemptID: id,
		Sender: protocol.Sender{Party: protocol.PartyClient, ID: "client-e2e"},
		Epoch:  epoch, SentAt: time.Now().UTC().Format(time.RFC3339Nano),
		EffectState: protocol.EffectNone, SourceRefs: []protocol.SourceRef{}, Payload: payload,
	}
	request := struct {
		JSONRPC string            `json:"jsonrpc"`
		ID      string            `json:"id"`
		Method  protocol.Kind     `json:"method"`
		Params  protocol.Envelope `json:"params"`
	}{"2.0", id, kind, envelope}
	if err := c.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(c.conn).Encode(request); err != nil {
		t.Fatalf("send %s: %v", kind, err)
	}
	// Require newline framing and bound allocations from an unhealthy peer.
	line, err := c.reader.ReadSlice('\n')
	if err != nil {
		t.Fatalf("receive %s: %v", kind, err)
	}
	answer, err := decodeReply(line, id)
	if err != nil {
		t.Fatalf("reply to %s: %v", kind, err)
	}
	if c.welcome.MainID != "" && (answer.Sender.ID != c.welcome.MainID || answer.Epoch != c.welcome.Epoch) {
		t.Fatal("reply changed Core identity or epoch")
	}
	return answer
}

func decodeReply(line []byte, requestID string) (protocol.Envelope, error) {
	var response struct {
		JSONRPC string            `json:"jsonrpc"`
		ID      string            `json:"id"`
		Result  protocol.Envelope `json:"result"`
		Error   json.RawMessage   `json:"error"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		return protocol.Envelope{}, err
	}
	answer := response.Result
	if response.JSONRPC != "2.0" || response.ID != requestID || response.Error != nil ||
		answer.ReplyTo == nil || *answer.ReplyTo != requestID || answer.Sender.Party != protocol.PartyCore {
		return protocol.Envelope{}, fmt.Errorf("uncorrelated or non-Core JSON-RPC response")
	}
	return answer, nil
}

func (h *harness) inputRefused(c *client) {
	h.t.Helper()
	h.inputRefusedWithin(c, operationTimeout)
}

func (h *harness) inputRefusedWithin(c *client, timeout time.Duration) {
	h.t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		answer := c.call(h.t, protocol.KindInput, protocol.Input{})
		if answer.Kind != protocol.KindError || answer.EffectState != protocol.EffectRefused || answer.Error == nil {
			h.t.Fatalf("input reply: kind=%s effect=%s error=%v", answer.Kind, answer.EffectState, answer.Error)
		}
		if answer.Error.Code == protocol.ErrorCapabilityNotNegotiated {
			return
		}
		if answer.Error.Code != protocol.ErrorUnavailable {
			h.t.Fatalf("input refusal = %s", answer.Error.Code)
		}
		// The socket also serves during RECOVERING. Only an explicit unavailable
		// refusal permits another fresh probe; an accepted effect is never retried.
		select {
		case <-deadline.C:
			h.t.Fatal("Core remained unavailable")
		case <-h.ctx.Done():
			h.t.Fatalf("Core readiness: %v", h.ctx.Err())
		default:
		}
	}
}

func clientHello(versions []string) protocol.Hello {
	peer, platform := "lina-tui", runtime.GOOS+"/"+runtime.GOARCH
	return protocol.Hello{Party: protocol.PartyClient, ID: "client-e2e", ProductVersion: "unrelated-product-version", ProtocolVersions: versions, Capabilities: []protocol.Capability{}, Peer: &peer, Platform: &platform}
}
