// SPDX-License-Identifier: Apache-2.0

package fakecore_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/fakecore"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/protocol/conformance"
)

var fixtures = os.DirFS("../../protocol/fixtures/client/1.0")

type clock struct {
	now   time.Time
	waits []time.Duration
}

func newClock() *clock          { return &clock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }
func (c *clock) Now() time.Time { return c.now }
func (c *clock) Wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)
	return nil
}

type stream struct {
	*bytes.Reader
	output bytes.Buffer
	closed bool
}

func (s *stream) Write(p []byte) (int, error) { return s.output.Write(p) }
func (s *stream) Close() error                { s.closed = true; return nil }

func request(t *testing.T, name string, id any) []byte {
	t.Helper()
	data, err := os.ReadFile("../../protocol/fixtures/client/1.0/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var e protocol.Envelope
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": e.Kind, "params": e})
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

type frame struct {
	ID     json.RawMessage    `json:"id"`
	Result *protocol.Envelope `json:"result"`
	Params *protocol.Envelope `json:"params"`
}

func (f frame) envelope() protocol.Envelope {
	if f.Result != nil {
		return *f.Result
	}
	return *f.Params
}

func replay(t *testing.T, steps []fakecore.Step, input []byte) ([]frame, []byte, error) {
	t.Helper()
	c, err := fakecore.New(fixtures, newClock(), steps)
	if err != nil {
		t.Fatal(err)
	}
	s := &stream{Reader: bytes.NewReader(input)}
	err = c.Run(t.Context(), s)
	if !s.closed {
		t.Fatal("stream not closed")
	}
	var frames []frame
	scanner := bufio.NewScanner(bytes.NewReader(s.output.Bytes()))
	for scanner.Scan() {
		var f frame
		if err := json.Unmarshal(scanner.Bytes(), &f); err != nil {
			t.Fatal(err)
		}
		if f.Result == nil && f.Params == nil {
			t.Fatal("missing envelope")
		}
		frames = append(frames, f)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return frames, s.output.Bytes(), err
}

func TestReplayDeterministic(t *testing.T) {
	steps := []fakecore.Step{
		{Request: "hello.json", Emit: []string{"welcome.json"}},
		{Emit: []string{"normal/output_delta.json", "results/summary.json"}},
	}
	input := request(t, "hello.json", 9007199254740993)
	frames, first, err := replay(t, steps, input)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := replay(t, steps, input)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("replay changed: %v", err)
	}
	if len(frames) != 3 || string(frames[0].ID) != "9007199254740993" {
		t.Fatalf("frames: %+v", frames)
	}
	for i, f := range frames {
		e := f.envelope()
		if e.SentAt != "2026-01-01T00:00:00Z" || e.Epoch != "42" {
			t.Fatalf("frame %d: %+v", i, e)
		}
		if i > 0 && (e.ReplyTo != nil || f.Result != nil) {
			t.Fatal("notification became a reply")
		}
	}
	if frames[0].Result.ReplyTo == nil || *frames[0].Result.ReplyTo != "hello-example" {
		t.Fatal("uncorrelated welcome")
	}
}

func TestReplayCorpusAndReuse(t *testing.T) {
	suite, err := conformance.Load(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for name, data := range suite.Envelopes {
		var e protocol.Envelope
		if err := json.Unmarshal(data, &e); err != nil {
			t.Fatal(err)
		}
		if e.Sender.Party == protocol.PartyCore {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		t.Fatal("no Core fixtures")
	}
	steps := []fakecore.Step{{Emit: slices.Clone(names)}}
	core, err := fakecore.New(fixtures, newClock(), steps)
	if err != nil {
		t.Fatal(err)
	}
	steps[0].Emit[0] = "missing.json" // New must snapshot caller-owned data.
	var first []byte
	for run := 0; run < 2; run++ {
		s := &stream{Reader: bytes.NewReader(nil)}
		if err := core.Run(t.Context(), s); err != nil {
			t.Fatal(err)
		}
		if run == 0 {
			first = bytes.Clone(s.output.Bytes())
		} else if !bytes.Equal(first, s.output.Bytes()) {
			t.Fatal("reusing Core changed replay")
		}
		decoder := json.NewDecoder(bytes.NewReader(s.output.Bytes()))
		for _, name := range names {
			var f frame
			if err := decoder.Decode(&f); err != nil {
				t.Fatal(err)
			}
			got := f.envelope()
			var want protocol.Envelope
			if err := json.Unmarshal(suite.Envelopes[name], &want); err != nil {
				t.Fatal(err)
			}
			// These are the documented delivery substitutions; all remaining
			// payloads, codes, receipts and exact-number references must survive.
			want.MessageID, want.AttemptID, want.Epoch, want.SentAt, want.ReplyTo = got.MessageID, got.AttemptID, got.Epoch, got.SentAt, nil
			if w, ok := want.Payload.(protocol.Welcome); ok {
				w.Epoch = got.Epoch
				want.Payload = w
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("fixture changed: %s", name)
			}
		}
		if err := decoder.Decode(new(frame)); err != io.EOF {
			t.Fatalf("extra output: %v", err)
		}
	}
}

func TestFaults(t *testing.T) {
	t.Run("drop advances script", func(t *testing.T) {
		frames, _, err := replay(t, []fakecore.Step{
			{Emit: []string{"normal/output_delta.json"}, Faults: fakecore.Faults{Drop: true}},
			{Emit: []string{"normal/item_done.json"}},
		}, nil)
		if err != nil || len(frames) != 1 || frames[0].envelope().Kind != protocol.KindItemDone {
			t.Fatalf("drop: %+v %v", frames, err)
		}
	})
	t.Run("duplicate keeps logical identity", func(t *testing.T) {
		frames, _, err := replay(t, []fakecore.Step{{Emit: []string{"normal/output_delta.json"}, Faults: fakecore.Faults{Duplicate: true}}}, nil)
		if err != nil || len(frames) != 2 {
			t.Fatalf("duplicate: %d %v", len(frames), err)
		}
		a, b := frames[0].envelope(), frames[1].envelope()
		if a.AttemptID == b.AttemptID {
			t.Fatal("delivery attempt reused")
		}
		b.AttemptID = a.AttemptID
		if !reflect.DeepEqual(a, b) {
			t.Fatal("duplicate changed logical message")
		}
	})
	t.Run("epoch persists including welcome payload", func(t *testing.T) {
		frames, _, err := replay(t, []fakecore.Step{
			{Request: "hello.json", Emit: []string{"welcome.json"}, Faults: fakecore.Faults{Epoch: "18446744073709551615"}},
			{Emit: []string{"normal/output_delta.json"}},
		}, request(t, "hello.json", "rpc-hello"))
		if err != nil || len(frames) != 2 {
			t.Fatalf("epoch: %v", err)
		}
		for _, f := range frames {
			if f.envelope().Epoch != "18446744073709551615" {
				t.Fatal("epoch lost")
			}
		}
		if frames[0].Result.Payload.(protocol.Welcome).Epoch != frames[0].Result.Epoch {
			t.Fatal("welcome epoch mismatch")
		}
	})
	t.Run("disconnect mid batch", func(t *testing.T) {
		n := 1
		frames, _, err := replay(t, []fakecore.Step{
			{Emit: []string{"normal/output_delta.json", "normal/item_done.json"}, Faults: fakecore.Faults{DisconnectAfter: &n}},
			{Emit: []string{"normal/turn_state.json"}},
		}, nil)
		if !errors.Is(err, fakecore.ErrDisconnected) || len(frames) != 1 {
			t.Fatalf("disconnect: %d %v", len(frames), err)
		}
	})
	t.Run("recover then refuse then resume", func(t *testing.T) {
		steps := []fakecore.Step{
			{Request: "normal/input.json", Emit: []string{"normal/turn_state.json"}, Faults: fakecore.Faults{Recovering: true}},
			{Request: "normal/history_get.json", Emit: []string{"normal/history.json"}, Faults: fakecore.Faults{Recovering: true}},
			{Request: "normal/input.json", Emit: []string{"normal/turn_state.json"}, Faults: fakecore.Faults{Refusal: protocol.ErrorNotAuthorized}},
			{Request: "normal/input.json", Emit: []string{"normal/turn_state.json"}},
		}
		var input []byte
		for i, s := range steps {
			input = append(input, request(t, s.Request, i)...)
		}
		frames, _, err := replay(t, steps, input)
		if err != nil || len(frames) != 4 {
			t.Fatalf("recover: %d %v", len(frames), err)
		}
		for i, code := range map[int]protocol.ErrorCode{0: protocol.ErrorUnavailable, 2: protocol.ErrorNotAuthorized} {
			e := frames[i].envelope()
			if e.Kind != protocol.KindError || e.EffectState != protocol.EffectRefused || e.Error == nil || e.Error.Code != code {
				t.Fatalf("refusal: %+v", e)
			}
		}
		if frames[1].envelope().Kind != protocol.KindHistory || frames[3].envelope().Kind != protocol.KindTurnState {
			t.Fatal("recovery blocked reads or persisted after switch cleared")
		}
	})
}

type gatedClock struct {
	*clock
	waiting chan time.Duration
	release chan struct{}
}

type observedConn struct {
	net.Conn
	reads atomic.Int32
}

func (c *observedConn) Read(p []byte) (int, error) {
	c.reads.Add(1)
	return c.Conn.Read(p)
}

func (c *gatedClock) Wait(ctx context.Context, d time.Duration) error {
	select {
	case c.waiting <- d:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-c.release:
		return c.clock.Wait(ctx, d)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestSlowReaderAndCancellation(t *testing.T) {
	for _, cancelWait := range []bool{false, true} {
		t.Run(map[bool]string{false: "release", true: "cancel"}[cancelWait], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			clk := &gatedClock{clock: newClock(), waiting: make(chan time.Duration), release: make(chan struct{})}
			core, err := fakecore.New(fixtures, clk, []fakecore.Step{{Request: "hello.json", Emit: []string{"welcome.json"}, Faults: fakecore.Faults{SlowReader: time.Hour}}})
			if err != nil {
				t.Fatal(err)
			}
			server, client := net.Pipe()
			observed := &observedConn{Conn: server}
			defer client.Close()
			done := make(chan error, 1)
			go func() { done <- core.Run(ctx, observed) }()
			select {
			case d := <-clk.waiting:
				if d != time.Hour {
					t.Fatal(d)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			written := make(chan error, 1)
			input := request(t, "hello.json", "hello-rpc")
			go func() { _, err := client.Write(input); written <- err }()
			// Wait has acknowledged entry and cannot return until released.
			// Inspect actual read calls here, independent of writer scheduling.
			if observed.reads.Load() != 0 {
				t.Fatal("read before clock release")
			}
			if cancelWait {
				cancel()
			} else {
				close(clk.release)
				data, err := io.ReadAll(client)
				if err != nil {
					t.Fatal(err)
				}
				var f frame
				if err := json.Unmarshal(data, &f); err != nil {
					t.Fatal(err)
				}
				if f.envelope().SentAt != "2026-01-01T01:00:00Z" {
					t.Fatal("injected time ignored")
				}
			}
			if err := <-done; (cancelWait && !errors.Is(err, context.Canceled)) || (!cancelWait && err != nil) {
				t.Fatal(err)
			}
			if err := <-written; !cancelWait && err != nil {
				t.Fatal(err)
			}
		})
	}
}

type blockingConn struct {
	net.Conn
	entered chan struct{}
}

func (c *blockingConn) Read(p []byte) (int, error)  { c.entered <- struct{}{}; return c.Conn.Read(p) }
func (c *blockingConn) Write(p []byte) (int, error) { c.entered <- struct{}{}; return c.Conn.Write(p) }

func TestCancelBlockedIO(t *testing.T) {
	for name, step := range map[string]fakecore.Step{
		"read":  {Request: "hello.json", Emit: []string{"welcome.json"}},
		"write": {Emit: []string{"normal/output_delta.json"}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			core, err := fakecore.New(fixtures, newClock(), []fakecore.Step{step})
			if err != nil {
				t.Fatal(err)
			}
			server, client := net.Pipe()
			defer client.Close()
			conn := &blockingConn{Conn: server, entered: make(chan struct{})}
			done := make(chan error, 1)
			go func() { done <- core.Run(ctx, conn) }()
			select {
			case <-conn.entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestRejectBadScriptsBeforeIO(t *testing.T) {
	n := -1
	for _, step := range []fakecore.Step{
		{Emit: []string{"missing.json"}},
		{Emit: []string{"invalid/unknown_kind.json"}},
		{Emit: []string{"hello.json"}},
		{Request: "welcome.json"},
		{Emit: []string{"welcome.json"}, Faults: fakecore.Faults{Epoch: "01"}},
		{Faults: fakecore.Faults{SlowReader: -1}},
		{Faults: fakecore.Faults{SlowReader: 1}},
		{Faults: fakecore.Faults{DisconnectAfter: &n}},
		{Request: "hello.json", Faults: fakecore.Faults{Refusal: "invented"}},
	} {
		if _, err := fakecore.New(fixtures, newClock(), []fakecore.Step{step}); err == nil {
			t.Fatalf("accepted %+v", step)
		}
	}
	if _, err := fakecore.New(fixtures, nil, nil); err == nil {
		t.Fatal("accepted absent clock")
	}
}

func TestRejectUnexpectedOrOversizedRequest(t *testing.T) {
	steps := []fakecore.Step{{Request: "hello.json", Emit: []string{"welcome.json"}}}
	for _, input := range [][]byte{request(t, "normal/input.json", 1), []byte("{}\n"), bytes.Repeat([]byte{'x'}, 1<<20), bytes.TrimSuffix(request(t, "hello.json", 1), []byte{'\n'})} {
		frames, _, err := replay(t, steps, input)
		if err == nil || len(frames) != 0 {
			t.Fatal("invalid request produced welcome")
		}
	}
}
