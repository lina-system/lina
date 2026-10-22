// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/thisisjun786/lina/internal/fakecore"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Bind generated idempotency keys to fixture names without changing request
// text, mode, conversation, RPC ids or correlation. Fake Core validates those.
type conversationScriptConn struct {
	net.Conn
	reader   *bufio.Reader
	buffered *bytes.Reader
	keys     map[string]string
}

func (c *conversationScriptConn) Read(p []byte) (int, error) {
	if c.buffered != nil && c.buffered.Len() > 0 {
		return c.buffered.Read(p)
	}
	data, err := c.reader.ReadBytes('\n')
	if err != nil {
		return 0, err
	}
	var frame struct {
		JSONRPC string            `json:"jsonrpc"`
		ID      json.RawMessage   `json:"id"`
		Method  string            `json:"method"`
		Params  protocol.Envelope `json:"params"`
	}
	if err = json.Unmarshal(data, &frame); err != nil {
		return 0, err
	}
	if input, ok := frame.Params.Payload.(protocol.Input); ok && input.InputID != nil {
		key := fmt.Sprintf("input-%d", len(c.keys)+1)
		c.keys[key] = *input.InputID
		input.InputID = &key
		frame.Params.Payload = input
		data, err = json.Marshal(frame)
		if err != nil {
			return 0, err
		}
		data = append(data, '\n')
	}
	c.buffered = bytes.NewReader(data)
	return c.buffered.Read(p)
}
func (c *conversationScriptConn) Write(data []byte) (int, error) {
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(data, &frame); err != nil {
		return 0, err
	}
	for _, field := range []string{"result", "params"} {
		if raw := frame[field]; raw != nil {
			var envelope protocol.Envelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				return 0, err
			}
			if item, ok := envelope.Payload.(protocol.ItemDone); ok && item.EchoKey != nil {
				if actual, ok := c.keys[*item.EchoKey]; ok {
					item.EchoKey = &actual
					envelope.Payload = item
					frame[field], _ = json.Marshal(envelope)
				}
			}
		}
	}
	encoded, err := json.Marshal(frame)
	if err != nil {
		return 0, err
	}
	_, err = c.Conn.Write(append(encoded, '\n'))
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

func TestM1TUIConversationStream(t *testing.T) {
	data, err := os.ReadFile("m1/tui-conversation-stream.json")
	if err != nil {
		t.Fatal(err)
	}
	var script struct {
		Conversation, Input, Steering, Partial, Reply string
		Steps                                         []fakecore.Step
	}
	if err = json.Unmarshal(data, &script); err != nil {
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
	welcome := load("welcome.json")
	w := welcome.Payload.(protocol.Welcome)
	w.Capabilities = []protocol.Capability{{Name: "conversation", Version: "1.0"}}
	welcome.Payload = w
	fixtures["welcome.json"] = &fstest.MapFile{Data: mustJSON(t, welcome)}
	put := func(name string, kind protocol.Kind, payload protocol.Payload, client bool) {
		e := welcome
		if client {
			e = load("hello.json")
		}
		e.Kind = kind
		e.Payload = payload
		e.ReplyTo = nil
		e.Error = nil
		fixtures[name] = &fstest.MapFile{Data: mustJSON(t, e)}
	}
	topic := "conversation:" + script.Conversation
	for _, part := range []struct{ name, topic string }{{"core", "core"}, {"conversation", topic}, {"results", "results:" + script.Conversation}} {
		put(part.name+"-request.json", protocol.KindSubscribe, protocol.Subscribe{Topic: &part.topic}, true)
		put(part.name+"-snapshot.json", protocol.KindSnapshot, protocol.Snapshot{Topic: part.topic, Cursor: protocol.StreamCursor{Epoch: w.Epoch, Seq: "0"}, RevocationEpoch: "0", Items: []protocol.StreamItem{}}, false)
	}
	put("history-request.json", protocol.KindHistoryGet, protocol.HistoryGet{}, true)
	put("history.json", protocol.KindHistory, protocol.History{ConversationID: &script.Conversation}, false)
	metadata := func(key string, seq int) *protocol.StreamMetadata {
		return &protocol.StreamMetadata{Topic: topic, Key: key, Cursor: protocol.StreamCursor{Epoch: w.Epoch, Seq: json.Number(fmt.Sprint(seq))}, Revision: json.Number(fmt.Sprint(seq))}
	}
	mode := protocol.InputModeSteer
	speaker := protocol.ConversationSpeakerPerson
	for i, part := range []struct {
		name, text string
		seq        int
	}{{"first", script.Input, 1}, {"steering", script.Steering, 4}} {
		key := fmt.Sprintf("input-%d", i+1)
		put(part.name+"-request.json", protocol.KindInput, protocol.Input{ConversationID: &script.Conversation, Text: &part.text, InputID: &key, Mode: &mode}, true)
		item := protocol.ItemDone{Text: &part.text, EchoKey: &key, Speaker: &speaker}
		put(part.name+"-ack.json", protocol.KindItemDone, item, false)
		item.Stream = metadata(key, part.seq)
		put(part.name+"-item.json", protocol.KindItemDone, item, false)
	}
	active, inactive := true, false
	put("active.json", protocol.KindTurnState, protocol.TurnState{Stream: metadata("turn", 2), Active: &active}, false)
	put("delta.json", protocol.KindOutputDelta, protocol.OutputDelta{Stream: metadata("reply", 3), Text: &script.Partial}, false)
	put("done.json", protocol.KindItemDone, protocol.ItemDone{Stream: metadata("reply", 5), Text: &script.Reply}, false)
	put("idle.json", protocol.KindTurnState, protocol.TurnState{Stream: metadata("turn", 6), Active: &inactive}, false)
	h := newHarness(t, buildLina(t))
	if err = os.MkdirAll(filepath.Dir(h.socket), 0700); err != nil {
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
	quitVerified := false
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			err = core.Run(ctx, &conversationScriptConn{Conn: conn, reader: bufio.NewReader(conn), keys: map[string]string{}})
		}
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		err := <-done
		// A verified quit may close with unread duplicate/idle notifications.
		// Unix sockets can report that close as reset instead of EOF.
		if quitVerified && errors.Is(err, syscall.ECONNRESET) {
			return
		}
		if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
			t.Errorf("scripted conversation: %v", err)
		}
	})
	terminal := startReadyScreenTUI(t, h, []string{"TERM=xterm-256color"}, 80, 24, nil)
	waitScreen(t, h, terminal, "Ctrl+D")
	h.keys(terminal, script.Input+"\r")
	waitScreen(t, h, terminal, script.Partial)
	h.keys(terminal, script.Steering+"\r")
	snapshot := waitScreen(t, h, terminal, script.Reply)
	screen := screenText(snapshot)
	if strings.Contains(screen, script.Partial) || strings.Count(screen, script.Reply) != 1 || strings.Count(screen, script.Input) != 1 {
		t.Fatalf("reply was not replaced once: %s", screen)
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	quitVerified = true
}
