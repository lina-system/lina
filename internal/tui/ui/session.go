// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

// connectionPeer is the UI's lifecycle boundary, not a transport. Its adapter
// must use the client service's generated/validated receive API. Close must
// unblock Receive. No protocol encoding, socket or request retry lives here.
type connectionPeer interface {
	Welcome() protocol.Welcome
	Receive(context.Context) (protocol.Envelope, error)
	Close() error
}

type connectionDial func(context.Context) (connectionPeer, error)
type connectionWait func(context.Context, time.Duration) error

type publicationError struct{ error }

func (e publicationError) Unwrap() error { return e.error }

func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// connectionLoop serializes receives and bounded output: a slow renderer can
// delay publication but no frame is silently dropped. Retry opens a fresh
// connection only; it never repeats an application request or starts Core.
func connectionLoop(ctx context.Context, dial connectionDial, wait connectionWait, publish func(tea.Msg) error) error {
	generation, attempt := uint64(1), uint64(0)
	var mainID, epoch string
	show := func(status ConnectionStatus) error {
		return publish(shellConnection{Generation: generation, Status: status})
	}
	if err := show(ConnectionStatus{State: Connecting}); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		peer, err := dial(ctx)
		if ctx.Err() != nil {
			if peer != nil {
				peer.Close()
			}
			return ctx.Err()
		}
		if err == nil && peer == nil {
			err = errors.New("missing client connection")
		}
		if err != nil && peer != nil {
			peer.Close()
		}
		if err != nil {
			if mainID == "" || !errors.Is(err, clientapi.ErrUnavailable) {
				return errors.Join(err, show(connectionFailure(err)))
			}
		} else {
			welcome := peer.Welcome()
			number, epochErr := strconv.ParseUint(welcome.Epoch, 10, 64)
			cause := protocol.ErrorCode("")
			switch {
			case epochErr != nil || strconv.FormatUint(number, 10) != welcome.Epoch || welcome.MainID == "":
				cause = protocol.ErrorMalformed
			case mainID != "" && mainID != welcome.MainID:
				cause = protocol.ErrorNotAuthorized
			case epoch != "":
				previous, _ := strconv.ParseUint(epoch, 10, 64)
				if number < previous {
					cause = protocol.ErrorStaleEpoch
				}
			}
			if cause != "" {
				peer.Close()
				err := &clientapi.Refusal{Cause: protocol.Error{Code: cause}}
				return errors.Join(err, show(ConnectionStatus{State: Failed, Cause: cause}))
			}
			if epoch != "" && epoch != welcome.Epoch {
				notice := shellData{Generation: generation, Message: state.Notice{ID: "epoch:" + welcome.Epoch, Code: "epoch_changed"}}
				if err := publish(notice); err != nil {
					peer.Close()
					return err
				}
			}
			mainID, epoch = welcome.MainID, welcome.Epoch
			if err := show(ConnectionStatus{State: Live}); err != nil {
				peer.Close()
				return err
			}
			attempt = 0
			err = receiveConnection(ctx, peer, generation, publish)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var displayError publicationError
			if errors.As(err, &displayError) {
				return displayError.error
			}
			var network net.Error
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && !errors.As(err, &network) {
				return errors.Join(err, show(connectionFailure(err)))
			}
		}
		attempt++
		generation++
		delay := time.Second << min(attempt-1, 5)
		if err := publish(shellBegin{Generation: generation}); err != nil {
			return err
		}
		if err := show(ConnectionStatus{State: Retrying, Cause: protocol.ErrorUnavailable, Attempt: attempt, NextTry: delay}); err != nil {
			return err
		}
		if err := wait(ctx, delay); err != nil {
			return err
		}
	}
}

func connectionFailure(err error) ConnectionStatus {
	status := ConnectionStatus{State: Failed, Cause: protocol.ErrorMalformed}
	var refusal *clientapi.Refusal
	switch {
	case errors.Is(err, clientapi.ErrUnavailable):
		status.State, status.Cause = Stopped, protocol.ErrorUnavailable
	case errors.As(err, &refusal):
		status.State, status.Cause = Refused, refusal.Cause.Code
	}
	return status
}

func receiveConnection(ctx context.Context, peer connectionPeer, generation uint64, publish func(tea.Msg) error) (err error) {
	var closeOnce sync.Once
	closePeer := func() { closeOnce.Do(func() { peer.Close() }) }
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { closePeer(); close(interrupted) })
	defer func() {
		if !stop() {
			<-interrupted
		}
		closePeer()
	}()
	for {
		envelope, err := peer.Receive(ctx)
		if err != nil {
			return err
		}
		data, err := json.Marshal(envelope)
		if err != nil {
			return err
		}
		if err := publish(shellData{Generation: generation, Message: state.Frame{Data: data}}); err != nil {
			return publicationError{err}
		}
	}
}
