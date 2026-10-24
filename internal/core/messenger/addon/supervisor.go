// SPDX-License-Identifier: Apache-2.0

// Process supervision for one add-on account: one long-lived child, a bounded
// restart with backoff, and size-bounded lines. The add-on runs inside the OS
// sandbox with network on and writes only in its own data directory; there is no
// unsandboxed fallback.
package addon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/sandbox"
)

// ErrSandboxUnavailable reports that the OS sandbox cannot confine the add-on,
// so the add-on does not run at all.
var ErrSandboxUnavailable = errors.New("sandbox unavailable")

// ErrUnsupportedPlatform reports an account whose platform has no driver in the
// pinned add-on yet; its own source fails and others keep working.
var ErrUnsupportedPlatform = errors.New("messenger add-on has no driver for this platform")

// Config is the add-on's trusted host configuration. Core supplies every path.
type Config struct {
	// Node is the absolute path of the person's Node.js, and Program the
	// absolute path of the add-on's main.ts. Either empty means the add-on is
	// not installed, and only its sources fail.
	Node, Program string
	// StateRoot is the main's state root. The add-on's session lives in
	// plugins/messenger-addon/<account>/, outside backups.
	StateRoot string
	// Socket is Core's client socket, hidden from the add-on.
	Socket string
	// Launcher pins an optional trusted OS sandbox launcher.
	Launcher string
	// MaxRestarts bounds consecutive child starts without a successful call.
	MaxRestarts int
	// RestartDelay is the first backoff step; it doubles up to a minute.
	RestartDelay time.Duration
	// RequestTimeout bounds one request.
	RequestTimeout time.Duration
	// Runner starts the child. It is a test seam; production leaves it nil and
	// uses the OS sandbox.
	Runner Runner
}

// Runner starts a confined child and blocks until it exits. env is the
// allowlisted child environment; the production runner ignores it because the
// sandbox already carries it.
type Runner interface {
	Run(ctx context.Context, l lease.Lease, command sandbox.Command, env []string) error
}

type sandboxRunner struct{ box *sandbox.Sandbox }

func (s sandboxRunner) Run(ctx context.Context, l lease.Lease, command sandbox.Command, _ []string) error {
	return s.box.Run(ctx, l, command)
}

type header struct {
	ID     string          `json:"id"`
	OK     *bool           `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

type child struct {
	write  func([]byte) error
	stop   func()
	done   chan struct{}
	closed sync.Once
	err    error

	mu      sync.Mutex
	pending map[string]chan wireResponse
	live    func(messenger.Event) error
}

func (c *child) finish() {
	c.closed.Do(func() {
		c.stop()
		close(c.done)
	})
}

func (c *child) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		return ErrStopped
	}
	return c.err
}

func (c *child) fail(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
	c.finish()
}

func (c *child) deliverLine(line []byte) {
	var head header
	if err := json.Unmarshal(line, &head); err != nil {
		c.fail(ErrUpstreamChanged)
		return
	}
	if head.ID != "" {
		if err := decodeStrict(line, &head); err != nil || head.OK == nil || (*head.OK && (len(head.Result) == 0 || string(head.Result) == "null" || head.Error != "")) || (!*head.OK && (head.Error == "" || len(head.Result) != 0)) {
			c.fail(ErrUpstreamChanged)
			return
		}
	}
	if head.ID != "" {
		c.mu.Lock()
		waiter := c.pending[head.ID]
		delete(c.pending, head.ID)
		c.mu.Unlock()
		if waiter != nil {
			ok := head.OK != nil && *head.OK
			waiter <- wireResponse{ID: head.ID, OK: ok, Result: head.Result, Error: head.Error}
		}
		return
	}
	var event wireEvent
	if err := decodeStrict(line, &event); err != nil {
		c.fail(err)
		return
	}
	converted, err := event.event()
	if err != nil {
		c.fail(err)
		return
	}
	c.mu.Lock()
	sink := c.live
	c.mu.Unlock()
	if sink != nil {
		_ = sink(converted)
	}
}

type process struct {
	platform, account string
	cfg               Config
	sequence          atomic.Uint64

	mu       sync.Mutex
	child    *child
	failures int
	closed   bool
	failure  error
}

func newProcess(platform, account string, cfg Config) *process {
	if cfg.MaxRestarts <= 0 {
		cfg.MaxRestarts = 5
	}
	if cfg.RestartDelay <= 0 {
		cfg.RestartDelay = time.Second
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 2 * time.Minute
	}
	return &process{platform: platform, account: account, cfg: cfg}
}

// DataDir is the add-on's own data directory for one account:
// plugins/messenger-addon/<account>/ under the state root, 0700 and outside
// backups ([docs/design/filesystem.md#state-root-and-layout]). A platform id is
// reduced to a safe name with a short digest that keeps two accounts apart.
func DataDir(stateRoot, platform, account string) string {
	sum := sha256.Sum256([]byte(platform + "\x00" + account))
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return -1
	}, account)
	if name == "" {
		name = "account"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return filepath.Join(stateRoot, "plugins", "messenger-addon", name+"-"+hex.EncodeToString(sum[:6]))
}

func (p *process) dataDir() string { return DataDir(p.cfg.StateRoot, p.platform, p.account) }

func (p *process) call(ctx context.Context, op string, arguments map[string]any, result any) error {
	current, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("r%d", p.sequence.Add(1))
	request := map[string]any{"id": id, "op": op}
	for key, value := range arguments {
		request[key] = value
	}
	line, err := json.Marshal(request)
	if err != nil {
		return err
	}
	waiter := make(chan wireResponse, 1)
	current.mu.Lock()
	current.pending[id] = waiter
	current.mu.Unlock()
	defer func() {
		current.mu.Lock()
		delete(current.pending, id)
		current.mu.Unlock()
	}()
	if err := current.write(append(line, '\n')); err != nil {
		current.fail(err)
		return fmt.Errorf("%w: %v", ErrStopped, err)
	}
	timer := time.NewTimer(p.cfg.RequestTimeout)
	defer timer.Stop()
	select {
	case response := <-waiter:
		p.mu.Lock()
		p.failures = 0
		p.mu.Unlock()
		if !response.OK {
			switch response.Error {
			case "unsupported_platform":
				return ErrUnsupportedPlatform
			case "upstream_changed":
				return ErrUpstreamChanged
			}
			return fmt.Errorf("%w: %s", ErrStopped, response.Error)
		}
		if result == nil {
			return nil
		}
		if err := requiredResult(op, response.Result); err != nil {
			return err
		}
		return decodeStrict(response.Result, result)
	case <-current.done:
		return errors.Join(ErrStopped, current.failure())
	case <-timer.C:
		current.fail(errors.New("request timed out"))
		return fmt.Errorf("%w: request timed out", ErrStopped)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *process) live(ctx context.Context, deliver func(messenger.Event) error) error {
	current, err := p.acquire(ctx)
	if err != nil {
		return err
	}
	current.mu.Lock()
	current.live = deliver
	current.mu.Unlock()
	defer func() {
		current.mu.Lock()
		current.live = nil
		current.mu.Unlock()
	}()
	if err := p.call(ctx, "live", nil, nil); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return nil
	case <-current.done:
		return errors.Join(ErrStopped, current.failure())
	}
}

// acquire returns the running child, starting it if needed. A child that died
// is replaced with backoff, a bounded number of times.
func (p *process) acquire(ctx context.Context) (*child, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, ErrStopped
		}
		if p.cfg.Node == "" || p.cfg.Program == "" {
			p.mu.Unlock()
			return nil, ErrNotConfigured
		}
		if p.failure != nil {
			failure := p.failure
			p.mu.Unlock()
			return nil, failure
		}
		current := p.child
		if current == nil {
			if p.failures >= p.cfg.MaxRestarts {
				p.failure = fmt.Errorf("%w: started %d times without a successful call", ErrStopped, p.failures)
				failure := p.failure
				p.mu.Unlock()
				return nil, failure
			}
		}
		p.mu.Unlock()
		if current != nil {
			select {
			case <-current.done:
				p.retire(current)
			default:
				return current, nil
			}
		}
		if err := p.wait(ctx); err != nil {
			return nil, err
		}
		p.mu.Lock()
		if p.child == nil && p.failure == nil && !p.closed {
			next, err := p.start(ctx)
			if err != nil {
				p.failures++
				p.mu.Unlock()
				return nil, err
			}
			p.child = next
			p.mu.Unlock()
			return next, nil
		}
		p.mu.Unlock()
	}
}

// wait sleeps the backoff before the next start. The first start does not wait.
func (p *process) wait(ctx context.Context) error {
	p.mu.Lock()
	failures := p.failures
	p.mu.Unlock()
	var delay time.Duration
	if failures > 0 {
		step := failures
		if step > 6 {
			step = 6
		}
		delay = p.cfg.RestartDelay << (step - 1)
		if delay > time.Minute || delay <= 0 {
			delay = time.Minute
		}
	}
	if delay == 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retire records that a child without a successful call has exited.
func (p *process) retire(current *child) { current.finish() }

func (p *process) start(ctx context.Context) (*child, error) {
	dir := p.dataDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("%w: data directory", ErrStopped)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, fmt.Errorf("%w: data directory", ErrStopped)
	}
	childIn, parentIn := io.Pipe()
	parentOut, childOut := io.Pipe()
	owner := proc.NewOwner()
	environment := p.environment(dir)
	var runner Runner
	if p.cfg.Runner != nil {
		runner = p.cfg.Runner
	} else {
		runner = sandboxRunner{box: sandbox.New(owner, sandbox.Config{
			Launcher:   p.cfg.Launcher,
			WorkDir:    dir,
			WriteRoots: []string{dir},
			ReadDenied: p.denied(),
			Env:        environment,
		})}
	}
	childCtx, cancel := context.WithCancel(ctx)
	exited := make(chan error, 1)
	go func() {
		l := lease.New(func() {})
		defer l.Release()
		err := runner.Run(childCtx, l, sandbox.Command{
			Path:    p.cfg.Node,
			Args:    []string{p.cfg.Program},
			Network: true,
			Stdin:   childIn,
			Stdout:  childOut,
			Stderr:  io.Discard,
		}, environment)
		if err == nil {
			err = ErrStopped
		}
		childOut.CloseWithError(err)
		childIn.CloseWithError(err)
		exited <- err
	}()
	next := &child{
		done:    make(chan struct{}),
		pending: map[string]chan wireResponse{},
	}
	next.stop = func() {
		cancel()
		parentIn.Close()
		parentOut.Close()
		_ = owner.Close(context.WithoutCancel(ctx))
		<-exited
	}
	next.write = func(line []byte) error {
		_, err := parentIn.Write(line)
		return err
	}
	go p.read(next, parentOut)
	return next, nil
}

func (p *process) read(c *child, output io.Reader) {
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 64*1024), maxLine)
	for scanner.Scan() {
		c.deliverLine(append([]byte{}, scanner.Bytes()...))
	}
	if err := scanner.Err(); err != nil {
		c.mu.Lock()
		if errors.Is(err, bufio.ErrTooLong) {
			c.err = fmt.Errorf("%w: line larger than the limit", ErrUpstreamChanged)
		} else if c.err == nil {
			c.err = err
		}
		c.mu.Unlock()
	}
	c.mu.Lock()
	if c.err == nil {
		c.err = ErrStopped
	}
	cause := c.err
	c.mu.Unlock()
	p.mu.Lock()
	if p.child == c {
		p.child = nil
		p.failures++
		if errors.Is(cause, ErrUpstreamChanged) {
			p.failure = ErrUpstreamChanged
		}
		if errors.Is(cause, sandbox.ErrUnavailable) || errors.Is(cause, ErrSandboxUnavailable) {
			p.failure = fmt.Errorf("%w: %v", ErrSandboxUnavailable, cause)
		}
	}
	p.mu.Unlock()
	c.finish()
}

// denied is everything the add-on must not read: Core's socket and every area
// of the state root except its own data directory. Each directory is created
// 0700 first, because a mask needs a mount point and a missing one would make
// the policy invalid rather than merely unavailable.
func (p *process) denied() []string {
	denied := []string{p.cfg.Socket}
	if p.cfg.StateRoot == "" {
		return denied
	}
	for _, name := range []string{"canon", "secrets", "system", "versions", "backups", "materials", "derived", "staging", "work", "workspace"} {
		path := filepath.Join(p.cfg.StateRoot, name)
		_ = os.MkdirAll(path, 0700)
		denied = append(denied, path)
	}
	return denied
}

func (p *process) environment(dir string) []string {
	values := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"XDG_CONFIG_HOME=" + dir,
		"AGENT_MESSENGER_CONFIG_DIR=" + dir,
		"LINA_MESSENGER_ADDON_PLATFORM=" + p.platform,
		"LINA_MESSENGER_ADDON_ACCOUNT=" + p.account,
	}
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		if value := os.Getenv(name); value != "" {
			values = append(values, name+"="+value)
		}
	}
	return proc.Environment(values)
}

// Close stops the child and joins it. Later calls report ErrStopped.
func (p *process) Close(ctx context.Context) error {
	p.mu.Lock()
	p.closed = true
	current := p.child
	p.child = nil
	p.mu.Unlock()
	if current == nil {
		return nil
	}
	current.finish()
	select {
	case <-current.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
