// SPDX-License-Identifier: Apache-2.0

// Package fakecore replays client-protocol fixtures as a scripted Core peer.
// It implements no application effects and is not a production Core service.
package fakecore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

// Clock supplies every timestamp and slow-reader wait. Tests advance or release
// it explicitly; fakecore never reads the wall clock or sleeps. Wait must honor
// cancellation. A caller sharing a clock across runs must synchronize it.
type Clock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}

// Faults apply to one step. Epoch alone persists into later steps. Drop consumes
// the step without delivering it; Duplicate keeps the message id and payload,
// but gives the second delivery a distinct attempt id. DisconnectAfter counts
// delivered frames in this step (including duplicates); zero closes immediately.
// SlowReader delays consumption of the request. Recovering refuses mutations
// with unavailable, while hello and read-only queries can still be scripted.
// Refusal overrides the scripted response with the specified closed error code.
// Precedence: wait, request, epoch, refusal/recovering, drop, duplicate, disconnect.
type Faults struct {
	Drop            bool               `json:"drop,omitempty"`
	SlowReader      time.Duration      `json:"slow_reader,omitempty"`
	Epoch           string             `json:"epoch,omitempty"`
	Duplicate       bool               `json:"duplicate,omitempty"`
	DisconnectAfter *int               `json:"disconnect_after,omitempty"`
	Recovering      bool               `json:"recovering,omitempty"`
	Refusal         protocol.ErrorCode `json:"refusal,omitempty"`
}

// Step names fixtures relative to the client 1.0 corpus. Request, when present,
// expects that fixture's kind and payload; ids and time belong to the caller.
// Hello also allows the caller's device id and diagnostic product version.
// Its first emitted envelope is a correlated JSON-RPC response; remaining
// envelopes are notifications. Without Request, every emission is a notification.
type Step struct {
	Request string   `json:"request,omitempty"`
	Emit    []string `json:"emit,omitempty"`
	Faults  Faults   `json:"faults,omitempty"`
}

type compiledStep struct {
	request *protocol.Envelope
	emit    [][]byte
	faults  Faults
}

// Core holds a validated, immutable script. Each Run starts a fresh replay with
// deterministic message and attempt ids; reset the injected clock to replay the
// same bytes. New snapshots the fixtures and caller-owned step data.
type Core struct {
	clock Clock
	steps []compiledStep
	epoch string
}

var ErrDisconnected = errors.New("fake Core injected disconnect")

const maxFrameBytes = 1 << 20

// New validates every referenced fixture and fault before any connection I/O.
func New(fsys fs.FS, clock Clock, steps []Step) (*Core, error) {
	if fsys == nil || clock == nil {
		return nil, errors.New("fake Core requires fixtures and an injected clock")
	}
	load := func(name string, party protocol.Party) ([]byte, protocol.Envelope, error) {
		data, err := fs.ReadFile(fsys, name)
		var e protocol.Envelope
		if err == nil {
			err = json.Unmarshal(data, &e)
		}
		if err == nil && e.Sender.Party != party {
			err = fmt.Errorf("expected %s fixture", party)
		}
		if err != nil {
			return nil, e, fmt.Errorf("fixture %s: %w", name, err)
		}
		return data, e, nil
	}
	_, welcome, err := load("welcome.json", protocol.PartyCore)
	if err != nil {
		return nil, err
	}
	c := &Core{clock: clock, epoch: welcome.Epoch}
	for i, step := range steps {
		s := compiledStep{faults: step.Faults}
		if step.Request != "" {
			_, e, err := load(step.Request, protocol.PartyClient)
			if err != nil {
				return nil, fmt.Errorf("step %d: %w", i, err)
			}
			s.request = &e
		}
		for _, name := range step.Emit {
			data, _, err := load(name, protocol.PartyCore)
			if err != nil {
				return nil, fmt.Errorf("step %d: %w", i, err)
			}
			s.emit = append(s.emit, data)
		}
		f := s.faults
		if f.SlowReader < 0 || (f.SlowReader > 0 && s.request == nil) ||
			((f.Recovering || f.Refusal != "") && s.request == nil) {
			return nil, fmt.Errorf("step %d: fault requires a request and nonnegative delay", i)
		}
		if f.DisconnectAfter != nil {
			n := *f.DisconnectAfter
			if n < 0 {
				return nil, fmt.Errorf("step %d: negative disconnect count", i)
			}
			s.faults.DisconnectAfter = &n
		}
		if f.Epoch != "" {
			probe := welcome
			probe.Epoch = f.Epoch
			if _, err := json.Marshal(probe); err != nil {
				return nil, fmt.Errorf("step %d: epoch: %w", i, err)
			}
		}
		code := f.Refusal
		if code == "" && f.Recovering && !readOnly(s.request.Kind) {
			code = protocol.ErrorUnavailable
		}
		if code != "" {
			_, refusal, err := load("outcomes/unavailable.json", protocol.PartyCore)
			if err != nil {
				return nil, err
			}
			refusal.Error.Code = code
			data, err := json.Marshal(refusal)
			if err != nil {
				return nil, fmt.Errorf("step %d: refusal: %w", i, err)
			}
			s.emit = [][]byte{data}
		}
		c.steps = append(c.steps, s)
	}
	return c, nil
}

// Queries retain their fixture responses during recovery. Unknown/new request
// kinds default to mutations until their semantics are explicitly added here.
func readOnly(kind protocol.Kind) bool {
	return map[protocol.Kind]bool{
		protocol.KindHello: true, protocol.KindHistoryGet: true,
		protocol.KindSubscribe: true, protocol.KindResultGet: true,
		protocol.KindProgressGet: true, protocol.KindProgressDigestGet: true,
		protocol.KindRunningGet: true, protocol.KindWaitingGet: true,
		protocol.KindDetailsGet: true, protocol.KindAgentTalkGet: true,
		protocol.KindAgentTalkSubscribe: true, protocol.KindAgentTalkExpand: true,
	}[kind]
}

type request struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id"`
	Method  protocol.Kind     `json:"method"`
	Params  protocol.Envelope `json:"params"`
}

// Run owns and closes conn, including on cancellation, script failure or an
// injected disconnect. Cancel interrupts blocked reads/writes as well as clock
// waits. The caller owns listening/accepting and joining Run's goroutine.
func (c *Core) Run(ctx context.Context, conn io.ReadWriteCloser) (err error) {
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		conn.Close()
		close(interrupted)
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
		conn.Close()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	reader := bufio.NewReaderSize(conn, maxFrameBytes)
	epoch, sequence := c.epoch, 0
	for i, step := range c.steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		f := step.faults
		if f.SlowReader > 0 {
			if err := c.clock.Wait(ctx, f.SlowReader); err != nil {
				return err
			}
		}
		var req request
		if step.request != nil {
			line, err := reader.ReadSlice('\n')
			if err != nil {
				return fmt.Errorf("step %d: read request: %w", i, err)
			}
			if err := json.Unmarshal(line, &req); err != nil {
				return fmt.Errorf("step %d: request: %w", i, err)
			}
			var id any
			decoder := json.NewDecoder(bytes.NewReader(req.ID))
			decoder.UseNumber()
			if err := decoder.Decode(&id); err != nil {
				return fmt.Errorf("step %d: request id: %w", i, err)
			}
			switch id.(type) {
			case string, json.Number:
			default:
				return fmt.Errorf("step %d: invalid request id", i)
			}
			payload := step.request.Payload
			if hello, ok := req.Params.Payload.(protocol.Hello); ok {
				if hello.Party != protocol.PartyClient || hello.ID != req.Params.Sender.ID {
					return fmt.Errorf("step %d: hello identity mismatch", i)
				}
				if expected, ok := payload.(protocol.Hello); ok {
					expected.ID, expected.ProductVersion = hello.ID, hello.ProductVersion
					payload = expected
				}
			}
			want, _ := json.Marshal(payload)
			got, _ := json.Marshal(req.Params.Payload)
			if req.JSONRPC != "2.0" || req.Method != step.request.Kind || req.Params.Kind != step.request.Kind ||
				req.Params.Sender.Party != protocol.PartyClient || !bytes.Equal(want, got) {
				return fmt.Errorf("step %d: unexpected request", i)
			}
		}
		if f.Epoch != "" {
			epoch = f.Epoch
		}
		delivered := 0
		disconnected := func() bool { return f.DisconnectAfter != nil && delivered == *f.DisconnectAfter }
		if disconnected() {
			return ErrDisconnected
		}
		for j, data := range step.emit {
			var e protocol.Envelope
			if err := json.Unmarshal(data, &e); err != nil {
				return err // New validated and snapshotted each fixture.
			}
			sequence++
			e.MessageID = fmt.Sprintf("fake-message-%d", sequence)
			e.Epoch, e.SentAt = epoch, c.clock.Now().UTC().Format(time.RFC3339Nano)
			e.ReplyTo = nil
			if step.request != nil && j == 0 {
				e.ReplyTo = &req.Params.MessageID
			}
			if welcome, ok := e.Payload.(protocol.Welcome); ok {
				welcome.Epoch = epoch
				e.Payload = welcome
			}
			if f.Drop {
				continue
			}
			copies := 1
			if f.Duplicate {
				copies++
			}
			for attempt := 1; attempt <= copies; attempt++ {
				e.AttemptID = fmt.Sprintf("%s-attempt-%d", e.MessageID, attempt)
				var frame any
				if e.ReplyTo != nil {
					frame = struct {
						JSONRPC string            `json:"jsonrpc"`
						ID      json.RawMessage   `json:"id"`
						Result  protocol.Envelope `json:"result"`
					}{"2.0", req.ID, e}
				} else {
					frame = struct {
						JSONRPC string            `json:"jsonrpc"`
						Method  protocol.Kind     `json:"method"`
						Params  protocol.Envelope `json:"params"`
					}{"2.0", e.Kind, e}
				}
				if err := json.NewEncoder(conn).Encode(frame); err != nil {
					return fmt.Errorf("step %d: write: %w", i, err)
				}
				delivered++
				if disconnected() {
					return ErrDisconnected
				}
			}
		}
	}
	return nil
}
