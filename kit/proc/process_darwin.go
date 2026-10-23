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
	identity processIdentity
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
	path, args, err := confine(c.Path, c.Args, c.Confined)
	if err != nil {
		return nil, err
	}
	cmd := command(Command{Path: path, Args: args, Dir: c.Dir, Env: c.Env, Stdin: c.Stdin, Stdout: c.Stdout, Stderr: c.Stderr, ExtraFiles: c.ExtraFiles})
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &darwinProcess{cmd: cmd, token: token, stopped: make(chan struct{})}, nil
}

func (p *darwinProcess) start() error {
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
	return nil
}

func (p *darwinProcess) release() {}

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
	// Keep the leader unreaped until cleanup finishes. A dead Darwin process
	// group can report EPERM instead of ESRCH; never signal a recycled group
	// id after cmd.Wait has released its leader.
	var err error
	info, inspectErr := unix.SysctlKinfoProc("kern.proc.pid", p.cmd.Process.Pid)
	if inspectErr == nil && info.Proc.P_stat != 5 { // SZOMB
		err = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.EPERM) {
			current, e := unix.SysctlKinfoProc("kern.proc.pid", p.cmd.Process.Pid)
			if e == nil && current.Proc.P_stat == 5 {
				err = nil
			}
		}
		if errors.Is(err, syscall.ESRCH) {
			err = nil
		}
	} else if inspectErr != nil && !errors.Is(inspectErr, syscall.ESRCH) {
		err = inspectErr
	}
	if p.identity.uniqueID != 0 {
		err = errors.Join(err, killDescendants(p.identity))
	}
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

// killDescendants kills every live process below root and repeats until a scan
// finds none. The tracked set keeps identities discovered in earlier scans, so
// a child forked just before its parent was killed and reaped still resolves to
// the tree. Killed processes cannot fork again, so the sweep ends.
func killDescendants(root processIdentity) error {
	tracked := map[uint64]struct{}{root.uniqueID: {}}
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
