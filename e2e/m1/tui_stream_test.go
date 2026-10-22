// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/fakecore"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIStream(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("tui-stream.json")
	if err != nil {
		t.Fatal(err)
	}
	var script struct {
		Topic, Text string
		Steps       []fakecore.Step
	}
	if err := json.Unmarshal(data, &script); err != nil {
		t.Fatal(err)
	}
	fixtures := tuiFixtures(t)
	load := func(name string) protocol.Envelope {
		var e protocol.Envelope
		if err := json.Unmarshal(fixtures[name].Data, &e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	put := func(name string, e protocol.Envelope) {
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		fixtures[name] = &fstest.MapFile{Data: data}
	}
	e := load("welcome.json")
	welcome := e.Payload.(protocol.Welcome)
	welcome.Capabilities = []protocol.Capability{{Name: "progress", Version: "1.0"}}
	e.Payload = welcome
	put("welcome.json", e)
	e = load("streams/subscribe.json")
	coreTopic := "core"
	e.Payload = protocol.Subscribe{Topic: &coreTopic}
	put("subscribe-core.json", e)
	e.Payload = protocol.Subscribe{Topic: &script.Topic}
	put("subscribe-topic.json", e)
	e = load("normal/resync.json")
	r := e.Payload.(protocol.Resync)
	r.Topic = script.Topic
	e.Payload = r
	put("resync-topic.json", e)
	e = load("normal/snapshot.json")
	s := e.Payload.(protocol.Snapshot)
	s.Topic = script.Topic
	body, err := json.Marshal(protocol.ItemDone{Text: &script.Text})
	if err != nil {
		t.Fatal(err)
	}
	s.Items[0].Payload = body
	e.Payload = s
	put("snapshot-topic.json", e)
	s.Topic, s.Items = coreTopic, []protocol.StreamItem{}
	e.Payload = s
	put("snapshot-core.json", e)
	h := newHarness(t, buildLina(t))
	if err := os.MkdirAll(filepath.Dir(h.socket), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", h.socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	core, err := fakecore.New(fixtures, &scenarioClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, script.Steps)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			err = core.Run(ctx, conn)
		}
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		err := <-done
		if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
			t.Errorf("stream peer: %v", err)
		}
	})
	terminal := h.startTUI("TERM=xterm-256color", "NO_COLOR=1", "LC_ALL=C")
	expectCatalogState(t, h, terminal, design.English, "live")
	expectCatalogState(t, h, terminal, design.English, "recovering")
	expectCatalogState(t, h, terminal, design.English, "resyncing")
	for {
		select {
		case line, ok := <-terminal.lines:
			if !ok {
				t.Fatal("terminal ended before snapshot text")
			}
			if strings.Contains(line, script.Text) {
				goto displayed
			}
		case <-h.ctx.Done():
			t.Fatal("snapshot did not reach terminal")
		}
	}
displayed:
	h.keys(terminal, "q")
	expectCatalogState(t, h, terminal, design.English, "quit")
	terminal.process.wait(t, 0)
	for range terminal.lines {
	}
	h.expectTerminalRestored(terminal)
}
