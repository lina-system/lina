// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"
	"time"

	"github.com/thisisjun786/lina/internal/fakecore"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Logical time advances only when the scripted peer asks to wait. No wall time
// controls the scenario; socket deadlines below are failure bounds only.
type scenarioClock struct{ now time.Time }

// Keep the shared conformance corpus unchanged. The TUI scenario explicitly
// offers its implemented text-stream capabilities, which Core may grant or not.
func tuiFixtures(t *testing.T) fstest.MapFS {
	t.Helper()
	fixtures := fstest.MapFS{}
	root := os.DirFS("../../protocol/fixtures/client/1.0")
	err := fs.WalkDir(root, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(root, path)
		if err == nil {
			fixtures[path] = &fstest.MapFile{Data: data}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(fixtures["hello.json"].Data, &envelope); err != nil {
		t.Fatal(err)
	}
	hello := envelope.Payload.(protocol.Hello)
	hello.Capabilities = []protocol.Capability{{Name: "worker_lanes", Version: "1.0"}, {Name: "web", Version: "1.0"}, {Name: "agents", Version: "1.0"}, {Name: "memory", Version: "1.0"}, {Name: "exchanges", Version: "1.0"}, {Name: "channels", Version: "1.0"}, {Name: "accounts", Version: "1.0"}, {Name: "sign_in_relay", Version: "1.0"}, {Name: "conversation", Version: "1.0"}, {Name: "turn_activity", Version: "1.0"}, {Name: "running", Version: "1.0"}, {Name: "progress", Version: "1.0"}, {Name: "waiting", Version: "1.0"}}
	envelope.Payload = hello
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	fixtures["hello.json"] = &fstest.MapFile{Data: data}
	return fixtures
}

func (c *scenarioClock) Now() time.Time { return c.now }
func (c *scenarioClock) Wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.now = c.now.Add(d)
	return nil
}

// Use #34's isolated state, socket, command and PTY lifecycle. The scenario is
// data under m1 so the unexported test helpers remain shared in this package.
func (h *harness) fakeCore(steps []fakecore.Step, tui ...bool) <-chan error {
	h.t.Helper()
	clk := &scenarioClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	fixtures := fs.FS(os.DirFS("../../protocol/fixtures/client/1.0"))
	if len(tui) > 0 && tui[0] {
		fixtures = tuiFixtures(h.t)
	}
	core, err := fakecore.New(fixtures, clk, steps)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(h.socket), 0700); err != nil {
		h.t.Fatal(err)
	}
	listener, err := net.Listen("unix", h.socket)
	if err != nil {
		h.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	result := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		defer listener.Close()
		conn, err := listener.Accept()
		if err == nil {
			err = core.Run(ctx, conn)
		}
		result <- err
	}()
	h.t.Cleanup(func() { cancel(); listener.Close(); <-joined; stop() })
	return result
}

func TestM1FakeCore(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("fakecore.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Protocol []fakecore.Step `json:"protocol"`
		TUI      []fakecore.Step `json:"tui"`
		Refused  []fakecore.Step `json:"tui_refused"`
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	t.Run("protocol faults", func(t *testing.T) {
		h := newHarness(t, "")
		done := h.fakeCore(scenario.Protocol)
		conn, err := net.Dial("unix", h.socket)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(operationTimeout)); err != nil {
			t.Fatal(err)
		}
		// Requests are known finite frames; write them while the server drains
		// its scripted replies. Joining the writer also proves request delivery.
		var requests [][]byte
		ids := map[string]string{}
		for i, step := range scenario.Protocol {
			if step.Request == "" {
				continue
			}
			data, err := os.ReadFile("../../protocol/fixtures/client/1.0/" + step.Request)
			if err != nil {
				t.Fatal(err)
			}
			var e protocol.Envelope
			if err := json.Unmarshal(data, &e); err != nil {
				t.Fatal(err)
			}
			id := fmt.Sprintf("rpc-%d", i)
			e.MessageID = fmt.Sprintf("client-message-%d", i)
			ids[id] = e.MessageID
			data, err = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": e.Kind, "params": e})
			if err != nil {
				t.Fatal(err)
			}
			requests = append(requests, append(data, '\n'))
		}
		written := make(chan error, 1)
		go func() {
			for _, data := range requests {
				if _, err := conn.Write(data); err != nil {
					written <- err
					return
				}
			}
			written <- nil
		}()
		t.Cleanup(func() {
			conn.Close()
			if err := <-written; err != nil {
				t.Errorf("write scenario requests: %v", err)
			}
		})
		reader := bufio.NewScanner(conn)
		reader.Buffer(make([]byte, 4096), 1<<20)
		var envelopes []protocol.Envelope
		for reader.Scan() {
			var f struct {
				JSONRPC string             `json:"jsonrpc"`
				ID      string             `json:"id"`
				Method  protocol.Kind      `json:"method"`
				Result  *protocol.Envelope `json:"result"`
				Params  *protocol.Envelope `json:"params"`
			}
			if err := json.Unmarshal(reader.Bytes(), &f); err != nil {
				t.Fatal(err)
			}
			if f.JSONRPC != "2.0" {
				t.Fatal("invalid frame")
			}
			var e protocol.Envelope
			if f.Result != nil {
				// #34's simple client uses the same RPC and message id; this
				// scenario deliberately separates them to check both correlations.
				e = *f.Result
				if ids[f.ID] == "" || e.ReplyTo == nil || *e.ReplyTo != ids[f.ID] || e.Sender.Party != protocol.PartyCore {
					t.Fatal("uncorrelated reply")
				}
				delete(ids, f.ID)
			} else {
				if f.Params == nil || f.ID != "" || f.Method != f.Params.Kind || f.Params.ReplyTo != nil {
					t.Fatal("invalid notification")
				}
				e = *f.Params
			}
			envelopes = append(envelopes, e)
		}
		if err := reader.Err(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, fakecore.ErrDisconnected) {
			t.Fatal(err)
		}
		want := []protocol.Kind{protocol.KindWelcome, protocol.KindItemDone, protocol.KindItemDone, protocol.KindError, protocol.KindHistory, protocol.KindError, protocol.KindTurnState, protocol.KindConnected}
		var kinds []protocol.Kind
		for _, e := range envelopes {
			kinds = append(kinds, e.Kind)
		}
		if !reflect.DeepEqual(kinds, want) || len(ids) != 0 {
			t.Fatalf("kinds=%v missing replies=%d", kinds, len(ids))
		}
		for i, e := range envelopes {
			epoch := "42"
			if i == len(envelopes)-1 {
				epoch = "43"
			}
			if e.Epoch != epoch || e.SentAt != "2026-01-01T00:00:01Z" {
				t.Fatalf("time or epoch: %+v", e)
			}
		}
		a, b := envelopes[1], envelopes[2]
		if a.AttemptID == b.AttemptID {
			t.Fatal("attempt id reused")
		}
		b.AttemptID = a.AttemptID
		if !reflect.DeepEqual(a, b) {
			t.Fatal("duplicate changed logical message")
		}
		for i, code := range map[int]protocol.ErrorCode{3: protocol.ErrorUnavailable, 5: protocol.ErrorNotAuthorized} {
			e := envelopes[i]
			if e.Error == nil || e.Error.Code != code || e.EffectState != protocol.EffectRefused {
				t.Fatalf("refusal: %+v", e)
			}
		}
	})
	binary := buildLina(t)
	t.Run("TUI connect and quit", func(t *testing.T) {
		h := newHarness(t, binary)
		done := h.fakeCore(scenario.TUI, true)
		term := h.startTUI()
		h.expectState(term, "live")
		h.keys(term, "q\n")
		term.process.wait(t, 0)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
	t.Run("TUI refused", func(t *testing.T) {
		h := newHarness(t, binary)
		done := h.fakeCore(scenario.Refused, true)
		term := h.startTUI()
		term.process.wait(t, 78)
		for range term.lines {
			t.Error("refused connection reported live")
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
