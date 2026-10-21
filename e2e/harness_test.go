// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const operationTimeout = 10 * time.Second

// Build the real command once per scenario group. Only verified Go caches and
// tool locations cross into the build; child products get a fresh HOME and no
// credentials, user configuration, display server or provider environment.
func buildLina(t *testing.T) string {
	return buildCommand(t, "lina")
}

func buildCommand(t *testing.T, name string) string {
	t.Helper()
	home := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GOENV=off", "GOTOOLCHAIN=auto"}
	for _, name := range []string{"GOCACHE", "GOMODCACHE", "TMPDIR"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	caches := exec.CommandContext(ctx, "go", "env", "GOCACHE", "GOMODCACHE")
	caches.Dir, caches.Env = "..", env
	output, err := caches.Output()
	if err != nil {
		t.Fatalf("locate Go caches: %v", err)
	}
	paths := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(paths) != 2 {
		t.Fatalf("expected two Go cache paths, got %d", len(paths))
	}
	binary := filepath.Join(home, name)
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, "./cmd/"+name)
	build.Dir = ".."
	build.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + home,
		"GOCACHE=" + paths[0], "GOMODCACHE=" + paths[1], "GOENV=off", "GOTOOLCHAIN=auto",
		"CGO_ENABLED=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
	}
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, output)
	}
	return binary
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
	root, err := os.MkdirTemp("", "lina-e2e-")
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
	}
	p.command.WaitDelay = time.Second
	p.command.Stdout, p.command.Stderr = &p.stdout, &p.stderr
	return p
}

func (p *process) start(t *testing.T) {
	t.Helper()
	if err := p.command.Start(); err != nil {
		t.Fatalf("start %s: %v", p.command.Args[1], err)
	}
	go func() {
		p.err = p.command.Wait()
		close(p.done)
	}()
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
	timer := time.NewTimer(operationTimeout)
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
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	retry := time.NewTicker(5 * time.Millisecond)
	defer retry.Stop()
	for {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", h.socket)
		if err == nil {
			h.t.Cleanup(func() { conn.Close() })
			return conn
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
			h.t.Fatalf("Core did not listen: %v", ctx.Err())
		case <-retry.C:
		}
	}
}

func (h *harness) stopCore(core *process) {
	h.t.Helper()
	if err := core.command.Process.Signal(syscall.SIGTERM); err != nil {
		h.t.Fatal(err)
	}
	core.wait(h.t, 0)
	for _, path := range []string{h.socket, filepath.Join(h.root, "system", "core", "run.sentinel")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			h.t.Fatalf("Core did not remove runtime state: %v", err)
		}
	}
}
