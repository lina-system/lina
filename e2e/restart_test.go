// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

func restartClient(h *harness, core *process) *client {
	h.t.Helper()
	conn := h.connect(core)
	c := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
	hello := clientHello([]string{"1.0"})
	hello.Capabilities = []protocol.Capability{{Name: "lifecycle", Version: "1.0"}}
	answer := c.call(h.t, protocol.KindHello, hello)
	welcome, ok := answer.Payload.(protocol.Welcome)
	if !ok || len(welcome.Capabilities) != 1 || welcome.Capabilities[0].Name != "lifecycle" {
		h.t.Fatalf("restart capability not negotiated: %+v", answer)
	}
	c.welcome = welcome
	h.inputRefused(c) // Wait for READY using an explicit protocol refusal.
	return c
}

func TestPlannedRestart(t *testing.T) {
	data, err := os.ReadFile("m1/restart.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Modes []string `json:"modes"`
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	binary := buildLina(t)
	for _, mode := range scenario.Modes {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, binary)
			core := h.startCore()
			c := restartClient(h, core)
			before := c.call(t, protocol.KindRestartGet, protocol.RestartGet{})
			status, ok := before.Payload.(protocol.RestartStatus)
			if !ok || status.Owed {
				t.Fatalf("initial status: %+v", before)
			}
			payload := protocol.RestartRequest{}
			if mode != "default" {
				m := protocol.RestartMode(mode)
				payload.Mode = &m
			}
			ack := c.call(t, protocol.KindRestartRequest, payload)
			pending, ok := ack.Payload.(protocol.RestartStatus)
			if !ok || !pending.Owed || pending.RequestID == nil || pending.RequestedEpoch == nil || *pending.RequestedEpoch != c.welcome.Epoch || ack.EffectState != protocol.EffectApplied || ack.ReceiptRef == nil || ack.ReceiptRef.ID != *pending.RequestID {
				t.Fatalf("durable acknowledgment: %+v", ack)
			}
			core.wait(t, 75)
			for _, path := range []string{h.socket, filepath.Join(h.root, "system/core/run.sentinel")} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("planned shutdown left %s: %v", filepath.Base(path), err)
				}
			}
			next := h.startCore()
			after := restartClient(h, next)
			oldEpoch, _ := strconv.ParseUint(c.welcome.Epoch, 10, 64)
			newEpoch, _ := strconv.ParseUint(after.welcome.Epoch, 10, 64)
			if newEpoch <= oldEpoch {
				t.Fatal("replacement epoch did not advance")
			}
			reply := after.call(t, protocol.KindRestartGet, protocol.RestartGet{})
			done, ok := reply.Payload.(protocol.RestartStatus)
			if !ok || done.Owed || done.RequestID == nil || *done.RequestID != *pending.RequestID || done.DischargedEpoch == nil || *done.DischargedEpoch != after.welcome.Epoch {
				t.Fatalf("replacement did not discharge: %+v", reply)
			}
			h.stopCore(next)
		})
	}
}
