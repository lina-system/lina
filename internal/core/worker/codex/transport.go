// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

const maxFrame = 16 << 20

var generations atomic.Uint64
var ErrUnsupported = errors.New("Codex capability not implemented")

// Config selects the installed executable, never its settings or credentials.
// Empty Executable resolves codex on the person's PATH.
type Config struct{ Executable string }

type Compatibility struct {
	Version          string
	Verified         bool
	VerifiedVersions []string
}

type frame struct {
	ID     json.RawMessage         `json:"id,omitempty"`
	Method string                  `json:"method,omitempty"`
	Params json.RawMessage         `json:"params,omitempty"`
	Result json.RawMessage         `json:"result,omitempty"`
	Error  *wire.JSONRPCErrorError `json:"error,omitempty"`
}

type Session struct {
	owner                   *proc.Owner
	cancel                  context.CancelFunc
	stdin                   *os.File
	processDone, readerDone chan struct{}
	events                  chan worker.Event
	quotaUpdates            chan struct{}
	lastQuota               json.RawMessage
	control                 sync.Mutex
	writeMu                 sync.Mutex
	mu                      sync.Mutex
	pending                 map[string]chan frame
	next                    uint64
	revision                uint64
	failure                 error
	closing                 bool
	closeOnce               sync.Once
	version                 string
	generation              uint64
	snapshot                worker.ThreadSnapshot
	started                 bool
	finished                map[string]bool
}

var _ worker.Adapter = (*Session)(nil)

func executable(name string) (string, error) {
	if name != "" {
		if !filepath.IsAbs(name) {
			return "", errors.New("Codex executable must be absolute")
		}
		return name, nil
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, "codex")
		st, err := os.Stat(p)
		if err == nil && st.Mode().IsRegular() && st.Mode()&0111 != 0 {
			return p, nil
		}
	}
	return "", errors.New("installed codex not found on PATH")
}

// Open initializes one owned stdio session. ctx is the session lifetime, not a
// turn deadline. Individual RPC contexts never cancel that lifetime.
func Open(ctx context.Context, l lease.Lease, cfg Config) (*Session, error) {
	path, err := executable(cfg.Executable)
	if err != nil {
		return nil, err
	}
	owner := proc.NewOwner()
	env := proc.WorkerEnvironment()
	var version bytes.Buffer
	probeCtx, stopProbe := context.WithTimeout(ctx, 10*time.Second)
	err = owner.Run(probeCtx, l, proc.Command{Path: path, Args: []string{"--version"}, Env: env, Stdout: &version, Stderr: io.Discard})
	stopProbe()
	if err != nil {
		owner.Close(context.Background())
		return nil, fmt.Errorf("Codex version: %w", err)
	}
	v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(version.String()), "codex-cli "))
	if v == "" || len(v) > 256 {
		owner.Close(context.Background())
		return nil, errors.New("invalid Codex version")
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	life, cancel := context.WithCancel(ctx)
	s := &Session{owner: owner, cancel: cancel, stdin: inW, processDone: make(chan struct{}), readerDone: make(chan struct{}), events: make(chan worker.Event, 257), quotaUpdates: make(chan struct{}, 1), pending: map[string]chan frame{}, finished: map[string]bool{}, version: v, generation: generations.Add(1)}
	go func() {
		defer close(s.processDone)
		err := owner.Run(life, l, proc.Command{Path: path, Args: []string{"app-server", "--listen", "stdio://"}, Env: env, Stdin: inR, Stdout: outW, Stderr: io.Discard})
		inR.Close()
		outW.Close()
		if err != nil {
			s.fail(fmt.Errorf("Codex process: %w", err))
		}
	}()
	go s.read(outR)
	handshake, stopHandshake := context.WithTimeout(ctx, 15*time.Second)
	defer stopHandshake()
	var initialized wire.InitializeResponse
	yes := true
	err = s.call(handshake, string(wire.ClientRequestInitializeMethodInitialize), wire.InitializeParams{ClientInfo: wire.ClientInfo{Name: "lina", Version: "1"}, Capabilities: &wire.InitializeCapabilities{ExperimentalApi: &yes}}, &initialized)
	if err == nil && initialized.UserAgent == "" {
		err = errors.New("initialize omitted userAgent")
	}
	if err == nil {
		err = s.write(handshake, wire.ClientNotification{Initialized: &wire.ClientNotificationInitialized{}})
	}
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("Codex initialize: %w", err)
	}
	return s, nil
}

func (s *Session) Name() string                                { return "codex" }
func (s *Session) Version(ctx context.Context) (string, error) { return s.version, ctx.Err() }
func (s *Session) Compatibility() Compatibility {
	c := Compatibility{Version: s.version, VerifiedVersions: VerifiedVersions()}
	for _, v := range c.VerifiedVersions {
		c.Verified = c.Verified || v == s.version
	}
	return c
}
func (s *Session) Capabilities() worker.CapabilitySet {
	return worker.CapabilitySet{worker.StreamProgress, worker.ReportUsage, worker.SignalCompletion, worker.SteerTurn, worker.ReportQuota}
}
func (s *Session) Events() <-chan worker.Event { return s.events }

func (s *Session) write(ctx context.Context, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(raw) > maxFrame {
		return errors.New("Codex request exceeds frame limit")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = s.stdin.SetWriteDeadline(deadline); err != nil {
		return err
	}
	_, err = s.stdin.Write(append(raw, '\n'))
	return err
}
func (s *Session) call(ctx context.Context, method string, params, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.failure != nil || s.closing {
		err = s.failure
		if err == nil {
			err = io.ErrClosedPipe
		}
		s.mu.Unlock()
		return err
	}
	s.next++
	id := strconv.FormatUint(s.next, 10)
	ch := make(chan frame, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pending, id); s.mu.Unlock() }()
	if err = s.write(ctx, wire.JSONRPCRequest{ID: json.RawMessage(id), Method: method, Params: (*json.RawMessage)(&p)}); err != nil {
		return err
	}
	select {
	case f := <-ch:
		if f.Error != nil {
			return fmt.Errorf("Codex %s: RPC %d: %s", method, f.Error.Code, f.Error.Message)
		}
		if len(f.Result) == 0 || bytes.Equal(f.Result, []byte("null")) {
			return errors.New("Codex reply omitted result")
		}
		return json.Unmarshal(f.Result, out)
	case <-ctx.Done():
		return ctx.Err()
	case <-s.readerDone:
		s.mu.Lock()
		err = s.failure
		s.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return err
	}
}

func (s *Session) read(r *os.File) {
	defer close(s.readerDone)
	defer close(s.events)
	defer r.Close()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.closing && s.snapshot.Thread.ID != "" {
			s.snapshot.State = worker.Dead
			s.events <- worker.Event{Thread: s.snapshot.Thread, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.ConnectionFailure, Code: "connection_lost"}}
		}
	}()
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 4096), maxFrame)
	for scan.Scan() {
		raw := scan.Bytes()
		var f frame
		if err := json.Unmarshal(raw, &f); err != nil {
			s.fail(errors.New("Codex sent malformed JSON"))
			return
		}
		if f.Method == "" {
			if len(f.ID) == 0 || (len(f.Result) == 0 && f.Error == nil) {
				s.fail(errors.New("Codex sent malformed response"))
				return
			}
			s.mu.Lock()
			ch := s.pending[string(f.ID)]
			s.mu.Unlock()
			if ch != nil {
				select {
				case ch <- f:
				default:
				}
			}
		} else if len(f.ID) > 0 {
			// #133 will bind questions and approvals to the originating conversation.
			// Never approve or silently stall an unsupported request.
			s.fail(fmt.Errorf("unsupported Codex server request: %s", f.Method))
			return
		} else if err := s.notification(raw); err != nil {
			s.fail(err)
			return
		}
	}
	err := scan.Err()
	if err == nil {
		err = io.EOF
	}
	s.fail(fmt.Errorf("Codex stdio: %w", err))
}

// fail is called by either process or reader. The reader alone closes events;
// publication and close cannot race because process failures only close stdin.
func (s *Session) fail(err error) {
	s.mu.Lock()
	if s.failure == nil {
		s.failure = err
	}
	s.mu.Unlock()
	s.cancel()
	s.stdin.Close()
}

func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		s.stdin.Close()
		timer := time.NewTimer(time.Second)
		select {
		case <-s.processDone:
		case <-timer.C:
			s.cancel()
		}
		timer.Stop()
		s.cancel()
		<-s.processDone
		<-s.readerDone
		s.owner.Close(context.Background())
	})
	return nil
}
