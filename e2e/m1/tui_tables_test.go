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
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/thisisjun786/lina/internal/fakecore"
	"github.com/thisisjun786/lina/kit/protocol"

	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

// The fake Core waits for screen observations at both streaming boundaries.
// A real PTY verifies display-cell alignment before item completion and after
// resizing; no delay stands in for a rendered frame.
func TestM1TUIMarkdownTables(t *testing.T) {
	const open = "| 항목 | 상태 | 메모 |\n|---|---|---|\n| 한글 | ready | 🙂 |\n| Latin | 완료 | é |\n"
	const closing = "\nTable closed\n"
	closeTable, finish := make(chan struct{}), make(chan struct{})
	h := newHarness(t, buildLina(t))
	fixtures := tuiFixtures(t)
	var welcome protocol.Envelope
	if err := json.Unmarshal(fixtures["welcome.json"].Data, &welcome); err != nil {
		t.Fatal(err)
	}
	w := welcome.Payload.(protocol.Welcome)
	w.Capabilities = []protocol.Capability{{Name: "conversation", Version: "1.0"}}
	welcome.Payload = w
	fixtures["welcome.json"] = &fstest.MapFile{Data: mustJSON(t, welcome)}
	put := func(name string, kind protocol.Kind, payload protocol.Payload) {
		envelope := welcome
		if kind == protocol.KindSubscribe || kind == protocol.KindHistoryGet {
			if err := json.Unmarshal(fixtures["hello.json"].Data, &envelope); err != nil {
				t.Fatal(err)
			}
		}
		envelope.Kind, envelope.Payload = kind, payload
		envelope.ReplyTo, envelope.Error = nil, nil
		fixtures[name] = &fstest.MapFile{Data: mustJSON(t, envelope)}
	}
	conversation := "fixture"
	topic := "conversation:" + conversation
	steps := []fakecore.Step{{Request: "hello.json", Emit: []string{"welcome.json"}}}
	for _, part := range []struct{ name, topic string }{{"core", "core"}, {"conversation", topic}, {"results", "results:" + conversation}} {
		put(part.name+"-request.json", protocol.KindSubscribe, protocol.Subscribe{Topic: &part.topic})
		put(part.name+"-snapshot.json", protocol.KindSnapshot, protocol.Snapshot{Topic: part.topic, Cursor: protocol.StreamCursor{Epoch: w.Epoch, Seq: "0"}, RevocationEpoch: "0", Items: []protocol.StreamItem{}})
		steps = append(steps, fakecore.Step{Request: part.name + "-request.json", Emit: []string{part.name + "-snapshot.json"}})
		if part.name == "core" {
			put("history-request.json", protocol.KindHistoryGet, protocol.HistoryGet{})
			put("history.json", protocol.KindHistory, protocol.History{ConversationID: &conversation})
			steps = append(steps, fakecore.Step{Request: "history-request.json", Emit: []string{"history.json"}})
		}
	}
	metadata := func(sequence json.Number) *protocol.StreamMetadata {
		return &protocol.StreamMetadata{Topic: topic, Key: "reply", Cursor: protocol.StreamCursor{Epoch: w.Epoch, Seq: sequence}, Revision: sequence}
	}
	openText, closingText, finalText := open, closing, open+closing+"\nTable finished"
	put("open.json", protocol.KindOutputDelta, protocol.OutputDelta{Stream: metadata("1"), Text: &openText})
	put("closed.json", protocol.KindOutputDelta, protocol.OutputDelta{Stream: metadata("2"), Text: &closingText})
	put("done.json", protocol.KindItemDone, protocol.ItemDone{Stream: metadata("3"), Text: &finalText})
	steps = append(steps,
		fakecore.Step{Emit: []string{"open.json"}},
		fakecore.Step{Emit: []string{"closed.json"}},
		fakecore.Step{Emit: []string{"done.json"}},
		fakecore.Step{Request: "hello.json"},
	)
	core, err := fakecore.New(fixtures, &scenarioClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, steps)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(h.socket), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", h.socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			err = core.Run(ctx, &tableScreenConn{Conn: conn, ctx: ctx, gates: []<-chan struct{}{closeTable, finish}})
		}
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		if err := <-done; err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	terminal, _ := h.startConversationTUI(80, 30, "TERM=xterm-256color", "COLORTERM=truecolor")
	preview := waitScreen(t, h, terminal, "Latin")
	if !strings.Contains(screenText(preview), "|---|---|---|") {
		t.Fatal("open table must remain an unstyled preview")
	}
	close(closeTable)
	closed := waitScreen(t, h, terminal, "Table closed")
	assertMarkdownTableCells(t, closed)
	t.Logf("closed table at 80 columns:\n%s", screenText(closed))
	for _, size := range []struct{ width, height int }{{40, 28}, {140, 32}} {
		h.resizeTerminal(terminal, size.width, size.height)
		snapshot, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
			// Cursor relocation proves the TUI repainted after the emulator resize.
			return len(s.Cells) == size.height && len(s.Cells[0]) == size.width &&
				s.CursorY == size.height-2 && strings.Contains(screenText(s), "Table closed")
		})
		if err != nil {
			t.Fatal(err)
		}
		assertMarkdownTableCells(t, snapshot)
		t.Logf("closed table at %d columns:\n%s", size.width, screenText(snapshot))
	}
	close(finish)
	assertMarkdownTableCells(t, waitScreen(t, h, terminal, "Table finished"))
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
}

func assertMarkdownTableCells(t *testing.T, snapshot tuiterm.Snapshot) {
	t.Helper()
	var boundaries []int
	count := 0
	for _, row := range snapshot.Cells {
		var line strings.Builder
		var columns []int
		for x, cell := range row {
			line.WriteString(cell.Content)
			if cell.Content == "|" {
				columns = append(columns, x)
			}
		}
		if strings.Contains(line.String(), "|---") {
			t.Fatalf("closed table still displays raw delimiter row:\n%s", screenText(snapshot))
		}
		if !strings.Contains(line.String(), "항목") && !strings.Contains(line.String(), "한글") && !strings.Contains(line.String(), "Latin") {
			continue
		}
		if len(columns) != 4 {
			t.Fatalf("table row has %d boundaries: %q", len(columns), line.String())
		}
		if boundaries == nil {
			boundaries = columns
		} else if !reflect.DeepEqual(columns, boundaries) {
			t.Fatalf("table columns differ: %v != %v\n%s", columns, boundaries, screenText(snapshot))
		}
		count++
	}
	if count != 3 {
		t.Fatalf("table rows = %d, want 3\n%s", count, screenText(snapshot))
	}
}

// Gate reply writes on rendered-screen observations, without wall-clock waits.
type tableScreenConn struct {
	net.Conn
	ctx     context.Context
	gates   []<-chan struct{}
	replies int
}

func (c *tableScreenConn) Write(data []byte) (int, error) {
	var frame struct{ Params *protocol.Envelope }
	if err := json.Unmarshal(data, &frame); err != nil {
		return 0, err
	}
	if frame.Params != nil && (frame.Params.Kind == protocol.KindOutputDelta || frame.Params.Kind == protocol.KindItemDone) {
		if c.replies > 0 {
			select {
			case <-c.gates[c.replies-1]:
			case <-c.ctx.Done():
				return 0, c.ctx.Err()
			}
		}
		c.replies++
	}
	return c.Conn.Write(data)
}
