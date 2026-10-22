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
			// Group kill first, then find tokens independently of ancestry/PGID.
			if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				p.stopErr = err
			}
			p.stopErr = errors.Join(p.stopErr, killTokenProcesses(p.token))
		}()
	})
	return nil
}
func (p *tokenProcess) wait() error {
	cleaned := make(chan error, 1)
	// Observe exit independently of exec.Wait, whose stream copies may be
	// held open by an escaped descendant. Descendant discovery uses
	// KERN_PROCARGS2, not unsupported kqueue NOTE_TRACK.
	go func() {
		for {
			info, err := unix.SysctlKinfoProc("kern.proc.pid", p.cmd.Process.Pid)
			if err != nil || info.Proc.P_stat == 5 {
				break
			} // SZOMB
			time.Sleep(10 * time.Millisecond)
		}
		_ = p.kill()
		<-p.stopped
		cleaned <- p.stopErr
	}()
	err := p.cmd.Wait()
	return errors.Join(err, <-cleaned)
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
			if err != nil || current.Proc.P_starttime != info.Proc.P_starttime || current.Eproc.Ucred.Uid != uint32(os.Geteuid()) {
				continue
			}
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
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
