// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

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
