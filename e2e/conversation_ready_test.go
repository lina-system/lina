// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"sync"

	"github.com/thisisjun786/lina/kit/protocol"
)

// startConversationTUI waits for the TUI's own conversation subscription. The
// shortcut footer and live connection status can precede history discovery.
// Observing this response proves the TUI has adopted the conversation id before
// the scenario submits input. The transparent proxy also forwards reconnects.
func (h *harness) startConversationTUI(environment []string, width, height int, conversation string) *terminal {
	h.t.Helper()
	target := h.socket
	socket := filepath.Join(filepath.Dir(target), "conversation-ready.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	ready, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		for {
			local, err := listener.Accept()
			if err != nil {
				return
			}
			remote, err := (&net.Dialer{}).DialContext(ctx, "unix", target)
			if err != nil {
				local.Close()
				continue
			}
			forwardConversation(ctx, local, remote, conversation, func() { once.Do(func() { close(ready) }) })
		}
	}()
	h.t.Cleanup(func() {
		cancel()
		listener.Close()
		<-done
	})
	h.socket = socket
	terminal := h.startTerminalSize(environment, false, true, width, height)
	h.socket = target
	select {
	case <-ready:
	case <-terminal.process.done:
		h.t.Fatalf("TUI exited before conversation discovery: %s", terminal.process.stderr.String())
	case <-h.ctx.Done():
		h.t.Fatal("TUI did not subscribe to its conversation", h.ctx.Err())
	}
	return terminal
}

func forwardConversation(ctx context.Context, local, remote net.Conn, conversation string, ready func()) {
	closeBoth := func() { local.Close(); remote.Close() }
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		_, _ = io.Copy(remote, local)
		closeBoth()
	}()
	defer func() { closeBoth(); <-inputDone }()
	reader := bufio.NewReader(remote)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var rpc struct{ Result json.RawMessage }
		if json.Unmarshal(line, &rpc) == nil && len(rpc.Result) > 0 {
			var decoder protocol.ClientDecoder
			if envelope, err := decoder.Decode(rpc.Result); err == nil && envelope != nil {
				if snapshot, ok := envelope.Payload.(protocol.Snapshot); ok && snapshot.Topic == "conversation:"+conversation {
					ready()
				}
			}
		}
		if _, err := local.Write(line); err != nil {
			return
		}
	}
}
