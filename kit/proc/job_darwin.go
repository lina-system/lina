// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package proc

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// macOS has no subreaper and refuses an unprivileged coalition change, so the
// ownership boundary is the launchd job itself. launchd places a job's leader
// in a resource coalition that every descendant inherits, including a child
// that starts its own session and clears its environment. Teardown kills that
// coalition rather than a process group or an inherited token.
//
// launchd passes paths, not descriptors, to a job, so the owner gives each job
// a private directory of Unix sockets. The helper connects to the control
// socket and, when the caller supplied a stream, to that stream's socket, and
// hands the connections to the command as its standard descriptors. A socket
// reports end of file only when every copy closes, so a descendant holding
// stdio open keeps the call from returning, exactly as on Linux. Closing the
// control socket is also the owner-death signal: the helper kills the
// coalition and removes the job.

const (
	jobHelper   = "proc-job"
	eventStart  = "started"
	eventExit   = "exit"
	eventFailed = "error"
	// startTimeout bounds the launchd handshake. A job that launchd accepted
	// but never ran must fail the call instead of holding it.
	startTimeout = 30 * time.Second
)

type jobRequest struct {
	Path, Dir, Temp   string
	Ctl, In, Out, Err string
	Args, Env         []string
}

type jobRecord struct {
	Event     string        `json:"event"`
	Pid       int           `json:"pid,omitempty"`
	Coalition uint64        `json:"coalition,omitempty"`
	Code      int           `json:"code,omitempty"`
	Signal    int           `json:"signal,omitempty"`
	Error     string        `json:"error,omitempty"`
	Op        string        `json:"op,omitempty"`
	Path      string        `json:"path,omitempty"`
	Errno     syscall.Errno `json:"errno,omitempty"`
}

// failure returns the start failure a helper record describes, matching the
// error shape the Linux supervisor reports for the same failure.
func (r jobRecord) failure() error {
	var err error = errors.New(r.Error)
	if r.Errno != 0 {
		err = r.Errno
	}
	if r.Op != "" {
		err = &os.PathError{Op: r.Op, Path: r.Path, Err: err}
	}
	return err
}

type jobStream struct {
	listener *net.UnixListener
	accepted chan *net.UnixConn
	conn     *net.UnixConn
	pump     chan error
}

type jobProcess struct {
	dir, label, domain, plist string
	ctlListener               *net.UnixListener
	ctl                       *net.UnixConn
	decoder                   *json.Decoder
	streams                   map[string]*jobStream // "in", "out" and "err" when supplied
	stdin                     io.Reader
	stdout, stderr            io.Writer
	coalition                 uint64
	stop                      sync.Once
	stopped                   chan struct{}
	stopErr                   error
}

func newProcess(c Command) (process, error) {
	if len(c.ExtraFiles) != 0 {
		return nil, errors.New("a launchd job cannot inherit extra descriptors")
	}
	dir, err := tempDir()
	if err != nil {
		return nil, err
	}
	p := &jobProcess{dir: dir, label: "com.lina.proc." + strings.ToLower(rand.Text()), streams: map[string]*jobStream{}, stdin: c.Stdin, stdout: c.Stdout, stderr: c.Stderr, stopped: make(chan struct{})}
	fail := func(err error) (process, error) {
		p.release()
		return nil, err
	}
	request := jobRequest{Path: c.Path, Dir: c.Dir, Temp: dir, Args: c.Args, Env: c.Env}
	for _, stream := range []struct {
		name   string
		path   *string
		active bool
	}{{"in", &request.In, c.Stdin != nil}, {"out", &request.Out, c.Stdout != nil}, {"err", &request.Err, c.Stderr != nil}} {
		if !stream.active {
			continue
		}
		path := filepath.Join(dir, stream.name+".sock")
		*stream.path = path
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			return fail(err)
		}
		p.streams[stream.name] = &jobStream{listener: listener, accepted: make(chan *net.UnixConn, 1), pump: make(chan error, 1)}
	}
	request.Ctl = filepath.Join(dir, "ctl.sock")
	ctl, err := net.ListenUnix("unix", &net.UnixAddr{Name: request.Ctl, Net: "unix"})
	if err != nil {
		return fail(err)
	}
	p.ctlListener = ctl
	requestPath := filepath.Join(dir, "request.json")
	data, err := json.Marshal(request)
	if err != nil {
		return fail(err)
	}
	if err := os.WriteFile(requestPath, data, 0600); err != nil {
		return fail(err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	p.plist = filepath.Join(dir, "job.plist")
	if err := os.WriteFile(p.plist, jobPlist(p.label, exe, requestPath, dir), 0600); err != nil {
		return fail(err)
	}
	return p, nil
}

// tempDir keeps the socket paths inside the 104-byte Unix-socket limit.
func tempDir() (string, error) {
	dir, err := os.MkdirTemp("", "lina-proc-")
	if err != nil {
		return "", err
	}
	if len(filepath.Join(dir, "request.json")) < 100 {
		return dir, nil
	}
	_ = os.RemoveAll(dir)
	if dir, err = os.MkdirTemp("/tmp", "lina-proc-"); err != nil {
		return "", err
	}
	return dir, nil
}

func jobPlist(label, exe, request, dir string) []byte {
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
`)
	for _, entry := range []struct {
		key   string
		value string
	}{{"Label", label}, {"WorkingDirectory", dir}} {
		body.WriteString("<key>" + entry.key + "</key><string>" + escapeXML(entry.value) + "</string>\n")
	}
	body.WriteString("<key>ProgramArguments</key><array>")
	for _, argument := range []string{exe, jobHelper, "--request", request} {
		body.WriteString("<string>" + escapeXML(argument) + "</string>")
	}
	// RunAtLoad starts the job once. KeepAlive stays false so a job whose
	// command exited is never restarted before its owner tears it down.
	body.WriteString(`</array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><false/>
<key>ExitTimeOut</key><integer>2</integer>
</dict></plist>
`)
	return []byte(body.String())
}

func escapeXML(text string) string {
	var out strings.Builder
	_ = xml.EscapeText(&out, []byte(text))
	return out.String()
}

func (p *jobProcess) start() error {
	// Accept before bootstrap so a connecting helper never waits on a full
	// backlog, and set the handshake deadline on every listener.
	deadline := time.Now().Add(startTimeout)
	accepted := make(chan *net.UnixConn, 1)
	go func() {
		conn, err := p.ctlListener.AcceptUnix()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- conn
	}()
	_ = p.ctlListener.SetDeadline(deadline)
	for _, stream := range p.streams {
		go func() {
			conn, err := stream.listener.AcceptUnix()
			if err != nil {
				close(stream.accepted)
				return
			}
			stream.accepted <- conn
		}()
		_ = stream.listener.SetDeadline(deadline)
	}
	if err := p.bootstrap(); err != nil {
		p.teardown()
		return err
	}
	ctl, ok := <-accepted
	if !ok || ctl == nil {
		p.teardown()
		return errors.New("start launchd job: the job did not connect")
	}
	p.ctl, p.decoder = ctl, json.NewDecoder(ctl)
	for name, stream := range p.streams {
		conn, ok := <-stream.accepted
		if !ok || conn == nil {
			p.teardown()
			return fmt.Errorf("start launchd job: %q stream did not connect", name)
		}
		stream.conn, stream.pump = conn, make(chan error, 1)
		switch name {
		case "in":
			go func() { _, _ = io.Copy(conn, p.stdin); stream.pump <- conn.CloseWrite() }()
		case "out":
			go func() { _, err := io.Copy(p.stdout, conn); stream.pump <- err }()
		case "err":
			go func() { _, err := io.Copy(p.stderr, conn); stream.pump <- err }()
		}
	}
	record, err := p.next()
	if err != nil {
		p.teardown()
		return fmt.Errorf("start launchd job: %w", err)
	}
	if record.Event == eventFailed {
		p.teardown()
		return record.failure()
	}
	if record.Event != eventStart || record.Pid <= 1 || record.Coalition == 0 {
		p.teardown()
		return fmt.Errorf("start launchd job: unexpected record %q", record.Event)
	}
	p.coalition = record.Coalition
	return nil
}

// bootstrap tries the GUI domain first because Node and the app are logged-in
// agents; an account without a GUI session has only the user domain.
func (p *jobProcess) bootstrap() error {
	var failure error
	for _, domain := range []string{fmt.Sprintf("gui/%d", os.Geteuid()), fmt.Sprintf("user/%d", os.Geteuid())} {
		if err := launchctl("bootstrap", domain, p.plist); err == nil {
			p.domain = domain
			return nil
		} else {
			failure = errors.Join(failure, err)
		}
	}
	return fmt.Errorf("load launchd job: %w", failure)
}

func launchctl(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "/bin/launchctl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", args[0], err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (p *jobProcess) kill() error {
	// Teardown scans the process table, so it never runs inside the owner's
	// lock.
	go p.teardown()
	return nil
}

func (p *jobProcess) wait() error {
	record, decodeErr := p.next()
	p.teardown()
	<-p.stopped
	var statusErr error
	switch {
	case decodeErr == nil && record.Event == eventExit:
		if record.Signal != 0 {
			statusErr = &ExitError{Code: -1, Signal: syscall.Signal(record.Signal)}
		} else if record.Code != 0 {
			statusErr = &ExitError{Code: record.Code}
		}
	case decodeErr == nil && record.Event == eventFailed:
		statusErr = record.failure()
	case decodeErr == nil:
		statusErr = fmt.Errorf("unexpected launchd job record %q", record.Event)
	case errors.Is(decodeErr, io.EOF) || errors.Is(decodeErr, io.ErrUnexpectedEOF):
		statusErr = errors.New("launchd job ended without an exit status")
	default:
		statusErr = decodeErr
	}
	return errors.Join(statusErr, p.stopErr, p.drain())
}

func (p *jobProcess) next() (jobRecord, error) {
	var record jobRecord
	return record, p.decoder.Decode(&record)
}

func (p *jobProcess) teardown() {
	p.stop.Do(func() {
		defer close(p.stopped)
		p.stopErr = p.killCoalition()
		if err := p.bootout(); err != nil {
			p.stopErr = errors.Join(p.stopErr, err)
		}
	})
}

// killCoalition kills every member until two consecutive scans find none. The
// job stays registered until the scan is clean, so its coalition cannot be
// released and reused while it is being killed.
func (p *jobProcess) killCoalition() error {
	if p.coalition == 0 {
		return nil
	}
	deadline := time.Now().Add(2 * time.Second)
	clean := 0
	for {
		remaining, err := killCoalition(p.coalition)
		if err != nil {
			return err
		}
		if remaining == 0 {
			if clean++; clean > 1 {
				return nil
			}
		} else {
			clean = 0
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("launchd coalition %d still has %d members", p.coalition, remaining)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// killCoalition signals live same-user members of one coalition and reports
// how many it found. The caller, the host and zombies are left alone: a zombie
// has ended and belongs to its reaper.
func killCoalition(coalition uint64) (int, error) {
	pids, err := allPIDs()
	if err != nil {
		return 0, err
	}
	remaining := 0
	for _, pid := range pids {
		if pid <= 1 || pid == os.Getpid() {
			continue
		}
		info, err := processInfo(pid)
		if err != nil || info.Proc.P_stat == 5 || info.Eproc.Ucred.Uid != uint32(os.Geteuid()) {
			continue
		}
		coalitions, err := coalitionIDs(pid)
		if err != nil || coalitions[0] != coalition {
			continue
		}
		// Membership was read after the identity; signal only while the same
		// process still holds it, never a recycled pid.
		current, err := processInfo(pid)
		if err != nil || current.Proc.P_starttime != info.Proc.P_starttime {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return remaining, err
		}
		remaining++
	}
	return remaining, nil
}

func (p *jobProcess) bootout() error {
	if p.domain == "" {
		return nil
	}
	// A job that already left its domain is torn down.
	if err := launchctl("bootout", p.domain+"/"+p.label); err != nil && !strings.Contains(err.Error(), "Could not find service") {
		return err
	}
	return nil
}

func (p *jobProcess) drain() error {
	var err error
	for _, stream := range p.streams {
		if stream.pump == nil {
			continue
		}
		select {
		case pumpErr := <-stream.pump:
			err = errors.Join(err, pumpErr)
		case <-time.After(5 * time.Second):
			// A stream that outlives teardown would keep the call from
			// returning; closing its end ends the copy and reports why.
			if stream.conn != nil {
				_ = stream.conn.Close()
			}
			select {
			case pumpErr := <-stream.pump:
				err = errors.Join(err, pumpErr)
			case <-time.After(time.Second):
				err = errors.Join(err, errors.New("process stream did not finish"))
			}
		}
	}
	return err
}

func (p *jobProcess) release() {
	p.teardown()
	for _, stream := range p.streams {
		_ = stream.listener.Close()
		if stream.conn != nil {
			_ = stream.conn.Close()
		}
	}
	if p.ctlListener != nil {
		_ = p.ctlListener.Close()
	}
	if p.ctl != nil {
		_ = p.ctl.Close()
	}
	_ = os.RemoveAll(p.dir)
}

// JobMain implements the explicit proc-job subcommand a launchd job runs. It is
// registered by both `lina` and `lina-node`, as proc-supervise is on Linux. The
// job request arrives through a private file named in argv.
func JobMain(args []string, diagnostic io.Writer) int {
	flags := flag.NewFlagSet(jobHelper, flag.ContinueOnError)
	flags.SetOutput(diagnostic)
	requestPath := flags.String("request", "", "private command request file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *requestPath == "" {
		fmt.Fprintln(diagnostic, "usage: proc-job --request <path>")
		return 64
	}
	request, err := readJobRequest(*requestPath)
	if err != nil {
		fmt.Fprintln(diagnostic, "proc-job:", err)
		return 70
	}
	ctl, err := net.Dial("unix", request.Ctl)
	if err != nil {
		fmt.Fprintln(diagnostic, "proc-job:", err)
		return 70
	}
	defer ctl.Close()
	if err := inheritStreams(request); err != nil {
		fmt.Fprintln(diagnostic, "proc-job:", err)
		return 70
	}
	coalitions, err := coalitionIDs(os.Getpid())
	if err != nil || coalitions[0] == 0 {
		fmt.Fprintln(diagnostic, "proc-job: read coalition:", err)
		return 70
	}
	records := json.NewEncoder(ctl)
	if err := records.Encode(jobRecord{Event: eventStart, Pid: os.Getpid(), Coalition: coalitions[0]}); err != nil {
		fmt.Fprintln(diagnostic, "proc-job:", err)
		return 70
	}
	// The owner closes the control socket when it tears the job down or dies.
	// Either way the coalition is killed; a job left registered is removed by
	// a detached launchctl.
	go func() {
		_, _ = io.Copy(io.Discard, ctl)
		_, _ = killCoalition(coalitions[0])
		endJob(request, os.Getenv("XPC_SERVICE_NAME"))
		os.Exit(0)
	}()
	command := exec.Command(request.Path, request.Args...)
	command.Dir, command.Env = request.Dir, request.Env
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		record := jobRecord{Event: eventFailed, Error: err.Error()}
		var path *os.PathError
		if errors.As(err, &path) {
			record.Op, record.Path = path.Op, path.Path
		}
		var errno syscall.Errno
		if errors.As(err, &errno) {
			record.Errno = errno
		}
		fmt.Fprintln(diagnostic, "proc-job:", err)
		_ = records.Encode(record)
		return 70
	}
	record := jobRecord{Event: eventExit}
	err = command.Wait()
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		if wait, ok := exited.Sys().(syscall.WaitStatus); ok {
			if wait.Signaled() {
				record.Signal = int(wait.Signal())
			} else {
				record.Code = wait.ExitStatus()
			}
		}
	} else if err != nil {
		record.Code, record.Error = 70, err.Error()
		fmt.Fprintln(diagnostic, "proc-job:", err)
	}
	if err := records.Encode(record); err != nil {
		fmt.Fprintln(diagnostic, "proc-job:", err)
	}
	// Hold the coalition until the owner tears the job down, so a descendant
	// that keeps running stays attributable.
	select {}
}

// inheritStreams hands the connected sockets to the command as its standard
// descriptors. A stream the caller did not supply stays as launchd left it.
func inheritStreams(request jobRequest) error {
	for _, stream := range []struct {
		path string
		fd   int
	}{{request.In, 0}, {request.Out, 1}, {request.Err, 2}} {
		if stream.path == "" {
			continue
		}
		conn, err := net.Dial("unix", stream.path)
		if err != nil {
			return err
		}
		file, err := conn.(*net.UnixConn).File()
		_ = conn.Close()
		if err != nil {
			return err
		}
		err = unix.Dup2(int(file.Fd()), stream.fd)
		_ = file.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// endJob removes the registered job after the owner is gone. Removal runs
// detached because bootout terminates this process.
func endJob(request jobRequest, label string) {
	if label == "" {
		return
	}
	uid := os.Geteuid()
	command := exec.Command("/bin/sh", "-c", fmt.Sprintf("sleep 0.1; launchctl bootout gui/%d/%s; launchctl bootout user/%d/%s; rm -rf %s", uid, shellQuote(label), uid, shellQuote(label), shellQuote(request.Temp)))
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err == nil {
		_ = command.Process.Release()
	}
}

func shellQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

func readJobRequest(path string) (jobRequest, error) {
	var request jobRequest
	data, err := os.ReadFile(path)
	if err != nil {
		return request, err
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return request, err
	}
	if !filepath.IsAbs(request.Path) || !filepath.IsAbs(request.Ctl) {
		return request, errors.New("proc-job request needs absolute paths")
	}
	return request, nil
}
