// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type supervisorRequest struct {
	Path      string
	Args, Env []string
}

type supervisorStart struct {
	Message, Op, Path string
	Errno             syscall.Errno
}

type supervisorProcess struct {
	cmd                                        *exec.Cmd
	control, status, childControl, childStatus *os.File
	request                                    supervisorRequest
	stop                                       sync.Once
}

func newProcess(c Command) (process, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	p := &supervisorProcess{request: supervisorRequest{Path: c.Path, Args: c.Args, Env: append([]string{}, c.Env...)}}
	p.childControl, p.control, err = os.Pipe()
	if err != nil {
		return nil, err
	}
	p.status, p.childStatus, err = os.Pipe()
	if err != nil {
		p.release()
		return nil, err
	}
	n := len(c.ExtraFiles)
	p.cmd = command(Command{Path: exe, Args: []string{"proc-supervise", "--control-fd", strconv.Itoa(3 + n), "--status-fd", strconv.Itoa(4 + n), "--files", strconv.Itoa(n)}, Dir: c.Dir, Stdin: c.Stdin, Stdout: c.Stdout, Stderr: c.Stderr})
	p.cmd.ExtraFiles = append(append([]*os.File{}, c.ExtraFiles...), p.childControl, p.childStatus)
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return p, nil
}

func (p *supervisorProcess) start() error {
	if err := p.cmd.Start(); err != nil {
		return err
	}
	p.childControl.Close()
	p.childStatus.Close()
	err := json.NewEncoder(p.control).Encode(p.request)
	var started supervisorStart
	if err == nil {
		err = json.NewDecoder(p.status).Decode(&started)
	}
	if err == nil && started.Message != "" {
		err = errors.New(started.Message)
		if started.Errno != 0 {
			err = started.Errno
		}
		if started.Op != "" {
			err = &os.PathError{Op: started.Op, Path: started.Path, Err: err}
		}
	}
	if err != nil {
		_ = p.kill()
		_ = p.cmd.Wait()
	}
	return err
}

func (p *supervisorProcess) kill() error {
	p.stop.Do(func() {
		if p.control != nil {
			p.control.Close()
		}
	})
	return nil
}
func (p *supervisorProcess) wait() error { return p.cmd.Wait() }
func (p *supervisorProcess) release() {
	_ = p.kill()
	for _, file := range []*os.File{p.status, p.childControl, p.childStatus} {
		if file != nil {
			file.Close()
		}
	}
}

// SuperviseMain implements the explicit proc-supervise subcommand. Hosts using
// NewOwner on Linux register it in their main dispatch and list it in usage.
// The caller sends the command through private descriptors, not argv or the
// supervisor's environment. Closing control requests teardown (also on death
// of the owner). No package init hook or process-wide subreaper is installed.
func SuperviseMain(args []string, diagnostic io.Writer) int {
	flags := flag.NewFlagSet("proc-supervise", flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	controlFD := flags.Int("control-fd", -1, "private command/control descriptor")
	statusFD := flags.Int("status-fd", -1, "private startup-status descriptor")
	files := flags.Int("files", 0, "number of inherited command descriptors")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *files < 0 || *controlFD != 3+*files || *statusFD != 4+*files {
		fmt.Fprintln(diagnostic, "usage: proc-supervise --control-fd <fd> --status-fd <fd> --files <count>")
		return 64
	}
	control := os.NewFile(uintptr(*controlFD), "proc-control")
	status := os.NewFile(uintptr(*statusFD), "proc-status")
	defer control.Close()
	defer status.Close()
	syscall.CloseOnExec(*controlFD)
	syscall.CloseOnExec(*statusFD)
	ack := func(err error) error {
		var reply supervisorStart
		if err != nil {
			reply.Message = err.Error()
			var path *os.PathError
			if errors.As(err, &path) {
				reply.Op, reply.Path = path.Op, path.Path
			}
			_ = errors.As(err, &reply.Errno)
		}
		return json.NewEncoder(status).Encode(reply)
	}
	var request supervisorRequest
	if err := json.NewDecoder(io.LimitReader(control, 8<<20)).Decode(&request); err != nil {
		_ = ack(err)
		return 70
	}
	if !filepath.IsAbs(request.Path) {
		_ = ack(errors.New("absolute command path required"))
		return 70
	}
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, 36, 1, 0, 0, 0, 0); errno != 0 { // PR_SET_CHILD_SUBREAPER
		_ = ack(errno)
		return 70
	}
	if _, err := supervisorChildren(); err != nil {
		_ = ack(err)
		return 70
	}
	events := make(chan os.Signal, 1)
	signal.Notify(events, syscall.SIGCHLD, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(events)
	stopped := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, control); close(stopped) }()
	cmd := command(Command{Path: request.Path, Args: request.Args, Env: request.Env, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	for fd := 3; fd < *controlFD; fd++ {
		syscall.CloseOnExec(fd)
		file := os.NewFile(uintptr(fd), "command-file")
		defer file.Close()
		cmd.ExtraFiles = append(cmd.ExtraFiles, file)
	}
	if err := cmd.Start(); err != nil {
		_ = ack(err)
		return 70
	}
	defer cmd.Process.Release()
	cleanup := ack(nil) != nil
	rootDone := false
	var rootStatus syscall.WaitStatus
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		for {
			var state syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &state, syscall.WNOHANG, nil)
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if errors.Is(err, syscall.ECHILD) && rootDone {
				if rootStatus.Signaled() {
					return 128 + int(rootStatus.Signal())
				}
				return rootStatus.ExitStatus()
			}
			if err != nil {
				fmt.Fprintln(diagnostic, "supervisor wait:", err)
				return 70
			}
			if pid == 0 {
				break
			}
			if pid == cmd.Process.Pid {
				rootDone, rootStatus, cleanup = true, state, true
			}
		}
		if cleanup {
			// Only immediate children are signalled. They cannot be reaped by
			// anyone else; killed parents hand every escaped descendant back
			// to this subreaper. Repeat until wait4 proves ECHILD.
			children, err := supervisorChildren()
			if err != nil {
				fmt.Fprintln(diagnostic, "supervisor children:", err)
			}
			for _, pid := range children {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		select {
		case sig := <-events:
			if sig != syscall.SIGCHLD {
				cleanup = true
			}
		case <-stopped:
			cleanup, stopped = true, nil
		case <-tick.C:
		}
	}
}

func supervisorChildren() ([]int, error) {
	return childrenIn("/proc", os.Getpid())
}

func childrenIn(root string, parent int) ([]int, error) {
	// The per-thread children interface depends on CONFIG_CHECKPOINT_RESTORE.
	// Ordinary stat records do not. Adoption is maintained by the kernel's
	// subreaper; scanning here only discovers its immediate children.
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var children []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name(), "stat"))
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrPermission) {
			continue
		}
		if err != nil {
			return nil, err
		}
		// comm may itself contain spaces or parentheses. The final ')' ends
		// it, followed by state and PPID. Ignore every non-child process.
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			return nil, errors.New("invalid process stat")
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) < 2 {
			return nil, errors.New("missing process parent")
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, err
		}
		if ppid == parent {
			children = append(children, pid)
		}
	}
	return children, nil
}
