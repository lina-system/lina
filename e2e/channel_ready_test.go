// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"github.com/thisisjun786/lina/kit/protocol"
	"net"
	"testing"
	"time"
)

// Channel list is read-only but its shared management kind remains gated during
// recovery. A successful response proves readiness without a mutation or sleep
// guessed from the handshake, which intentionally permits recovery-time reads.
func waitChannelReady(t *testing.T, h *harness) {
	t.Helper()
	conn, err := (&net.Dialer{}).DialContext(h.ctx, "unix", h.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
	hello := clientHello([]string{"1.0"})
	hello.Capabilities = []protocol.Capability{{Name: "channels", Version: "1.0"}}
	reply := c.call(t, protocol.KindHello, hello)
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
	c.welcome = reply.Payload.(protocol.Welcome)
	operation := "list"
	for {
		reply = c.call(t, protocol.KindChannelRequest, protocol.ChannelRequest{Operation: &operation})
		if reply.Error == nil {
			return
		}
		if reply.Error.Code != protocol.ErrorUnavailable {
			t.Fatal(reply.Error)
		}
		select {
		case <-h.ctx.Done():
			t.Fatal("Core management never became ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
