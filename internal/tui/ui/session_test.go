// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

type scriptedPeer struct {
	welcome protocol.Welcome
	read    func(context.Context) (protocol.Envelope, error)
	closed  atomic.Int32
}

func (p *scriptedPeer) Welcome() protocol.Welcome                              { return p.welcome }
func (p *scriptedPeer) Receive(ctx context.Context) (protocol.Envelope, error) { return p.read(ctx) }
func (p *scriptedPeer) Close() error                                           { p.closed.Add(1); return nil }

func TestConnectionLoopReconnectsAndAnnouncesEpochOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := &scriptedPeer{welcome: protocol.Welcome{MainID: "main", Epoch: "42"}, read: func(context.Context) (protocol.Envelope, error) { return protocol.Envelope{}, io.EOF }}
	second := &scriptedPeer{welcome: protocol.Welcome{MainID: "main", Epoch: "43"}, read: func(context.Context) (protocol.Envelope, error) {
		cancel()
		return protocol.Envelope{}, context.Canceled
	}}
	var messages []tea.Msg
	dials, waits := 0, 0
	err := connectionLoop(ctx, func(context.Context) (connectionPeer, error) {
		dials++
		if dials == 1 {
			return first, nil
		}
		return second, nil
	}, func(ctx context.Context, delay time.Duration) error {
		waits++
		if delay != time.Second {
			t.Fatalf("retry delay = %s", delay)
		}
		return nil
	}, func(msg tea.Msg) error { messages = append(messages, msg); return nil })
	if !errors.Is(err, context.Canceled) || dials != 2 || waits != 1 || first.closed.Load() != 1 || second.closed.Load() != 1 {
		t.Fatalf("lifecycle: %v dials=%d waits=%d close=%d/%d", err, dials, waits, first.closed.Load(), second.closed.Load())
	}
	var states []Connection
	notices := 0
	for _, msg := range messages {
		switch msg := msg.(type) {
		case shellConnection:
			states = append(states, msg.Status.State)
		case shellData:
			if notice, ok := msg.Message.(state.Notice); ok && notice.Code == "epoch_changed" {
				notices++
			}
		}
	}
	if !reflect.DeepEqual(states, []Connection{Connecting, Live, Retrying, Live}) || notices != 1 {
		t.Fatalf("states=%v notices=%d", states, notices)
	}
}

func TestConnectionLoopCancellationClosesBlockedReceiver(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	entered := make(chan struct{})
	closed := make(chan struct{})
	peer := &blockingPeer{entered: entered, closed: closed}
	done := make(chan error, 1)
	go func() {
		done <- connectionLoop(ctx, func(context.Context) (connectionPeer, error) { return peer, nil }, waitRetry, func(tea.Msg) error { return nil })
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if peer.closes.Load() != 1 {
		t.Fatal("connection not closed exactly once")
	}
}

type blockingPeer struct {
	entered, closed chan struct{}
	closes          atomic.Int32
}

func (p *blockingPeer) Welcome() protocol.Welcome {
	return protocol.Welcome{MainID: "main", Epoch: "42"}
}
func (p *blockingPeer) Receive(context.Context) (protocol.Envelope, error) {
	close(p.entered)
	<-p.closed
	return protocol.Envelope{}, io.EOF
}
func (p *blockingPeer) Close() error {
	if p.closes.Add(1) == 1 {
		close(p.closed)
	}
	return nil
}

func TestConnectionLoopNeverRetriesInitialAbsenceOrRefusal(t *testing.T) {
	for _, cause := range []error{clientapi.ErrUnavailable, &clientapi.Refusal{Cause: protocol.Error{Code: protocol.ErrorUnsupportedCombination}}} {
		dials := 0
		err := connectionLoop(t.Context(), func(context.Context) (connectionPeer, error) { dials++; return nil, cause }, func(context.Context, time.Duration) error { t.Fatal("initial failure retried"); return nil }, func(tea.Msg) error { return nil })
		if !errors.Is(err, cause) || dials != 1 {
			t.Fatalf("initial failure: %v, dials=%d", err, dials)
		}
	}
}

func TestConnectionLoopRejectsIdentityChangeAndEpochRollback(t *testing.T) {
	for _, welcome := range []protocol.Welcome{{MainID: "other", Epoch: "43"}, {MainID: "main", Epoch: "41"}} {
		dials := 0
		var final ConnectionStatus
		err := connectionLoop(t.Context(), func(context.Context) (connectionPeer, error) {
			dials++
			w := protocol.Welcome{MainID: "main", Epoch: "42"}
			if dials > 1 {
				w = welcome
			}
			return &scriptedPeer{welcome: w, read: func(context.Context) (protocol.Envelope, error) { return protocol.Envelope{}, io.EOF }}, nil
		}, func(context.Context, time.Duration) error { return nil }, func(msg tea.Msg) error {
			if s, ok := msg.(shellConnection); ok {
				final = s.Status
			}
			return nil
		})
		if err == nil || dials != 2 || final.State != Failed {
			t.Fatalf("unsafe reconnect admitted: %v, %d, %+v", err, dials, final)
		}
	}
}

func TestConnectionLoopDoesNotRetryDisplayFailure(t *testing.T) {
	fixture, err := os.ReadFile("../../../protocol/fixtures/client/1.0/normal/connected.json")
	if err != nil {
		t.Fatal(err)
	}
	var frame protocol.Envelope
	if err := json.Unmarshal(fixture, &frame); err != nil {
		t.Fatal(err)
	}
	peer := &scriptedPeer{welcome: protocol.Welcome{MainID: "main", Epoch: "42"}, read: func(context.Context) (protocol.Envelope, error) { return frame, nil }}
	dials := 0
	err = connectionLoop(t.Context(), func(context.Context) (connectionPeer, error) { dials++; return peer, nil }, func(context.Context, time.Duration) error { t.Fatal("display failure retried"); return nil }, func(message tea.Msg) error {
		if _, ok := message.(shellData); ok {
			return io.EOF
		}
		return nil
	})
	if !errors.Is(err, io.EOF) || dials != 1 || peer.closed.Load() != 1 {
		t.Fatalf("display error: %v, dials=%d", err, dials)
	}
}

func TestConnectionLoopBackoffIsBoundedAndCancelable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	peer := &scriptedPeer{welcome: protocol.Welcome{MainID: "main", Epoch: "42"}, read: func(context.Context) (protocol.Envelope, error) { return protocol.Envelope{}, io.EOF }}
	dials := 0
	var delays []time.Duration
	err := connectionLoop(ctx, func(context.Context) (connectionPeer, error) {
		dials++
		if dials == 1 {
			return peer, nil
		}
		return nil, clientapi.ErrUnavailable
	}, func(ctx context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		if len(delays) == 7 {
			cancel()
			return ctx.Err()
		}
		return nil
	}, func(tea.Msg) error { return nil })
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, 32 * time.Second}
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(delays, want) || dials != 7 || peer.closed.Load() != 1 {
		t.Fatalf("backoff=%v dials=%d error=%v", delays, dials, err)
	}
	if err := waitRetry(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled backoff did not end")
	}
}
