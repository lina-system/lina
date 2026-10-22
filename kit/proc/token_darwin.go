// SPDX-License-Identifier: Apache-2.0

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

// Darwin has no unprivileged subreaper/job primitive. The token is deliberately
// only best effort: a child can remove it before exec or obscure its arguments.
type tokenProcess struct {
	cmd     *exec.Cmd
	token   string
	stop    sync.Once
	stopErr error
	stopped chan struct{}
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
	cmd := command(c)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &tokenProcess{cmd: cmd, token: token, stopped: make(chan struct{})}, nil
}

func (p *tokenProcess) start() error { return p.cmd.Start() }
func (p *tokenProcess) release()     {}
func (p *tokenProcess) kill() error {
	p.stop.Do(func() {
		go func() {
			defer close(p.stopped)
			// Keep the leader unreaped until cleanup finishes. A dead Darwin
			// process group can report EPERM instead of ESRCH; never signal
			// a recycled group id after cmd.Wait has released its leader.
			info, inspectErr := unix.SysctlKinfoProc("kern.proc.pid", p.cmd.Process.Pid)
			if inspectErr == nil && info.Proc.P_stat != 5 {
				err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
				if errors.Is(err, syscall.EPERM) {
					current, e := unix.SysctlKinfoProc("kern.proc.pid", p.cmd.Process.Pid)
					if e == nil && current.Proc.P_stat == 5 {
						err = nil
					}
				}
				if err != nil && !errors.Is(err, syscall.ESRCH) {
					p.stopErr = err
				}
			} else if inspectErr != nil && !errors.Is(inspectErr, syscall.ESRCH) {
				p.stopErr = inspectErr
			}
			p.stopErr = errors.Join(p.stopErr, killTokenProcesses(p.token))
		}()
	})
	return nil
}
func (p *tokenProcess) wait() error {
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
