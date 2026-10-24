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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// macOS has no subreaper, and a child cannot be given a coalition of its own
// from inside this package: XNU answers an unprivileged coalition request with
// EPERM, and coalitions are created by launchd. The ownership boundary is
// therefore the launchd job. launchd places a job in a resource coalition that
// every descendant inherits, including a child that starts its own session,
// outlives its parent and clears its environment, and no process can change its
// coalition after it is created. Teardown kills the coalition, not a process
// group or an inherited token.
//
// launchd passes paths to a job, never descriptors, so the owner gives each job
// a private directory holding one Unix socket per stream and one control
// socket. The helper connects, hands each stream socket to the command as its
// standard descriptor, reports the command's status and the job's coalition,
// and holds the job open. Closing the control socket is the owner-death signal:
// the helper then kills the coalition and removes the job itself.

const (
	jobHelper = "proc-job"
	// JobDepthVariable marks a process that launchd started as a job. A job
	// that would itself start a job is refused, so a host that loses the helper
	// dispatch cannot turn every command into a nested run of jobs.
	JobDepthVariable = "LINA_PROC_JOB_DEPTH"
	eventStart       = "started"
	eventExit        = "exit"
	eventFailed      = "error"
	startTimeout     = 30 * time.Second
	// launchctl calls are serialized: each one changes launchd's job table,
	// and a burst of concurrent bootstraps is what makes a loaded host
	// misbehave.
	launchctlTimeout = 20 * time.Second
)

var launchctlLock sync.Mutex

type jobRequest struct {
	Path, Dir, Temp   string
	Ctl, In, Out, Err string
	Label, Domain     string
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

// failure returns the start failure a helper record describes, in the same
// shape the Linux supervisor reports for the same failure.
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

// sharedWriter serializes writes through the lock its stream holds. os/exec
// gives the same guarantee when stdout and stderr are the same writer, and no
// guarantee at all when they are not, so distinct streams never share a lock.
type sharedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (s *sharedWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// sameWriter reports whether two writers are the same value, including types
// that cannot be compared.
func sameWriter(a, b io.Writer) bool {
	if a == nil || b == nil {
		return false
	}
	same := false
	func() {
		defer func() { _ = recover() }()
		same = a == b
	}()
	return same
}

type jobStream struct {
	listener *net.UnixListener
	accepted chan *net.UnixConn
	conn     *net.UnixConn
	pump     chan error
	writeMu  *sync.Mutex // nil for the input stream
}

type jobProcess struct {
	dir, label, domain, plist string
	exe, requestPath          string
	ctlListener               *net.UnixListener
	ctl                       *net.UnixConn
	decoder                   *json.Decoder
	streams                   map[string]*jobStream // "in", "out" and "err"
	stdin                     io.Reader
	stdout, stderr            io.Writer
	stdinMu                   sync.Mutex
	stdinErr                  error
	coalition                 uint64
	loaded                    bool
	stop                      sync.Once
	stopped                   chan struct{}
	stopErr                   error
}

func newProcess(c Command) (process, error) {
	if len(c.ExtraFiles) != 0 {
		return nil, errors.New("a launchd job cannot inherit extra descriptors")
	}
	var err error
	if c.Dir, err = ownerDirectory(c.Dir); err != nil {
		return nil, err
	}
	c.Env = ownerEnvironment(c.Env)
	dir, err := tempDir()
	if err != nil {
		return nil, err
	}
	outMu := &sync.Mutex{}
	p := &jobProcess{dir: dir, label: "com.lina.proc." + strings.ToLower(rand.Text()), streams: map[string]*jobStream{}, stdin: c.Stdin, stdout: c.Stdout, stderr: c.Stderr, stopped: make(chan struct{})}
	fail := func(err error) (process, error) {
		p.release()
		return nil, err
	}
	request := jobRequest{Path: c.Path, Dir: c.Dir, Temp: dir, Label: p.label, Args: c.Args, Env: c.Env}
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
		var lock *sync.Mutex
		if stream.name != "in" {
			lock = &sync.Mutex{}
			if sameWriter(c.Stdout, c.Stderr) {
				// One writer for both streams must see one write at a time.
				lock = outMu
			}
		}
		p.streams[stream.name] = &jobStream{listener: listener, accepted: make(chan *net.UnixConn, 1), pump: make(chan error, 1), writeMu: lock}
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
	if err := os.WriteFile(requestPath, data, 0o600); err != nil {
		return fail(err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	p.plist = filepath.Join(dir, "job.plist")
	p.exe, p.requestPath = exe, requestPath
	if err := os.WriteFile(p.plist, jobPlist(p.label, exe, requestPath, dir, false), 0o600); err != nil {
		return fail(err)
	}
	return p, nil
}

// ownerDirectory resolves the directory a job receives. The helper runs in the
// job's private directory, so an unset or relative directory is resolved here
// against the owner's own before it is sent.
func ownerDirectory(dir string) (string, error) {
	if filepath.IsAbs(dir) {
		return dir, nil
	}
	base := dir
	if base == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		base = wd
	}
	return filepath.Abs(base)
}

// ownerEnvironment keeps an explicit environment explicit: a nil list is sent
// as the empty one a Linux command receives, never as "inherit the job's".
func ownerEnvironment(env []string) []string {
	if env == nil {
		return []string{}
	}
	return env
}

// tempDir keeps every socket path inside the 104-byte Unix-socket limit.
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

func jobPlist(label, exe, request, dir string, background bool) []byte {
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
	// RunAtLoad starts the job once. KeepAlive stays false and the job is
	// throttled, so a job launchd outlives is never restarted in a loop.
	body.WriteString(`</array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><false/>
<key>ExitTimeOut</key><integer>2</integer>
<key>ThrottleInterval</key><integer>60</integer>
`)
	if background {
		// The user domain exists without a GUI login; the GUI domain does not.
		body.WriteString("<key>LimitLoadToSessionType</key><string>Background</string>\n")
	}
	body.WriteString("<key>EnvironmentVariables</key><dict><key>" + JobDepthVariable + "</key><string>" + strconv.Itoa(jobDepth()+1) + "</string></dict>\n")
	body.WriteString("</dict></plist>\n")
	return []byte(body.String())
}

func escapeXML(text string) string {
	var out strings.Builder
	_ = xml.EscapeText(&out, []byte(text))
	return out.String()
}

func (p *jobProcess) start() error {
	// Accept before bootstrap so a connecting helper never waits on a full
	// backlog, and bound the handshake on every listener.
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
		stream.conn = conn
		switch name {
		case "in":
			go func() {
				_, err := io.Copy(conn, p.stdin)
				if skipStdinCopyError(err) {
					err = nil
				}
				p.stdinMu.Lock()
				if err != nil && p.stdinErr == nil {
					p.stdinErr = err
				}
				p.stdinMu.Unlock()
				stream.pump <- conn.CloseWrite()
			}()
		case "out":
			go func() {
				_, err := io.Copy(&sharedWriter{mu: stream.writeMu, w: p.stdout}, conn)
				stream.pump <- err
				_ = conn.Close()
			}()
		default:
			go func() {
				_, err := io.Copy(&sharedWriter{mu: stream.writeMu, w: p.stderr}, conn)
				stream.pump <- err
				_ = conn.Close()
			}()
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

// bootstrap loads the job in the GUI domain when the person has one, and in
// the per-user domain otherwise, as on a host without a console session.
func (p *jobProcess) bootstrap() error {
	uid := os.Geteuid()
	var failure error
	for _, attempt := range []struct {
		domain     string
		background bool
	}{{fmt.Sprintf("gui/%d", uid), false}, {fmt.Sprintf("user/%d", uid), true}} {
		// The session type is part of the job definition, so the plist and the
		// request are written for the domain the job is about to load into.
		if err := p.writePlist(attempt.domain, attempt.background); err != nil {
			return err
		}
		if err := launchctl("bootstrap", attempt.domain, p.plist); err != nil {
			failure = errors.Join(failure, err)
			continue
		}
		p.domain, p.loaded = attempt.domain, true
		return nil
	}
	return fmt.Errorf("load launchd job: %w", failure)
}

// writePlist writes the job definition for domain. The helper removes its own
// registration from that domain if the owner disappears, so the domain is part
// of the request rather than something the helper guesses.
func (p *jobProcess) writePlist(domain string, background bool) error {
	var request jobRequest
	data, err := os.ReadFile(p.requestPath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return err
	}
	request.Domain = domain
	if data, err = json.Marshal(request); err != nil {
		return err
	}
	if err := os.WriteFile(p.requestPath, data, 0o600); err != nil {
		return err
	}
	return os.WriteFile(p.plist, jobPlist(p.label, p.exe, p.requestPath, p.dir, background), 0o600)
}

func launchctl(args ...string) error {
	launchctlLock.Lock()
	defer launchctlLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), launchctlTimeout)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "/bin/launchctl", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", args[0], err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (p *jobProcess) kill() error {
	// Teardown scans the process table and calls launchctl, so it never runs
	// inside the owner's lock.
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
	return errors.Join(statusErr, p.stopErr, p.drain(), p.stdinFailure())
}

// stdinFailure reports a reader that failed partway through the input. A
// command that stopped reading first is not a failure, as with os/exec.
func (p *jobProcess) stdinFailure() error {
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	return p.stdinErr
}

func skipStdinCopyError(err error) bool {
	// The job's end of the input closed because the command finished, which
	// is a normal end of input rather than a failed command.
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, net.ErrClosed)
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
		// Nothing starts under the job after it is removed, but a member can
		// have been born between the last scan and the bootout.
		p.stopErr = errors.Join(p.stopErr, p.killCoalition())
	})
}

// killCoalition kills every member until two consecutive scans find none. The
// job stays registered until the scan is clean, so its coalition cannot be
// released while it is being killed.
func (p *jobProcess) killCoalition() error {
	if p.coalition == 0 {
		return nil
	}
	deadline := time.Now().Add(5 * time.Second)
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
// how many it found. The caller, its host and zombies are left alone: a zombie
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
		// Identity was read before the coalition; signal only while the same
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

// bootout removes the job from launchd. It must succeed before the owner
// forgets the job: a registration left behind is a leak that grows with every
// command the host runs. A job that is already gone counts as removed.
func (p *jobProcess) bootout() error {
	if !p.loaded {
		return nil
	}
	p.loaded = false
	deadline := time.Now().Add(launchctlTimeout)
	var failure error
	for {
		err := launchctl("bootout", p.domain+"/"+p.label)
		if err == nil || jobAlreadyRemoved(err) {
			return nil
		}
		failure = err
		if time.Now().After(deadline) {
			return failure
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func jobAlreadyRemoved(err error) bool {
	text := err.Error()
	for _, marker := range []string{"Could not find service", "No such process"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (p *jobProcess) drain() error {
	var err error
	deadline := time.Now().Add(5 * time.Second)
	for _, stream := range p.streams {
		if stream.conn == nil {
			continue
		}
		if stream.writeMu == nil {
			// The caller owns the reader; a reader that never ends keeps this
			// copy blocked. Its error is reported by stdinFailure instead.
			continue
		}
		remaining := time.Until(deadline)
		if remaining < 0 {
			remaining = 0
		}
		select {
		case pumpErr := <-stream.pump:
			err = errors.Join(err, pumpErr)
		case <-time.After(remaining):
			// A stream that outlives teardown would keep the call from
			// returning; closing its end ends the copy and reports why.
			_ = stream.conn.Close()
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
	// Never remove the job's directory while its teardown is still running:
	// the bootout needs the plist, and a job that outlives its definition is
	// a registration nothing will clean up.
	select {
	case <-p.stopped:
	case <-time.After(launchctlTimeout):
	}
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
	if depth := jobDepth(); depth > 1 {
		fmt.Fprintf(diagnostic, "proc-job: a job cannot start another job (depth %d)\n", depth)
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
	// Either way the coalition is killed, and a job left registered is removed
	// by a detached launchctl.
	go func() {
		_, _ = io.Copy(io.Discard, ctl)
		_, _ = killCoalition(coalitions[0])
		endJob(request.Domain, request.Label, request.Temp)
		os.Exit(0)
	}()
	if request.Env == nil {
		// A nil environment would make the command inherit the job's own, so
		// an absent list is the same empty environment Linux gives it.
		request.Env = []string{}
	}
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
	// Hold the job until the owner tears it down, so a descendant that keeps
	// running stays attributable to this coalition.
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
// detached because bootout terminates this process. Every value is an argument
// of a fixed script, never interpolated text.
func endJob(domain, label, dir string) {
	if label == "" || domain == "" {
		return
	}
	script := "sleep 0.1; launchctl bootout \"$1/$2\" 2>/dev/null; rm -rf \"$3\""
	command := exec.Command("/bin/sh", "-c", script, "sh", domain, label, dir)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	command.Env = []string{"PATH=/usr/bin:/bin"}
	if err := command.Start(); err == nil {
		_ = command.Process.Release()
	}
}

// jobDepth reads the nesting this process was started at. launchd gives every
// job the marker, so a job launched from inside a job reports a depth above 1
// and refuses to run, and a job definition always names one level deeper than
// the process writing it.
func jobDepth() int {
	depth, err := strconv.Atoi(os.Getenv(JobDepthVariable))
	if err != nil || depth < 0 {
		return 0
	}
	return depth
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
