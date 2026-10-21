// SPDX-License-Identifier: Apache-2.0

// Package boot owns Core's ordered start, recovery and reverse shutdown.
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/sandbox"
)

type Step uint8

const (
	RootValidated Step = iota + 1
	Locked
	EventsOpened
	ControlsRead
	EpochMinted
	CanonOpened
	EpochMirrored
	SocketPending
	SocketCreated
	SocketBound
	SocketsBound
	Reconciled
	Ready
)

type State string

const (
	Starting   State = "STARTING"
	Recovering State = "RECOVERING"
	Running    State = "READY"
)

type Snapshot struct {
	Epoch   uint64
	State   State
	Sandbox sandbox.Status
}
type Sentinel struct {
	Epoch         string `json:"epoch"`
	Socket        string `json:"socket"`
	PendingSocket string `json:"pending_socket,omitempty"`
}
type Recovery struct {
	MainID    string
	Epoch     uint64
	Previous  *Sentinel
	Halt      json.RawMessage
	Lifecycle json.RawMessage
}

// Events starts with a null epoch. SetEpoch runs only after the durable mint.
// Service Close methods must finish within the supplied context's deadline.
type Events interface {
	SetEpoch(uint64) error
	Close(context.Context) error
}
type Canon interface {
	MirrorEpoch(context.Context, uint64) error
	Close(context.Context) error
}

// Services are the integration points for the event writer, canon and recovery
// implementations. PeekEpoch must only read canon, without opening its writer.
// An absent canon implementation is allowed only for an empty canon directory.
type Services struct {
	OpenEvents func(context.Context) (Events, error)
	PeekEpoch  func(context.Context) (uint64, error)
	OpenCanon  func(context.Context, uint64) (Canon, error)
	Reconcile  func(context.Context, Recovery) error
	// SandboxReady receives the same probed wrapper used by shell/file handlers.
	// A failed probe disables those handlers only; it does not stop recovery.
	SandboxReady func(*sandbox.Sandbox)
}
type Config struct {
	StateRoot   string
	Socket      string
	PeerSeenMax uint64
	Services    Services
	// AfterStep observes a completed boundary. It is used by deterministic crash
	// tests and is never configured through command arguments or the environment.
	AfterStep func(Step, Snapshot) error
}

// Run owns one Core for an installed state root, until cancellation. It never
// creates an identity: installation must supply system/core/main.json.
func Run(ctx context.Context, cfg Config) (result error) {
	if cfg.StateRoot == "" || cfg.Socket == "" {
		return errors.New("state root and socket are required")
	}
	if (cfg.Services.PeekEpoch == nil) != (cfg.Services.OpenCanon == nil) {
		return errors.New("canon requires both read-only peek and writer open")
	}
	socket, err := filepath.Abs(cfg.Socket)
	if err != nil {
		return err
	}
	cfg.Socket = socket
	store, err := ctlfiles.Open(cfg.StateRoot)
	if err != nil {
		return err
	}
	var stack []func(context.Context) error
	sentinelWritten := false
	defer func() {
		for i := len(stack) - 1; i >= 0; i-- {
			phase, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			result = errors.Join(result, stack[i](phase))
			cancel()
		}
		if result == nil && sentinelWritten {
			result = store.RemoveSentinel()
		}
		result = errors.Join(result, store.Close())
	}()
	snapshot := Snapshot{State: Starting}
	boundary := func(step Step) error {
		if cfg.AfterStep != nil {
			if err := cfg.AfterStep(step, snapshot); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	if err := boundary(RootValidated); err != nil {
		return err
	}
	if err := store.Acquire(); err != nil {
		return err
	}
	if err := boundary(Locked); err != nil {
		return err
	}
	var events Events
	if cfg.Services.OpenEvents != nil {
		events, err = cfg.Services.OpenEvents(ctx)
		if err != nil {
			return err
		}
		if events == nil {
			return errors.New("event writer is nil")
		}
		stack = append(stack, events.Close)
	}
	if err := boundary(EventsOpened); err != nil {
		return err
	}
	recovery, err := readControls(store)
	if err != nil {
		return err
	}
	if err := boundary(ControlsRead); err != nil {
		return err
	}
	var highest uint64
	if cfg.Services.PeekEpoch != nil {
		highest, err = cfg.Services.PeekEpoch(ctx)
	} else {
		// Never silently ignore an existing database when its implementation has
		// not yet been linked into this Core increment.
		var entries []os.DirEntry
		entries, err = os.ReadDir(filepath.Join(cfg.StateRoot, "canon"))
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		if len(entries) != 0 {
			err = errors.New("existing canon requires a canon service")
		}
	}
	if err != nil {
		return err
	}
	snapshot.Epoch, err = store.MintEpoch(highest, cfg.PeerSeenMax)
	if err != nil {
		return err
	}
	recovery.Epoch = snapshot.Epoch
	if events != nil {
		if err := events.SetEpoch(snapshot.Epoch); err != nil {
			return err
		}
	}
	// Starting a run does not establish ownership of the requested socket.
	// Keep both previous paths until a new listener has actually bound. A second
	// interrupted start must not lose an earlier run's uncompleted bind intent.
	ownedSocket, pendingSocket := "", ""
	if recovery.Previous != nil {
		ownedSocket = recovery.Previous.Socket
		pendingSocket = recovery.Previous.PendingSocket
	}
	writeSentinel := func(socket, pending string) error {
		sentinel, err := json.Marshal(Sentinel{Epoch: strconv.FormatUint(snapshot.Epoch, 10), Socket: socket, PendingSocket: pending})
		if err != nil {
			return err
		}
		return store.Write("run.sentinel", sentinel)
	}
	if err := writeSentinel(ownedSocket, pendingSocket); err != nil {
		return err
	}
	sentinelWritten = true
	if err := boundary(EpochMinted); err != nil {
		return err
	}
	var canon Canon
	if cfg.Services.OpenCanon != nil {
		canon, err = cfg.Services.OpenCanon(ctx, snapshot.Epoch)
		if err != nil {
			return err
		}
		if canon == nil {
			return errors.New("canon writer is nil")
		}
		stack = append(stack, canon.Close)
	}
	if err := boundary(CanonOpened); err != nil {
		return err
	}
	if canon != nil {
		if err := canon.MirrorEpoch(ctx, snapshot.Epoch); err != nil {
			return err
		}
	}
	if err := boundary(EpochMirrored); err != nil {
		return err
	}
	// Validate the owner before either deleting a previous socket or binding.
	if err := clientsrv.PrepareSocketDirectory(cfg.Socket); err != nil {
		return err
	}
	if err := reclaimSocket(cfg.Socket, recovery.Previous); err != nil {
		return err
	}
	// Never record intent for an already occupied target. Otherwise a failed
	// start could authorize reclaiming an unrelated stale socket on the next run.
	if _, err := os.Lstat(cfg.Socket); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return fmt.Errorf("inspect socket path: %w", err)
		}
		return errors.New("socket path already exists")
	}
	// Retire an interrupted bind at another path before replacing its intent,
	// keeping the previous owned path available if this path change fails.
	if pendingSocket != "" && pendingSocket != cfg.Socket {
		if err := clientsrv.PrepareSocketDirectory(pendingSocket); err != nil {
			return err
		}
		if err := reclaimSocket(pendingSocket, recovery.Previous); err != nil {
			return err
		}
	}
	if err := writeSentinel(ownedSocket, cfg.Socket); err != nil {
		return err
	}
	if err := boundary(SocketPending); err != nil {
		return err
	}
	listener, err := (clientsrv.ListenConfig{AfterBind: func() error {
		return boundary(SocketCreated)
	}}).Listen(cfg.Socket)
	if err != nil {
		// A competing binder may have occupied the path after the preflight.
		// A known failed bind must not grant ownership on a subsequent start.
		return errors.Join(err, writeSentinel(ownedSocket, ""))
	}
	if err := boundary(SocketBound); err != nil {
		return errors.Join(err, listener.Close())
	}
	// Retire the previous owned path only after the replacement has bound, and
	// before publishing it. Until retirement succeeds, the durable sentinel
	// still names both paths so an interrupted change can recover either one.
	if ownedSocket != "" && ownedSocket != cfg.Socket {
		if err := clientsrv.PrepareSocketDirectory(ownedSocket); err != nil {
			return errors.Join(err, listener.Close())
		}
		if err := reclaimSocket(ownedSocket, recovery.Previous); err != nil {
			return errors.Join(err, listener.Close())
		}
	}
	if err := writeSentinel(cfg.Socket, ""); err != nil {
		return errors.Join(err, listener.Close())
	}
	snapshot.State = Recovering
	var recovering atomic.Bool
	recovering.Store(true)
	serving, cancelServe := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- clientsrv.Serve(serving, listener, clientsrv.Identity{MainID: recovery.MainID, Epoch: strconv.FormatUint(snapshot.Epoch, 10), Recovering: recovering.Load})
	}()
	stack = append(stack, func(phase context.Context) error {
		cancelServe()
		select {
		case err := <-done:
			return err
		case <-phase.Done():
			return phase.Err()
		}
	})
	if err := boundary(SocketsBound); err != nil {
		return err
	}
	if cfg.Services.Reconcile != nil {
		if err := cfg.Services.Reconcile(ctx, recovery); err != nil {
			return err
		}
	}
	if err := boundary(Reconciled); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	box, closeProcesses := startSandbox(ctx, cfg.StateRoot, cfg.Socket)
	stack = append(stack, closeProcesses)
	snapshot.Sandbox = box.Status()
	if cfg.Services.SandboxReady != nil {
		cfg.Services.SandboxReady(box)
	}
	if len(recovery.Halt) == 0 && ctx.Err() == nil {
		snapshot.State = Running
		recovering.Store(false)
		// Cancellation at READY is an orderly stop, including in test observers.
		if err := boundary(Ready); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		// Leave one result for the reverse shutdown stack.
		done <- err
		if err == nil && ctx.Err() == nil {
			return errors.New("client service stopped before shutdown")
		}
		return err
	}
}

func readControls(store *ctlfiles.Store) (Recovery, error) {
	var recovery Recovery
	data, err := store.Read("main.json")
	if err != nil {
		return recovery, fmt.Errorf("read installed main identity: %w", err)
	}
	var main struct {
		ID string `json:"main_id"`
	}
	if err := json.Unmarshal(data, &main); err != nil {
		return recovery, err
	}
	if main.ID == "" || strings.TrimSpace(main.ID) != main.ID || len(main.ID) > 256 {
		return recovery, errors.New("invalid installed main identity")
	}
	recovery.MainID = main.ID
	for _, name := range []string{"run.sentinel", "halt.json", "lifecycle.json"} {
		data, err := store.Read(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return recovery, err
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil || object == nil {
			return recovery, fmt.Errorf("invalid %s record", name)
		}
		switch name {
		case "run.sentinel":
			var previous Sentinel
			if err := json.Unmarshal(data, &previous); err != nil {
				return recovery, err
			}
			epoch, err := strconv.ParseUint(previous.Epoch, 10, 64)
			_, socketPresent := object["socket"]
			pending, pendingPresent := object["pending_socket"]
			if err != nil || strconv.FormatUint(epoch, 10) != previous.Epoch || epoch == 0 ||
				!socketPresent || strings.TrimSpace(string(object["socket"])) == "null" ||
				(previous.Socket != "" && !filepath.IsAbs(previous.Socket)) ||
				(pendingPresent && strings.TrimSpace(string(pending)) == "null") ||
				(previous.PendingSocket != "" && !filepath.IsAbs(previous.PendingSocket)) {
				return recovery, errors.New("invalid run sentinel")
			}
			recovery.Previous = &previous
		case "halt.json":
			recovery.Halt = data
		case "lifecycle.json":
			recovery.Lifecycle = data
		}
	}
	return recovery, nil
}

// Only a socket owned or attempted by this root's previous run may be reclaimed,
// while holding its lock. Regular files, symlinks and live listeners are preserved.
func reclaimSocket(path string, previous *Sentinel) error {
	if previous == nil || (previous.Socket != path && previous.PendingSocket != path) {
		return nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode().Type() != os.ModeSocket || info.Mode().Perm() != 0600 {
		return errors.New("previous socket path is not a private socket")
	}
	conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err == nil {
		conn.Close()
		return errors.New("previous socket is still in use")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("probe previous socket: %w", err)
	}
	return os.Remove(path)
}
