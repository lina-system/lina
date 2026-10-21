// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/lease"
)

var (
	ErrPending = errors.New("a restart with another mode is already owed")
	ErrRestart = errors.New("planned Core restart")
)

// RestartStore is the locked control-file owner, never a canon/backup store.
// Its owner must serialize other control-file operations with this service.
type RestartStore interface {
	Locked() bool
	Epoch() (uint64, error)
	ReadRestart() (*ctlfiles.RestartRequest, error)
	WriteRestart(ctlfiles.RestartRequest) error
}

// Restarter owns a durable request and the admission fence for one Core epoch.
// A write failure latches this service: an uncertain fsync is never acknowledged
// or followed by more work that could overwrite its recovery record.
type Restarter struct {
	mu       sync.Mutex
	store    RestartStore
	epoch    uint64
	registry *ActivityRegistry
	request  ctlfiles.RestartRequest
	token    Token
	ready    bool
	failure  error
}

func OpenRestarter(store RestartStore, epoch uint64, registry *ActivityRegistry) (*Restarter, error) {
	if store == nil || !store.Locked() || epoch == 0 || registry == nil || registry.active == nil {
		return nil, errors.New("restart requires the locked current Core and activity registry")
	}
	current, err := store.Epoch()
	if err != nil {
		return nil, err
	}
	if current != epoch {
		return nil, errors.New("restart epoch differs from control epoch")
	}
	request, err := store.ReadRestart()
	if err != nil {
		return nil, err
	}
	x := &Restarter{store: store, epoch: epoch, registry: registry}
	if request != nil {
		for _, text := range []string{request.Epoch, request.CommittedEpoch, request.DischargedEpoch} {
			if text == "" {
				continue
			}
			n, err := strconv.ParseUint(text, 10, 64)
			if err != nil || n > epoch {
				return nil, errors.New("restart record is ahead of Core epoch")
			}
		}
		x.request = *request
	}
	return x, nil
}

// Status is a copy of the durable receipt. An empty ID means no request.
func (x *Restarter) Status() ctlfiles.RestartRequest {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.request
}

// HoldReply prevents a requested restart from closing the client connection
// before its durable acknowledgment has been written. The transport releases
// this lease on both successful writes and disconnects.
func (x *Restarter) HoldReply(ctx context.Context) (lease.Lease, error) {
	return x.registry.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "restart acknowledgment"})
}

func (x *Restarter) save(request ctlfiles.RestartRequest) error {
	if x.failure != nil {
		return x.failure
	}
	if err := x.store.WriteRestart(request); err != nil {
		x.failure = err
		return err
	}
	x.request = request
	return nil
}

// Request persists before acknowledging. Repeating the pending mode returns
// the same request; changing it cannot silently escalate when_idle to drain.
func (x *Restarter) Request(ctx context.Context, mode string) (ctlfiles.RestartRequest, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ctlfiles.RestartRequest{}, err
	}
	if x.failure != nil {
		return ctlfiles.RestartRequest{}, x.failure
	}
	if mode == "" {
		mode = "when_idle"
	}
	if mode != "when_idle" && mode != "drain" {
		return ctlfiles.RestartRequest{}, errors.New("invalid restart mode")
	}
	if x.request.ID != "" && x.request.Owed() {
		if x.request.Mode != mode {
			return ctlfiles.RestartRequest{}, ErrPending
		}
		return x.request, nil
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return ctlfiles.RestartRequest{}, err
	}
	request := ctlfiles.RestartRequest{Schema: 1, ID: hex.EncodeToString(id[:]), Mode: mode, Epoch: strconv.FormatUint(x.epoch, 10)}
	if err := x.save(request); err != nil {
		return ctlfiles.RestartRequest{}, err
	}
	x.registry.Notify()
	return request, nil
}

// Ready is called only after startup reconciliation succeeds. A crash before
// commitment does not discharge the request. A later READY epoch discharges a
// committed restart, even if an intervening startup crashed during recovery.
func (x *Restarter) Ready() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.failure != nil {
		return x.failure
	}
	request := x.request
	if request.ID != "" && request.Owed() && request.CommittedEpoch != "" {
		committed, _ := strconv.ParseUint(request.CommittedEpoch, 10, 64)
		if committed < x.epoch {
			request.DischargedEpoch = strconv.FormatUint(x.epoch, 10)
			if err := x.save(request); err != nil {
				return err
			}
		}
	}
	x.ready = true
	x.registry.Notify()
	return nil
}

// Step checks one lifecycle edge. when_idle gives admission back while busy;
// drain holds it closed and waits for the existing Hard and Live leases. Both
// modes wait indefinitely for Unknown and never cancel an existing call.
// A true result requires orderly shutdown followed by exit 75.
func (x *Restarter) Step(ctx context.Context) (bool, IdleReport, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, IdleReport{State: Unknown, Err: err}, err
	}
	if x.failure != nil {
		return false, IdleReport{State: Unknown, Err: x.failure}, x.failure
	}
	if !x.ready || x.request.ID == "" || !x.request.Owed() {
		return false, IdleReport{}, nil
	}
	var report IdleReport
	if x.token.attempt == nil {
		x.token, report = x.registry.Prepare(ctx)
	} else {
		report = x.registry.Idle(ctx)
	}
	if report.Busy() {
		if x.request.Mode == "when_idle" {
			x.registry.Cancel(x.token)
			x.token = Token{}
		}
		return false, report, nil
	}
	if err := x.registry.Commit(x.token); err != nil {
		if errors.Is(err, ErrBusy) {
			if x.request.Mode == "when_idle" {
				x.registry.Cancel(x.token)
				x.token = Token{}
			}
			return false, x.registry.Idle(ctx), nil
		}
		return false, report, err
	}
	request := x.request
	request.CommittedEpoch = strconv.FormatUint(x.epoch, 10)
	if err := x.save(request); err != nil {
		return false, report, err
	}
	return true, report, nil
}

// Run is the sole lifecycle observer. Notifications coalesce, so it always
// rechecks activity. There is no polling deadline or timeout-based forced exit.
func (x *Restarter) Run(ctx context.Context) error {
	for {
		exit, _, err := x.Step(ctx)
		if err != nil {
			return err
		}
		if exit {
			return ErrRestart
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-x.registry.Edges():
		}
	}
}
