// SPDX-License-Identifier: Apache-2.0

// Package boot owns Core's ordered start, recovery and reverse shutdown.
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/backup"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/ghclient"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/recover"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider/applefm"
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
	// Lifecycle shares the locked restart controller with composed services.
	// External calls must acquire their activity through Config.Activities.
	Lifecycle func(*lifecycle.Restarter)
}
type Config struct {
	Work *WorkConfig
	// SandboxLauncher pins an optional trusted absolute OS launcher.
	SandboxLauncher string
	// Client supplies the installed person's agents, supported rows and handlers.
	// Boot always supplies the durable main id, epoch and recovery state itself.
	Client       clientsrv.Identity
	Conversation *modelpath.ResponsesConfig
	// Local is the on-device helper, when the person configured one.
	Local *modelpath.LocalConfig
	// SignInTransport is an in-process test seam; production uses the fixed issuer.
	SignInTransport http.RoundTripper
	// ChannelTransport is an in-process platform test seam; production uses the fixed Telegram and Discord origins.
	ChannelTransport http.RoundTripper
	// WebTransport is an in-process test seam; production uses the fixed Firecrawl origin.
	WebTransport http.RoundTripper
	// ClientManifest selects the build manifest containing supportedCombinations.
	// An explicit path must exist and contain a valid table, including an empty
	// table when every client combination is intentionally unsupported.
	ClientManifest string
	StateRoot      string
	Socket         string
	Tailnet        *clientsrv.TailnetConfig
	PeerSeenMax    uint64
	Services       Services
	Activities     *lifecycle.ActivityRegistry
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
	if cfg.ClientManifest != "" {
		file, err := os.Open(cfg.ClientManifest)
		if err != nil {
			return fmt.Errorf("open client build manifest: %w", err)
		}
		rows, readErr := manifest.ReadCombinations(file)
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return fmt.Errorf("read client build manifest: %w", err)
		}
		cfg.Client.Supported = rows
	}
	if cfg.Client.Supported != nil {
		if err := manifest.ValidateCombinations(cfg.Client.Supported); err != nil {
			return err
		}
	}
	if (cfg.Services.PeekEpoch == nil) != (cfg.Services.OpenCanon == nil) {
		return errors.New("canon requires both read-only peek and writer open")
	}
	socket, err := filepath.Abs(cfg.Socket)
	if err != nil {
		return err
	}
	cfg.Socket = socket
	// An older binary must refuse before creating a lock, epoch or sentinel.
	// Custom canon services own their own compatibility check.
	if cfg.Conversation != nil || cfg.Services.OpenCanon == nil {
		if err := canon.CheckVersion(ctx, conversationPath(cfg.StateRoot), len(ApplicationMigrations())); err != nil {
			return err
		}
	}
	store, err := ctlfiles.Open(cfg.StateRoot)
	if err != nil {
		return err
	}
	var stack []func(context.Context) error
	sentinelWritten := false
	plannedRestart := false
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
		if result == nil && plannedRestart {
			result = lifecycle.ErrRestart
		}
	}()
	snapshot := Snapshot{State: Starting}
	boundary := func(step Step) error {
		if cfg.AfterStep != nil {
			if err := cfg.AfterStep(step, snapshot); err != nil {
				return err
			}
		}
		// Once bind makes the socket connectable, finish retiring the previous
		// socket and publishing ownership before honoring cancellation. Clearing
		// the sentinel earlier could discard the previous socket's recovery path.
		if step == SocketCreated || step == SocketBound {
			return nil
		}
		return ctx.Err()
	}
	if err := boundary(RootValidated); err != nil {
		return err
	}
	if err := store.Acquire(); err != nil {
		return err
	}
	if err := recover.UpdateStart(store, cfg.StateRoot, cfg.Socket); err != nil {
		return err
	}
	if err := backup.CheckRestore(cfg.StateRoot); err != nil {
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
	ownsConversation := cfg.Conversation != nil || cfg.Services.OpenCanon == nil
	if !ownsConversation && cfg.Services.PeekEpoch == nil {
		_, statErr := os.Lstat(conversationPath(cfg.StateRoot))
		if statErr == nil {
			ownsConversation = true
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	var highest uint64
	if ownsConversation {
		if _, statErr := os.Lstat(conversationPath(cfg.StateRoot)); errors.Is(statErr, os.ErrNotExist) {
			entries, readErr := os.ReadDir(filepath.Dir(conversationPath(cfg.StateRoot)))
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return readErr
			}
			if len(entries) != 0 {
				return errors.New("existing canon requires a canon service")
			}
		}
		// Repeat under the lock before minting, in case another Core migrated
		// between the initial read-only check and acquiring the lock.
		if err := canon.CheckVersion(ctx, conversationPath(cfg.StateRoot), len(ApplicationMigrations())); err != nil {
			return err
		}
		highest, err = canon.PeekEpoch(ctx, conversationPath(cfg.StateRoot))
	} else if cfg.Services.PeekEpoch != nil {
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
	activities := cfg.Activities
	if activities == nil {
		activities = lifecycle.NewRegistry(nil)
	}
	restarter, err := lifecycle.OpenRestarter(store, snapshot.Epoch, activities)
	if err != nil {
		return err
	}
	if cfg.Services.Lifecycle != nil {
		cfg.Services.Lifecycle(restarter)
	}
	var mergeAuthority *authority.Merges
	var mergeAgentExists func(context.Context, string) bool
	var canon Canon
	var conversation *conversationRuntime
	if ownsConversation && len(recovery.Halt) == 0 {
		runtime, openErr := startConversation(ctx, cfg.StateRoot, recovery.MainID, snapshot.Epoch, store, cfg.Conversation, activities, cfg.Client.PersonID, cfg.SignInTransport, cfg.ChannelTransport)
		if openErr != nil {
			return openErr
		}
		conversation = runtime
		if cfg.Local != nil {
			helper, helperErr := applefm.New(applefm.Settings{Helper: cfg.Local.Helper, Model: cfg.Local.Model, PrivateCloud: cfg.Local.PrivateCloud, Env: cfg.Local.Env})
			if helperErr != nil {
				return helperErr
			}
			held, holdErr := activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "on-device model"})
			if holdErr != nil {
				return holdErr
			}
			profile, probeErr := helper.Probe(ctx, held)
			held.Release()
			if probeErr != nil || profile.Model != cfg.Local.Model || profile.PrivateCloud != cfg.Local.PrivateCloud {
				return errors.New("on-device model unavailable")
			}
			runtime.onDevice = helper
			runtime.onDeviceProfile = profile
		}
		runtime.replaySocket = cfg.Socket
		runtime.webTransport = cfg.WebTransport
		supported := cfg.Client.Supported
		cfg.Client = runtime.identity
		cfg.Client.Supported = supported
		canon = runtime.db
		mergeAuthority = authority.NewMerges(runtime.db, snapshot.Epoch)
		mergeAgentExists = func(ctx context.Context, id string) bool { _, err := runtime.agents.Get(ctx, id); return err == nil }
		stack = append(stack, runtime.Close)
	} else if cfg.Services.OpenCanon != nil {
		canon, err = cfg.Services.OpenCanon(ctx, snapshot.Epoch)
		if err != nil {
			return err
		}
		if canon == nil {
			return errors.New("canon writer is nil")
		}
		stack = append(stack, canon.Close)
	}
	github, err := ghclient.New(ctx, ghclient.Config{Leaser: activities})
	if err != nil {
		return err
	}
	stack = append(stack, github.Close)
	if err := addOperations(&cfg.Client, clientsrv.GitHubOperations(githubService{client: github, merges: mergeAuthority, agentExists: mergeAgentExists})); err != nil {
		return err
	}
	if conversation != nil {
		if err := addOperations(&cfg.Client, localModelOperation(conversation)); err != nil {
			return err
		}
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
		identity := cfg.Client
		identity.MainID, identity.Epoch, identity.Recovering = recovery.MainID, strconv.FormatUint(snapshot.Epoch, 10), recovering.Load
		identity.Restart = restarter
		done <- clientsrv.Serve(serving, listener, identity)
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
	var remoteDone chan error
	if cfg.Tailnet != nil {
		remote, err := clientsrv.ListenTailnet(serving, *cfg.Tailnet)
		if err != nil {
			return err
		}
		remoteDone = make(chan error, 1)
		identity := cfg.Client
		identity.MainID, identity.Epoch, identity.Recovering = recovery.MainID, strconv.FormatUint(snapshot.Epoch, 10), recovering.Load
		identity.Restart = restarter
		go func() { remoteDone <- clientsrv.Serve(serving, remote, identity) }()
		stack = append(stack, func(phase context.Context) error {
			cancelServe()
			select {
			case err := <-remoteDone:
				return err
			case <-phase.Done():
				return phase.Err()
			}
		})
	}
	if err := boundary(SocketsBound); err != nil {
		// Ownership is now durable and the listener is on the shutdown stack.
		// A signal here is an orderly stop, even before the first handshake.
		if err == context.Canceled && ctx.Err() == context.Canceled {
			return nil
		}
		return err
	}
	var workDone <-chan struct{}
	if conversation != nil && cfg.Work != nil {
		if err := conversation.startWork(ctx, cfg.Work); err != nil {
			if errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled {
				return nil
			}
			return err
		}
		workDone = conversation.workEngine.done
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
	box, closeProcesses := startSandbox(ctx, cfg.StateRoot, cfg.Socket, cfg.SandboxLauncher)
	stack = append(stack, closeProcesses)
	snapshot.Sandbox = box.Status()
	if cfg.Client.Topics != nil {
		available := snapshot.Sandbox.Available
		data, marshalErr := json.Marshal(protocol.Connected{ShellFileToolsAvailable: &available})
		if marshalErr != nil {
			return marshalErr
		}
		if err := cfg.Client.Topics.Publish("core", clientsrv.TopicEvent{Kind: protocol.KindConnected, Key: "shell-file-tools", Data: data}); err != nil {
			return err
		}
	}

	if cfg.Services.SandboxReady != nil {
		cfg.Services.SandboxReady(box)
	}
	if len(recovery.Halt) == 0 && ctx.Err() == nil {
		if err := recover.UpdateReady(store); err != nil {
			return err
		}
		if err := restarter.Ready(); err != nil {
			return err
		}
		snapshot.State = Running
		recovering.Store(false)
		// Cancellation at READY is an orderly stop, including in test observers.
		if err := boundary(Ready); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	restartContext, stopRestart := context.WithCancel(ctx)
	restartDone := make(chan error, 1)
	go func() { restartDone <- restarter.Run(restartContext) }()
	// Join the observer before closing any of the stores it uses.
	defer func() { stopRestart(); <-restartDone }()
	select {
	case <-workDone:
		if ctx.Err() != nil {
			return nil
		}
		if conversation.workEngine.err != nil {
			return conversation.workEngine.err
		}
		return errors.New("work service stopped before shutdown")
	case <-ctx.Done():
		return nil
	case err := <-restartDone:
		restartDone <- err
		if errors.Is(err, lifecycle.ErrRestart) {
			plannedRestart = true
			return nil
		}
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil
		}
		return err
	case err := <-remoteDone:
		remoteDone <- err
		if err == nil && ctx.Err() == nil {
			return errors.New("tailnet client service stopped before shutdown")
		}
		return err
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
			if _, err := store.ReadRestart(); err != nil {
				return recovery, err
			}
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
