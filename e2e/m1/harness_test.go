// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/proc"
)

const operationTimeout = 10 * time.Second

var commandBuilds struct {
	sync.Mutex
	directory string
	binaries  map[string]string
}

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "harness-cpu-budget" {
		fmt.Fprintln(os.Stdout, runtime.GOMAXPROCS(0))
		return
	}
	if os.Getenv("LINA_WORKER_FIXTURE") == "3" && len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "app-server") {
		recoveryWorkerFixture()
		return
	}
	if updateManagerHelper() {
		return
	}
	if os.Getenv("LINA_WORKER_FIXTURE") == "2" && len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "app-server") {
		codexWorkToolsFixture()
		return
	}
	if os.Getenv("LINA_WORKER_FIXTURE") == "1" && len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "app-server") {
		codexSessionFixture()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "proc-job" {
		os.Exit(proc.JobMain(os.Args[2:], os.Stderr))
	}
	closeCache, err := prepareSQLiteCache()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	commandBuilds.binaries = make(map[string]string)
	code := m.Run()
	if commandBuilds.directory != "" {
		if err := os.RemoveAll(commandBuilds.directory); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	if err := closeCache(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

// Build each real command once per test run. Only verified Go caches and
// tool locations cross into the build; child products get a fresh HOME and no
// credentials, user configuration, display server or provider environment.
func buildLina(t *testing.T) string {
	return buildCommand(t, "lina")
}

func buildCommand(t *testing.T, name string) string {
	t.Helper()
	commandBuilds.Lock()
	defer commandBuilds.Unlock()
	if binary, ok := commandBuilds.binaries[name]; ok {
		return binary
	}
	// Sandbox helper subprocesses also enter TestMain, but never build a
	// command and may have no writable temporary directory.
	if commandBuilds.directory == "" {
		directory, err := os.MkdirTemp("", "lina-e2e-commands-")
		if err != nil {
			t.Fatal(err)
		}
		commandBuilds.directory = directory
	}
	home, err := os.MkdirTemp(commandBuilds.directory, name+"-")
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GOENV=off", "GOTOOLCHAIN=auto"}
	for _, name := range []string{"GOCACHE", "GOMODCACHE", "TMPDIR"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	caches := exec.CommandContext(ctx, "go", "env", "GOCACHE", "GOMODCACHE")
	caches.Dir, caches.Env = "../..", env
	output, err := caches.Output()
	if err != nil {
		t.Fatalf("locate Go caches: %v", err)
	}
	paths := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(paths) != 2 {
		t.Fatalf("expected two Go cache paths, got %d", len(paths))
	}
	binary := filepath.Join(home, name)
	// Acceptance exercises the real command, but never uses DWARF. Omitting
	// debugger data reduces repeated bundle copying, hashing and archiving.
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-ldflags=-w", "-o", binary, "./cmd/"+name)
	build.Dir = "../.."
	build.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + home,
		"GOCACHE=" + paths[0], "GOMODCACHE=" + paths[1], "GOENV=off", "GOTOOLCHAIN=auto",
		"CGO_ENABLED=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
	}
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, output)
	}
	if err := os.Chmod(binary, 0500); err != nil {
		t.Fatal(err)
	}
	commandBuilds.binaries[name] = binary
	return binary
}

// alphaScenarioParent is the parent for a scenario's private state root. The
// default is the temporary directory. A long acceptance run sets
// LINA_ALPHA_STATE so its state survives unrelated cleanup of the shared
// temporary directory, which would otherwise remove a running Core's socket.
func alphaScenarioParent() (string, error) {
	parent := os.Getenv("LINA_ALPHA_STATE")
	if parent == "" || !filepath.IsAbs(parent) {
		return "", nil
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", fmt.Errorf("prepare LINA_ALPHA_STATE %s: %w", parent, err)
	}
	return parent, nil
}

type harness struct {
	t      *testing.T
	ctx    context.Context
	binary string
	root   string
	socket string
}

func newHarness(t *testing.T, binary string) *harness {
	t.Helper()
	// Keep Unix socket paths below the platform limit even for long test names.
	parent, err := alphaScenarioParent()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(parent, "lina-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove scenario state: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*operationTimeout)
	t.Cleanup(cancel)
	return &harness{t: t, ctx: ctx, binary: binary, root: root, socket: filepath.Join(root, "run", "client.sock")}
}

type process struct {
	command *exec.Cmd
	signal  func(os.Signal) error // Optional child target for a test-only launcher.
	done    chan struct{}
	err     error // Read only after done closes, including all diagnostic buffers.
	stdout  bytes.Buffer
	stderr  bytes.Buffer
}

func (h *harness) command(args ...string) *process {
	p := &process{command: exec.CommandContext(h.ctx, h.binary, args...), done: make(chan struct{})}
	p.command.Dir = h.root
	p.command.Env = []string{
		"HOME=" + h.root, "XDG_STATE_HOME=" + h.root, "XDG_CONFIG_HOME=" + h.root,
		"XDG_RUNTIME_DIR=" + h.root, "PATH=", "TERM=dumb", "LANG=C", "LC_ALL=C",
		// Keep foundation's Go CPU share when constructing the isolated child
		// environment; otherwise every parallel Core uses the whole host.
		fmt.Sprintf("GOMAXPROCS=%d", runtime.GOMAXPROCS(0)),
		sqliteCacheEnv + "=" + os.Getenv(sqliteCacheEnv),
	}
	p.command.WaitDelay = time.Second
	p.command.Stdout, p.command.Stderr = &p.stdout, &p.stderr
	return p
}

func (p *process) start(t *testing.T) {
	t.Helper()
	started := make(chan error, 1)
	go func() {
		// Linux parent-death signals track the spawning OS thread. A
		// Bubblewrap child must outlive the goroutine that requested it,
		// while still dying if this test process exits. Keep its parent
		// thread alive through Wait instead of relying on Go's thread pool.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		err := p.command.Start()
		started <- err
		if err != nil {
			return
		}
		p.err = p.command.Wait()
		close(p.done)
	}()
	if err := <-started; err != nil {
		t.Fatalf("start %s: %v", p.command.Args[1], err)
	}
	t.Cleanup(func() { p.killAndWait() })
}

func (p *process) killAndWait() {
	select {
	case <-p.done:
		return
	default:
		_ = p.command.Process.Kill()
		<-p.done
	}
}

func (p *process) wait(t *testing.T, code int) {
	t.Helper()
	p.waitWithin(t, code, operationTimeout)
}
func (p *process) waitWithin(t *testing.T, code int, budget time.Duration) {
	t.Helper()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		p.killAndWait()
		t.Fatalf("process did not exit; stderr: %s", &p.stderr)
	}
	if got := p.command.ProcessState.ExitCode(); got != code || (code == 0 && p.err != nil) {
		t.Fatalf("exit = %d, want %d: %v; stderr: %s", got, code, p.err, &p.stderr)
	}
}

func (h *harness) startCore() *process {
	h.t.Helper()
	directory := filepath.Join(h.root, "system", "core")
	if err := os.MkdirAll(directory, 0700); err != nil {
		h.t.Fatal(err)
	}
	// Installation owns this identity; boot deliberately does not invent one.
	if err := os.WriteFile(filepath.Join(directory, "main.json"), []byte(`{"main_id":"main-e2e"}`), 0600); err != nil {
		h.t.Fatal(err)
	}
	p := h.command("core", "--state-root", h.root, "--socket", h.socket)
	p.start(h.t)
	return p
}

func (h *harness) connect(core *process) net.Conn {
	h.t.Helper()
	return h.connectWithin(core, operationTimeout)
}
func (h *harness) connectWithin(core *process, budget time.Duration) net.Conn {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, budget)
	defer cancel()
	retry := time.NewTicker(5 * time.Millisecond)
	defer retry.Stop()
	// Both bounds end in this loop, and a dial that started before a deadline
	// can report its own timeout before the context records the cancellation.
	// Decide by the deadlines the loop owns, so a short scenario allowance is
	// never reported as a Core startup or protocol fault.
	notListening := func() {
		if deadline, ok := h.ctx.Deadline(); ok && !time.Now().Before(deadline) {
			h.t.Fatalf("Core did not listen: scenario context expired")
		}
		h.t.Fatalf("Core did not listen within %s", budget)
	}
	timeout := func(err error) bool {
		var netErr net.Error
		return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout()
	}
	for {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", h.socket)
		if err == nil {
			h.t.Cleanup(func() { conn.Close() })
			return conn
		}
		if timeout(err) {
			notListening()
		}
		// Retry only socket startup, never a broken protocol exchange. Progress
		// is a successful connection, not an elapsed startup delay.
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ECONNREFUSED) {
			h.t.Fatalf("connect Core: %v", err)
		}
		select {
		case <-core.done:
			h.t.Fatalf("Core exited before listening: %v; stderr: %s", core.err, &core.stderr)
		case <-ctx.Done():
			notListening()
		case <-retry.C:
		}
	}
}

func (h *harness) stopCore(core *process) {
	h.t.Helper()
	signal := core.signal
	if signal == nil {
		signal = core.command.Process.Signal
	}
	if err := signal(syscall.SIGTERM); err != nil {
		h.t.Fatal(err)
	}
	core.wait(h.t, 0)
	for _, path := range []string{h.socket, filepath.Join(h.root, "system", "core", "run.sentinel")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			h.t.Fatalf("Core did not remove runtime state: %v", err)
		}
	}
}
