// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package proc

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const ownerTokenName = "LINA_PROC_OWNER"

// A Darwin child can leave the process group and session with setsid or
// setpgid, but it cannot alter two records the owner also keeps: the identity
// the kernel writes at fork (parent unique id, never rewritten) and, for
// ordinary descendants, an inherited environment token. Teardown kills the
// group, then every process whose recorded parent chain reaches the leader, and
// finally every same-user process carrying the exact token.
type darwinProcess struct {
	cmd      *exec.Cmd
	token    string
	target   string // the command the owner itself wrapped
	confined bool
	identity processIdentity
	watcher  *watcher
	stop     sync.Once
	stopErr  error
	stopped  chan struct{}
}

func newProcess(c Command) (process, error) {
	token := ownerTokenName + "=" + rand.Text()
	env := make([]string, 0, len(c.Env)+1)
	for _, entry := range c.Env {
		if !strings.HasPrefix(entry, ownerTokenName+"=") {
			env = append(env, entry)
		}
	}
	c.Env = append(env, token)
	path, args, wrapped := confine(c.Path, c.Args, c.Confined)
	cmd := command(Command{Path: path, Args: args, Dir: c.Dir, Env: c.Env, Stdin: c.Stdin, Stdout: c.Stdout, Stderr: c.Stderr, ExtraFiles: c.ExtraFiles})
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &darwinProcess{cmd: cmd, token: token, target: c.Path, confined: wrapped, stopped: make(chan struct{})}, nil
}

func (p *darwinProcess) start() error {
	if p.confined {
		if err := checkEscapeDenial(p.target); err != nil {
			return err
		}
	}
	if err := p.cmd.Start(); err != nil {
		return err
	}
	identity, err := processIdentityOf(p.cmd.Process.Pid)
	if err != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
		return fmt.Errorf("read command identity: %w", err)
	}
	p.identity = identity
	if w, err := newWatcher(identity); err == nil {
		p.watcher = w
	}
	return nil
}

func (p *darwinProcess) release() {
	if p.watcher != nil {
		p.watcher.close()
	}
}

func (p *darwinProcess) kill() error {
	p.stop.Do(func() {
		go func() {
			defer close(p.stopped)
			p.stopErr = p.terminate()
		}()
	})
	return nil
}

func (p *darwinProcess) terminate() error {
	// The leader stays unreaped until cleanup finishes, so its pid and process
	// group id cannot be reused by another process. The escape denial keeps
	// every descendant in this group, including one that outlived its own
	// parent, so this kill is the primary teardown.
	err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		err = nil // the group is already empty
	}
	if errors.Is(err, syscall.EPERM) {
		// Darwin can refuse a signal once the group has no live member.
		// Accept only a positively observed end of this leader.
		current, inspectErr := unix.SysctlKinfoProc("kern.proc.pid", p.cmd.Process.Pid)
		if errors.Is(inspectErr, syscall.ESRCH) || inspectErr == nil && current.Proc.P_stat == 5 {
			err = nil
		}
	}
	err = errors.Join(err, killDescendants(p.watcher, p.identity))
	return errors.Join(err, killTokenProcesses(p.token))
}

func (p *darwinProcess) wait() error {
	// Observe exit without reaping: escaped descendants can hold stdio open,
	// and cleanup must finish while the leader still reserves its PID/PGID.
	for {
		info, err := unix.SysctlKinfoProc("kern.proc.pid", p.cmd.Process.Pid)
		if err != nil || info.Proc.P_stat == 5 {
			break
		} // SZOMB
		time.Sleep(10 * time.Millisecond)
	}
	_ = p.kill()
	<-p.stopped
	return errors.Join(p.cmd.Wait(), p.stopErr)
}

// killDescendants kills every live process recorded for this command and
// repeats until a scan finds none. Records come from the watcher, so a
// descendant whose parent was reaped, or which changed its own session or
// process group, is still killed.
func killDescendants(w *watcher, root processIdentity) error {
	tracked := make(map[uint64]struct{})
	if w != nil {
		w.scan()
		w.mu.Lock()
		for id := range w.tracked {
			tracked[id] = struct{}{}
		}
		w.mu.Unlock()
	}
	if root.uniqueID != 0 {
		tracked[root.uniqueID] = struct{}{}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		states, err := processStates()
		if err != nil {
			return fmt.Errorf("scan owned processes: %w", err)
		}
		identities := make([]processIdentity, len(states))
		for i, state := range states {
			identities[i] = state.processIdentity
		}
		trackDescendants(tracked, identities)
		live := false
		for _, state := range states {
			if _, ok := tracked[state.uniqueID]; !ok || state.zombie {
				continue
			}
			// Recheck identity after the scan so a recycled pid is never
			// signalled in place of the process that was tracked.
			current, err := processIdentityOf(state.pid)
			if err != nil || current.uniqueID != state.uniqueID {
				continue
			}
			if err := syscall.Kill(state.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("kill descendant %d: %w", state.pid, err)
			}
			live = true
		}
		if !live {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("descendant cleanup did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// killTokenProcesses kills same-user processes that inherited the exact owner
// token. It covers a descendant whose intermediate parent has already exited
// and been reaped, which the kernel's parent record can no longer link to the
// leader.
func killTokenProcesses(token string) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		processes, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Geteuid())
		if err != nil {
			return fmt.Errorf("scan owned processes: %w", err)
		}
		live := false
		for _, info := range processes {
			pid := int(info.Proc.P_pid)
			if pid <= 1 || pid == os.Getpid() || info.Proc.P_stat == 5 {
				continue
			}
			data, err := unix.SysctlRaw("kern.procargs2", pid)
			if err != nil || !hasProcessToken(data, token) {
				continue
			}
			// Recheck identity and credentials after reading arguments to avoid
			// signalling a replacement process found under a recycled PID.
			current, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
			if err != nil || current.Proc.P_starttime != info.Proc.P_starttime || current.Eproc.Ucred.Uid != uint32(os.Geteuid()) || current.Proc.P_stat == 5 {
				continue
			}
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				// Darwin can refuse a signal after the process became a
				// zombie between inspection and kill. Ignore only a positively
				// observed end of this identity, never EPERM on a live child.
				if errors.Is(err, syscall.EPERM) {
					after, readErr := unix.SysctlKinfoProc("kern.proc.pid", pid)
					if errors.Is(readErr, syscall.ESRCH) || readErr == nil && (after.Proc.P_stat == 5 || after.Proc.P_starttime != info.Proc.P_starttime) {
						continue
					}
				}
				return err
			}
			live = true
		}
		if !live {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("token process cleanup did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
