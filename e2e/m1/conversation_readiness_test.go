// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/protocol"
)

type scriptedAdmission struct {
	frames []protocol.Envelope
	inputs int
}

func (c *scriptedAdmission) Input(context.Context, protocol.Input) (string, error) {
	c.inputs++
	return fmt.Sprintf("probe%d", c.inputs), nil
}
func (c *scriptedAdmission) Receive(context.Context) (protocol.Envelope, error) {
	if len(c.frames) == 0 {
		return protocol.Envelope{}, io.EOF
	}
	frame := c.frames[0]
	c.frames = c.frames[1:]
	return frame, nil
}

func TestConversationReadinessCorrelatesReplies(t *testing.T) {
	reply := func(id string, code protocol.ErrorCode) protocol.Envelope {
		return protocol.Envelope{ReplyTo: &id, Error: &protocol.Error{Code: code}}
	}
	c := &scriptedAdmission{frames: []protocol.Envelope{
		{Kind: protocol.KindItemDone}, // recovered worker notification
		reply("unrelated", protocol.ErrorMalformed),
		reply("probe1", protocol.ErrorUnavailable),
		reply("probe1", protocol.ErrorMalformed), // late reply to an older probe
		{Kind: protocol.KindTurnState},
		reply("probe2", protocol.ErrorMalformed),
	}}
	if err := awaitConversationAdmission(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if c.inputs != 2 || len(c.frames) != 0 {
		t.Fatalf("admission used an unrelated frame: inputs=%d remaining=%d", c.inputs, len(c.frames))
	}
	if err := awaitConversationAdmission(t.Context(), &scriptedAdmission{}); !errors.Is(err, io.EOF) {
		t.Fatalf("missing readiness response passed: %v", err)
	}
}

// Subscribing after admission makes the first snapshot reflect recovered work
// and keeps live events out of the readiness probe's receive loop.
func TestConversationReadinessBeforeSubscription(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "")
	hub, err := clientsrv.NewTopics("1", clientsrv.TopicOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	listener, err := clientsrv.Listen(h.socket)
	if err != nil {
		t.Fatal(err)
	}
	capability := clientsrv.ConversationCapability
	rows := clientsrv.LocalCombinations()
	for i := range rows {
		rows[i].Capabilities = []protocol.Capability{capability}
	}
	conversation := "main:fixture-agent"
	var probes, snapshotProbes atomic.Int32
	identity := clientsrv.Identity{
		MainID: "fixture-core", Epoch: "1", Supported: rows, Topics: hub,
		TopicCapabilities: map[string]protocol.Capability{"conversation:": capability},
		RefreshTopic: func(_ context.Context, topic string) error {
			if topic != "conversation:"+conversation {
				return fmt.Errorf("unexpected topic %q", topic)
			}
			snapshotProbes.Store(probes.Load())
			return nil
		},
		Operations: map[protocol.Kind]clientsrv.Operation{
			protocol.KindHistoryGet: {Capability: capability, Handle: func(context.Context, clientsrv.Session, protocol.Envelope) (clientsrv.Result, error) {
				return clientsrv.Result{Kind: protocol.KindHistory, Payload: protocol.History{ConversationID: &conversation}}, nil
			}},
			protocol.KindInput: {Capability: capability, Handle: func(_ context.Context, _ clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
				if input, ok := e.Payload.(protocol.Input); !ok || input.ConversationID != nil || input.InputID != nil || input.Text != nil {
					t.Error("readiness probe must not submit a message")
				}
				code := protocol.ErrorMalformed
				if probes.Add(1) == 1 {
					code = protocol.ErrorUnavailable
				}
				return clientsrv.Result{}, &clientsrv.OperationError{Code: code}
			}},
		},
	}
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan error, 1)
	go func() { done <- clientsrv.Serve(ctx, listener, identity) }()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	c, got := conversationClient(t, h)
	c.Close()
	if got != conversation || probes.Load() != 2 || snapshotProbes.Load() != 2 {
		t.Fatalf("subscription preceded readiness: conversation=%q probes=%d snapshot_probes=%d", got, probes.Load(), snapshotProbes.Load())
	}
}
