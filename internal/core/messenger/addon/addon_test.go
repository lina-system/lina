// SPDX-License-Identifier: Apache-2.0

package addon_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/addon"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/sandbox"
)

// script is a fake add-on: it reads requests and writes the responses a test
// queued for that operation.
type script struct {
	mu        sync.Mutex
	responses map[string][]string
	requests  []map[string]any
	exits     int
}

func (s *script) Run(ctx context.Context, l lease.Lease, command sandbox.Command, _ []string) error {
	s.mu.Lock()
	s.exits++
	s.mu.Unlock()
	reader := bufio.NewScanner(command.Stdin)
	writer := bufio.NewWriter(command.Stdout)
	defer writer.Flush()
	for reader.Scan() {
		var request map[string]any
		if err := json.Unmarshal(reader.Bytes(), &request); err != nil {
			return err
		}
		s.mu.Lock()
		s.requests = append(s.requests, request)
		op, _ := request["op"].(string)
		queue := s.responses[op]
		if len(queue) == 0 {
			s.mu.Unlock()
			continue
		}
		line := queue[0]
		s.responses[op] = queue[1:]
		s.mu.Unlock()
		id, _ := request["id"].(string)
		line = strings.ReplaceAll(line, `"id":""`, `"id":"`+id+`"`)
		if _, err := writer.WriteString(line + "\n"); err != nil {
			return err
		}
		if err := writer.Flush(); err != nil {
			return err
		}
		if op == "quit" {
			return nil
		}
	}
	return nil
}

func (s *script) count(op string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, request := range s.requests {
		if request["op"] == op {
			total++
		}
	}
	return total
}

func session(t *testing.T, s *script) *addon.Session {
	t.Helper()
	dir := t.TempDir()
	value := addon.NewSession("kakaotalk", "account-1", addon.Config{
		Node: "/usr/bin/node", Program: "/addon/main.ts", StateRoot: dir,
		Runner: s, RestartDelay: time.Millisecond, MaxRestarts: 2, RequestTimeout: 2 * time.Second,
	})
	t.Cleanup(func() { _ = value.Close(context.Background()) })
	return value
}

func TestSignInAndChatsReadThroughTheAddon(t *testing.T) {
	t.Parallel()
	s := &script{responses: map[string][]string{
		"signIn":    {`{"id":"","ok":true,"result":{"state":"needs_sign_in"}}`},
		"listChats": {`{"id":"","ok":true,"result":{"chats":[{"id":"chat-1","name":"Direct","kind":"direct"}]}}`},
	}}
	adapter := session(t, s).Adapter()

	state, err := adapter.SignIn(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"})
	if err != nil || state != messenger.NeedsSignIn {
		t.Fatalf("sign-in state = %v, %v", state, err)
	}
	chats, err := adapter.ListChats(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"})
	if err != nil || len(chats) != 1 || chats[0].ID != "chat-1" || chats[0].Kind != "direct" {
		t.Fatalf("chats = %+v, %v", chats, err)
	}
	if s.count("signIn") != 1 || s.count("listChats") != 1 {
		t.Fatalf("requests = %+v", s.requests)
	}
}

func TestUnknownResponseShapeRefusesTheBatch(t *testing.T) {
	t.Parallel()
	for name, response := range map[string]string{
		"unknown field":  `{"id":"","ok":true,"result":{"messages":[],"deletions":[],"next":"","hasMore":false,"extra":1}}`,
		"bad route":      `{"id":"","ok":true,"result":{"messages":[{"id":"m-1","author":{"id":"u1","name":""},"route":"owner","at":"2026-10-24T09:00:00Z","text":""}],"deletions":[],"next":"","hasMore":false}}`,
		"bad time":       `{"id":"","ok":true,"result":{"messages":[{"id":"m-1","author":{"id":"u1","name":""},"route":"person","at":"yesterday","text":""}],"deletions":[],"next":"","hasMore":false}}`,
		"missing id":     `{"id":"","ok":true,"result":{"messages":[{"author":{"id":"u1","name":""},"route":"person","at":"2026-10-24T09:00:00Z","text":""}],"deletions":[],"next":"","hasMore":false}}`,
		"oversized page": `{"id":"","ok":true,"result":{"messages":[],"deletions":[],"next":"","hasMore":false,"through":"never"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := &script{responses: map[string][]string{"page": {response}}}
			_, err := session(t, s).Adapter().Page(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"}, "chat-1", "")
			if !errors.Is(err, addon.ErrUpstreamChanged) {
				t.Fatalf("page error = %v, want upstream changed", err)
			}
		})
	}
}

func TestUnsupportedPlatformFailsOnlyThatSource(t *testing.T) {
	t.Parallel()
	s := &script{responses: map[string][]string{
		"members": {`{"id":"","ok":false,"error":"unsupported_platform"}`},
	}}
	_, err := session(t, s).Adapter().Members(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"}, "chat-1")
	if !errors.Is(err, addon.ErrUnsupportedPlatform) {
		t.Fatalf("members error = %v", err)
	}
}

func TestMissingNodeReportsTheCauseWithoutStartingAChild(t *testing.T) {
	t.Parallel()
	value := addon.NewSession("kakaotalk", "account-1", addon.Config{StateRoot: t.TempDir()})
	t.Cleanup(func() { _ = value.Close(context.Background()) })
	if _, err := value.Adapter().ListChats(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"}); !errors.Is(err, addon.ErrNotConfigured) {
		t.Fatalf("list chats error = %v, want %v", err, addon.ErrNotConfigured)
	}
	if !strings.Contains(addon.ErrNotConfigured.Error(), "Node.js") {
		t.Fatalf("cause does not name Node.js: %v", addon.ErrNotConfigured)
	}
}

func TestRestartIsBounded(t *testing.T) {
	t.Parallel()
	s := &script{responses: map[string][]string{}}
	value := addon.NewSession("kakaotalk", "account-1", addon.Config{
		Node: "/usr/bin/node", Program: "/addon/main.ts", StateRoot: t.TempDir(),
		Runner: s, RestartDelay: time.Millisecond, MaxRestarts: 2, RequestTimeout: time.Second,
	})
	t.Cleanup(func() { _ = value.Close(context.Background()) })
	var err error
	for range 6 {
		_, err = value.Adapter().ListChats(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"})
		if err != nil && errors.Is(err, addon.ErrStopped) {
			break
		}
	}
	if err == nil || !errors.Is(err, addon.ErrStopped) {
		t.Fatalf("error after bounded restarts = %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The first start plus the bounded restarts, and never a sixth.
	if s.exits > 3 {
		t.Fatalf("child started %d times, want at most 3", s.exits)
	}
}

func TestOversizedLineFromTheAddonFailsAsUpstreamChanged(t *testing.T) {
	t.Parallel()
	s := oversized{}
	dir := t.TempDir()
	value := addon.NewSession("kakaotalk", "account-1", addon.Config{
		Node: "/usr/bin/node", Program: "/addon/main.ts", StateRoot: dir,
		Runner: s, RestartDelay: time.Millisecond, MaxRestarts: 0, RequestTimeout: 2 * time.Second,
	})
	t.Cleanup(func() { _ = value.Close(context.Background()) })
	_, err := value.Adapter().ListChats(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"})
	if !errors.Is(err, addon.ErrStopped) {
		t.Fatalf("error = %v", err)
	}
}

type oversized struct{}

func (oversized) Run(_ context.Context, _ lease.Lease, command sandbox.Command, _ []string) error {
	reader := bufio.NewScanner(command.Stdin)
	if reader.Scan() {
		_, _ = command.Stdout.Write([]byte(`{"id":"r1","ok":true,"result":{"chats":[` + strings.Repeat("x", 9<<20) + `]}}` + "\n"))
	}
	return nil
}

// fixtureProgram is the recorded add-on program, found from this file so a
// test binary started outside the package directory still finds it.
func fixtureProgram(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test source")
	}
	program := filepath.Join(filepath.Dir(source), "..", "..", "..", "..", "adapters", "messenger", "fixture-main.ts")
	program, err := filepath.Abs(program)
	if err != nil {
		t.Fatal(err)
	}
	return program
}

// TestRealAddonProgramThroughTheSupervisor runs the add-on's own program. It
// needs the person's Node.js but no SDK, network or credential.
func TestRealAddonProgramThroughTheSupervisor(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is absent")
	}
	program := fixtureProgram(t)
	if _, err := os.Stat(program); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	value := addon.NewSession("kakaotalk", "account-1", addon.Config{
		Node: node, Program: program, StateRoot: root, Runner: plainRunner{},
		RestartDelay: time.Millisecond, MaxRestarts: 1, RequestTimeout: 5 * time.Second,
	})
	t.Cleanup(func() { _ = value.Close(context.Background()) })
	adapter := value.Adapter()
	chats, err := adapter.ListChats(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"})
	if err != nil || len(chats) != 1 {
		t.Fatalf("chats = %+v, %v", chats, err)
	}
	page, err := adapter.Page(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"}, "dm-1", "")
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != "m-1" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	if _, err := adapter.Members(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"}, "dm-1"); err != nil {
		t.Fatal(err)
	}
	// The data directory is the add-on's own, 0700 and outside the canon.
	entries, err := os.ReadDir(filepath.Join(root, "plugins", "messenger-addon"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("data directories = %v, %v", entries, err)
	}
	info, err := os.Stat(filepath.Join(root, "plugins", "messenger-addon", entries[0].Name()))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("data directory mode = %v, %v", info.Mode().Perm(), err)
	}
}

// plainRunner runs the program without the OS sandbox, for hosts whose sandbox
// is unavailable; the sandbox path has its own test.
type plainRunner struct{}

func (plainRunner) Run(ctx context.Context, _ lease.Lease, command sandbox.Command, env []string) error {
	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = command.Stdin, command.Stdout, io.Discard
	return cmd.Run()
}

func TestResponseEnvelopeAndRequiredPageFields(t *testing.T) {
	for name, response := range map[string]string{
		"unknown envelope": `{"id":"","ok":true,"result":{"messages":[],"deletions":[],"next":"","hasMore":false},"extra":true}`,
		"missing fields":   `{"id":"","ok":true,"result":{}}`,
		"null page":        `{"id":"","ok":true,"result":null}`,
		"unknown event":    `{"event":"unexpected"}`,
		"missing ok":       `{"id":"","result":{"messages":[],"deletions":[],"next":"","hasMore":false}}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := &script{responses: map[string][]string{"page": {response}}}
			_, err := session(t, s).Adapter().Page(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"}, "chat-1", "")
			if !errors.Is(err, addon.ErrUpstreamChanged) {
				t.Fatalf("got %v, want upstream changed", err)
			}
		})
	}
}

func TestAccountMismatchNeverReachesTheProcess(t *testing.T) {
	s := &script{responses: map[string][]string{}}
	a := session(t, s).Adapter()
	for _, id := range []messenger.SourceID{{Platform: "discord", Account: "account-1"}, {Platform: "kakaotalk", Account: "account-2"}, {Platform: "kakaotalk"}} {
		_, e1 := a.SignIn(t.Context(), id)
		_, e2 := a.ListChats(t.Context(), id)
		_, e3 := a.Page(t.Context(), id, "chat", "")
		_, e4 := a.Members(t.Context(), id, "chat")
		e5 := a.Live(t.Context(), id, func(messenger.Event) error { return nil })
		_, e6 := a.FetchAttachment(t.Context(), id, "chat", "message", "file")
		for _, err := range []error{e1, e2, e3, e4, e5, e6} {
			if !errors.Is(err, messenger.ErrInvalid) {
				t.Fatalf("wrong source: %v", err)
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exits != 0 {
		t.Fatalf("started %d children", s.exits)
	}
}

func TestLiveReturnsWhenTheAddonReportsItsEnd(t *testing.T) {
	t.Parallel()
	id := messenger.SourceID{Platform: "kakaotalk", Account: "account-1"}
	for _, tc := range []struct {
		frame string
		want  error
	}{
		{`{"event":"ended","error":"failed:ECONNRESET"}`, addon.ErrLiveEnded},
		{`{"event":"ended","error":"needs_sign_in"}`, messenger.ErrNeedsSignIn},
	} {
		s := &script{responses: map[string][]string{
			"live":   {`{"id":"","ok":true,"result":{"live":true}}` + "\n" + tc.frame},
			"signIn": {`{"id":"","ok":true,"result":{"state":"signed_in"}}`},
		}}
		adapter := session(t, s).Adapter()
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		err := adapter.Live(ctx, id, func(messenger.Event) error { return nil })
		expired := ctx.Err()
		cancel()
		if expired != nil || !errors.Is(err, tc.want) {
			t.Fatalf("%s: live returned %v (context %v), want %v", tc.frame, err, expired, tc.want)
		}
		if state, err := adapter.SignIn(t.Context(), id); err != nil || state != messenger.SignedIn {
			t.Fatalf("%s: the add-on stopped serving after the live end: %v, %v", tc.frame, state, err)
		}
	}
}

func TestMalformedLiveEndIsRefused(t *testing.T) {
	t.Parallel()
	for _, frame := range []string{
		`{"event":"ended"}`,
		`{"event":"ended","error":"failed","message":{"id":"m"}}`,
		`{"event":"message","error":"failed","message":{"id":"m","author":{"id":"u","name":""},"route":"system","at":"2026-10-24T09:00:00Z","text":""}}`,
	} {
		s := &script{responses: map[string][]string{
			"live": {`{"id":"","ok":true,"result":{"live":true}}` + "\n" + frame},
		}}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		err := session(t, s).Adapter().Live(ctx, messenger.SourceID{Platform: "kakaotalk", Account: "account-1"}, func(messenger.Event) error { return nil })
		cancel()
		if !errors.Is(err, addon.ErrUpstreamChanged) {
			t.Fatalf("%s: got %v, want upstream changed", frame, err)
		}
	}
}

func TestSignInRelaysAnAnswerToTheSameAccountSession(t *testing.T) {
	s := &script{responses: map[string][]string{
		"beginSignIn": {`{"id":"","ok":true,"result":{"state":"needs_sign_in","prompt":{"key":"pin","kind":"secret","text":"PIN"}}}`},
		"answer":      {`{"id":"","ok":true,"result":{"state":"signed_in"}}`},
		"whoami":      {`{"id":"","ok":true,"result":{"author":{"id":"u0","name":"Owner"}}}`},
	}}
	value := session(t, s)
	step, err := value.BeginSignIn(t.Context())
	if err != nil || step.Prompt == nil || step.Prompt.Key != "pin" {
		t.Fatalf("prompt: %+v, %v", step, err)
	}
	step, err = value.Answer(t.Context(), "pin", "synthetic-pin")
	if err != nil || step.State != messenger.SignedIn {
		t.Fatalf("answer: %+v, %v", step, err)
	}
	author, err := value.WhoAmI(t.Context())
	if err != nil || author.ID != "u0" {
		t.Fatalf("whoami: %+v, %v", author, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exits != 1 || len(s.requests) != 3 || s.requests[1]["value"] != "synthetic-pin" {
		t.Fatalf("relay did not use one session")
	}
}

func TestSandboxedAddonProgramUsesItsPrivateDirectory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux sandbox acceptance")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	launcher, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("Bubblewrap absent")
	}
	socket := filepath.Join(root, "core.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	value := addon.NewSession("kakaotalk", "account-1", addon.Config{Node: node, Program: fixtureProgram(t), StateRoot: root, Socket: socket, Launcher: launcher, RequestTimeout: 5 * time.Second})
	defer value.Close(context.Background())
	_, err = value.Adapter().ListChats(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"})
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
}

func TestChildExitUnblocksPendingRead(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	program := filepath.Join(root, "exit.mjs")
	if err := os.WriteFile(program, []byte("process.stdin.once('data', () => process.exit(7));\n"), 0600); err != nil {
		t.Fatal(err)
	}
	value := addon.NewSession("kakaotalk", "account-1", addon.Config{Node: node, Program: program, StateRoot: root, Runner: plainRunner{}, RequestTimeout: 30 * time.Second})
	defer value.Close(context.Background())
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, err = value.Adapter().ListChats(ctx, messenger.SourceID{Platform: "kakaotalk", Account: "account-1"})
	if !errors.Is(err, addon.ErrStopped) {
		t.Fatalf("child exited but pending read did not stop: %v", err)
	}
}

// countingRunner runs the program with the caller's context, so a cancelled
// request kills the child exactly as the sandbox does.
type countingRunner struct {
	mu     sync.Mutex
	starts int
}

func (r *countingRunner) Run(ctx context.Context, _ lease.Lease, command sandbox.Command, env []string) error {
	r.mu.Lock()
	r.starts++
	r.mu.Unlock()
	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = command.Stdin, command.Stdout, io.Discard
	return cmd.Run()
}

func (r *countingRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

// TestFinishedRequestKeepsTheAccountProcess proves a request's context ends
// with that request. The account's process belongs to the session: a read that
// was cancelled, such as a scheduled sync that completed, must not stop the
// child that also serves the account's live subscription.
func TestFinishedRequestKeepsTheAccountProcess(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is absent")
	}
	runner := &countingRunner{}
	value := addon.NewSession("fixture", "account-1", addon.Config{
		Node: node, Program: fixtureProgram(t), StateRoot: t.TempDir(), Runner: runner,
		RestartDelay: time.Millisecond, MaxRestarts: 1, RequestTimeout: 5 * time.Second,
	})
	t.Cleanup(func() { _ = value.Close(context.Background()) })
	adapter := value.Adapter()
	id := messenger.SourceID{Platform: "fixture", Account: "account-1"}

	request, cancel := context.WithCancel(t.Context())
	if _, err := adapter.ListChats(request, id); err != nil {
		t.Fatal(err)
	}
	cancel()
	// The cancelled request's child exits before the next read asks for one.
	time.Sleep(100 * time.Millisecond)
	if _, err := adapter.ListChats(t.Context(), id); err != nil {
		t.Fatalf("the next read failed after a cancelled request: %v", err)
	}
	if got := runner.count(); got != 1 {
		t.Fatalf("child started %d times, want the one process of the account", got)
	}
}

// TestStoppingALiveReaderStopsItsSubscription proves Core tells the add-on to
// end a subscription it no longer reads, and that the account's process keeps
// serving afterwards.
func TestStoppingALiveReaderStopsItsSubscription(t *testing.T) {
	t.Parallel()
	s := &script{responses: map[string][]string{
		"live":   {`{"id":"","ok":true,"result":{"live":true}}`},
		"signIn": {`{"id":"","ok":true,"result":{"state":"signed_in"}}`},
	}}
	adapter := session(t, s).Adapter()
	id := messenger.SourceID{Platform: "kakaotalk", Account: "account-1"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The reader leaves once its subscription is running.
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for s.count("live") == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if err := adapter.Live(ctx, id, func(messenger.Event) error { return nil }); err != nil {
		t.Fatalf("live returned %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.count("stop") != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("subscription stop requests = %d, want 1", s.count("stop"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if state, err := adapter.SignIn(t.Context(), id); err != nil || state != messenger.SignedIn {
		t.Fatalf("the add-on stopped serving after the reader left: %v, %v", state, err)
	}
	if got := s.count("signIn"); got != 1 {
		t.Fatalf("sign-in requests = %d, want the same session", got)
	}
}
