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
	// liveEvent is written after the answer to a live request, so a test can
	// observe the subscription Core established. stopSeen signals the first stop
	// request, so a test synchronizes on the add-on instead of a delay.
	liveEvent string
	stopSeen  chan struct{}
	stopOnce  sync.Once
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
		if op == "stop" && s.stopSeen != nil {
			s.stopOnce.Do(func() { close(s.stopSeen) })
		}
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
		if op == "live" && s.liveEvent != "" {
			if _, err := writer.WriteString(s.liveEvent + "\n"); err != nil {
				return err
			}
			if err := writer.Flush(); err != nil {
				return err
			}
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

// A single-account platform keeps one process and one data directory for the
// platform, so the source's account may change from the pending sign-in to the
// platform's own user id. Stopping it ends the session; the next read starts a
// new one.
func TestSingleAccountPlatformKeepsOneSessionUntilStopped(t *testing.T) {
	t.Parallel()
	s := &script{responses: map[string][]string{
		"listChats": {`{"id":"","ok":true,"result":{"chats":[{"id":"chat-1","name":"Direct","kind":"direct"}]}}`,
			`{"id":"","ok":true,"result":{"chats":[{"id":"chat-1","name":"Direct","kind":"direct"}]}}`,
			`{"id":"","ok":true,"result":{"chats":[{"id":"chat-1","name":"Direct","kind":"direct"}]}}`},
	}}
	root := t.TempDir()
	manager := addon.NewManager(addon.Config{
		Node: "/usr/bin/node", Program: "/addon/main.ts", StateRoot: root,
		Runner: s, RestartDelay: time.Millisecond, MaxRestarts: 2, RequestTimeout: 2 * time.Second,
		SingleAccount: []string{"kakaotalk"},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	first, err := manager.Addon("kakaotalk", "7788")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = first.ListChats(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "7788"}); err != nil {
		t.Fatal(err)
	}
	second, err := manager.Addon("kakaotalk", "7788")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = second.ListChats(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "7788"}); err != nil {
		t.Fatal(err)
	}
	if s.exits != 1 {
		t.Fatalf("the account change started %d children, want 1", s.exits)
	}
	entries, err := os.ReadDir(filepath.Join(root, "plugins", "messenger-addon"))
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "kakaotalk-") {
		t.Fatalf("data directories = %v, %v", entries, err)
	}
	if err = manager.Stop(t.Context(), "kakaotalk", "7788"); err != nil {
		t.Fatal(err)
	}
	third, err := manager.Addon("kakaotalk", "7788")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = third.ListChats(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "7788"}); err != nil {
		t.Fatal(err)
	}
	if s.exits != 2 {
		t.Fatalf("after the stop the child started %d times, want 2", s.exits)
	}
}

// One session and one data directory serve a single-account platform. Removing
// it for an account the platform never named leaves the owner's session alone.
func TestRemoveLeavesTheOwnersSessionAlone(t *testing.T) {
	t.Parallel()
	chats := `{"id":"","ok":true,"result":{"chats":[{"id":"chat-1","name":"Direct","kind":"direct"}]}}`
	s := &script{responses: map[string][]string{"listChats": {chats, chats}}}
	root := t.TempDir()
	manager := addon.NewManager(addon.Config{
		Node: "/usr/bin/node", Program: "/addon/main.ts", StateRoot: root,
		Runner: s, RestartDelay: time.Millisecond, MaxRestarts: 2, RequestTimeout: 2 * time.Second,
		SingleAccount: []string{"kakaotalk"},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	first, err := manager.Start("kakaotalk", "7788")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Adapter().ListChats(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "7788"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start("kakaotalk", "7788"); err != nil {
		t.Fatal(err)
	}
	dir := addon.DataDir(root, "kakaotalk", "kakaotalk")
	if err := manager.Remove(t.Context(), "kakaotalk", "pending"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("removing another account ended the owner's session: %v", err)
	}
	owner, err := manager.Addon("kakaotalk", "7788")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ListChats(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "7788"}); err != nil {
		t.Fatal(err)
	}
	if s.exits != 1 {
		t.Fatalf("the owner's session started %d times, want 1", s.exits)
	}
	if err := manager.Remove(t.Context(), "kakaotalk", "7788"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the owner's session survived its own removal: %v", err)
	}
}

// The platform driver's descriptor reaches Core with the sign-in state every
// read asks for, so the schedule and the read window come from the platform
// rather than from the add-on's general bound.
func TestPlatformDescriptorReachesTheAdapter(t *testing.T) {
	t.Parallel()
	s := &script{responses: map[string][]string{
		"signIn": {`{"id":"","ok":true,"result":{"state":"signed_in","capabilities":{"historyDepth":"platform-window","cursorKind":"log-id","edits":false,"deletions":false,"liveEvents":true,"intervalSeconds":900,"readWindowSeconds":172800}}}`},
	}}
	adapter := session(t, s).Adapter()
	if got := adapter.Capabilities(); got.CursorKind != "opaque" {
		t.Fatalf("descriptor before the first read: %+v", got)
	}
	state, err := adapter.SignIn(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"})
	if err != nil || state != messenger.SignedIn {
		t.Fatalf("sign-in state %v, %v", state, err)
	}
	caps := adapter.Capabilities()
	if caps.CursorKind != "log-id" || caps.Edits || caps.Deletions || !caps.LiveEvents || caps.ReadWindow != 48*time.Hour || caps.Interval != 15*time.Minute {
		t.Fatalf("descriptor %+v", caps)
	}
	if s.count("signIn") != 1 || s.count("capabilities") != 0 {
		t.Fatalf("the descriptor cost an extra frame: %+v", s.requests)
	}
}

// A descriptor with a field Core does not know refuses the read instead of
// guessing the platform's rules.
func TestUnknownDescriptorFieldRefusesTheRead(t *testing.T) {
	t.Parallel()
	s := &script{responses: map[string][]string{
		"signIn": {`{"id":"","ok":true,"result":{"state":"signed_in","capabilities":{"historyDepth":"platform-window","cursorKind":"log-id","edits":false,"deletions":false,"liveEvents":true,"intervalSeconds":900,"readWindowSeconds":172800,"writes":["send"]}}}`},
	}}
	adapter := session(t, s).Adapter()
	if _, err := adapter.SignIn(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"}); !errors.Is(err, addon.ErrUpstreamChanged) {
		t.Fatalf("unknown field: %v", err)
	}
	if got := adapter.Capabilities(); got.CursorKind != "opaque" {
		t.Fatalf("a refused descriptor was used: %+v", got)
	}
}

// A page carries the platform's edit time, an empty body and its attachment
// metadata through the add-on without inventing text.
func TestPageCarriesAnEditAnEmptyBodyAndAttachments(t *testing.T) {
	t.Parallel()
	s := &script{responses: map[string][]string{
		"page": {`{"id":"","ok":true,"result":{"messages":[{"id":"9007199254740993123","author":{"id":"42","name":"Someone"},"route":"external_participant","at":"2026-10-24T09:00:00Z","edited":"2026-10-24T09:05:00Z","text":"","attachments":[{"id":"file-key","name":"report.pdf","mimeType":"application/pdf","size":2048}]}],"deletions":[],"next":"9007199254740993123","hasMore":false,"through":"2026-10-24T09:05:00Z"}}`},
	}}
	page, err := session(t, s).Adapter().Page(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"}, "chat-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("page %+v", page)
	}
	message := page.Messages[0]
	if message.ID != "9007199254740993123" || message.Text != "" || message.Edited.IsZero() || len(message.Attachments) != 1 {
		t.Fatalf("message %+v", message)
	}
	if page.Through.IsZero() || page.HasMore || page.Next != "9007199254740993123" {
		t.Fatalf("page position %+v", page)
	}
}

func TestManagerKeepsPlatformNamespaces(t *testing.T) {
	manager := addon.NewManager(addon.Config{StateRoot: t.TempDir()})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	first, err := manager.Start("line", "same")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Start("discord", "same")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || second.Platform() != "discord" || second.Account() != "same" {
		t.Fatal("different platforms shared a session")
	}
}

func TestSingleAccountOwnerCannotBeReassigned(t *testing.T) {
	manager := addon.NewManager(addon.Config{StateRoot: t.TempDir(), SingleAccount: []string{"kakaotalk"}})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	pending, err := manager.Start("kakaotalk", "")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := manager.Start("kakaotalk", "7788")
	if err != nil || owner != pending {
		t.Fatalf("claim: %v", err)
	}
	if _, err := manager.Start("kakaotalk", "old-account"); !errors.Is(err, messenger.ErrInvalid) {
		t.Fatalf("historical account reassigned the shared session: %v", err)
	}
	if _, err := owner.Adapter().ListChats(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "old-account"}); !errors.Is(err, messenger.ErrInvalid) {
		t.Fatalf("read another account through the owner's session: %v", err)
	}
}

// recordingRunner runs the program with the context the supervisor gave it, so
// a cancelled request kills the child exactly as the sandbox does, and keeps
// that context for the test to inspect.
type recordingRunner struct {
	mu    sync.Mutex
	child []context.Context
}

func (r *recordingRunner) Run(ctx context.Context, _ lease.Lease, command sandbox.Command, env []string) error {
	r.mu.Lock()
	r.child = append(r.child, ctx)
	r.mu.Unlock()
	cmd := exec.CommandContext(ctx, command.Path, command.Args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = command.Stdin, command.Stdout, io.Discard
	return cmd.Run()
}

func (r *recordingRunner) starts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.child)
}

func (r *recordingRunner) context(index int) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.child[index]
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
	runner := &recordingRunner{}
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
	// Cancelling a parent context closes the contexts derived from it
	// synchronously, so this reads the child's lifetime without a delay.
	if err := runner.context(0).Err(); err != nil {
		t.Fatalf("the request's cancellation ended the account's process: %v", err)
	}
	if _, err := adapter.ListChats(t.Context(), id); err != nil {
		t.Fatalf("the next read failed after a cancelled request: %v", err)
	}
	if got := runner.starts(); got != 1 {
		t.Fatalf("child started %d times, want the one process of the account", got)
	}
}

// TestStoppingALiveReaderStopsItsSubscription proves Core tells the add-on to
// end a subscription it no longer reads, and that the account's process keeps
// serving afterwards.
func TestStoppingALiveReaderStopsItsSubscription(t *testing.T) {
	t.Parallel()
	s := &script{
		responses: map[string][]string{
			"live":   {`{"id":"","ok":true,"result":{"live":true}}`},
			"signIn": {`{"id":"","ok":true,"result":{"state":"signed_in"}}`},
		},
		liveEvent: `{"event":"message","chat":"dm-1","message":{"id":"m-1","author":{"id":"u1","name":"Someone"},"route":"external_participant","at":"2026-10-24T09:00:00Z","text":"hi"}}`,
		stopSeen:  make(chan struct{}),
	}
	adapter := session(t, s).Adapter()
	id := messenger.SourceID{Platform: "kakaotalk", Account: "account-1"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The reader leaves once Core has admitted an event of the subscription.
	established := make(chan struct{})
	var once sync.Once
	go func() {
		<-established
		cancel()
	}()
	err := adapter.Live(ctx, id, func(messenger.Event) error {
		once.Do(func() { close(established) })
		return nil
	})
	if err != nil {
		t.Fatalf("live returned %v", err)
	}
	select {
	case <-s.stopSeen:
	case <-time.After(10 * time.Second):
		t.Fatal("the add-on was never told to end the subscription")
	}
	if state, err := adapter.SignIn(t.Context(), id); err != nil || state != messenger.SignedIn {
		t.Fatalf("the add-on stopped serving after the reader left: %v, %v", state, err)
	}
	if got := s.count("signIn"); got != 1 {
		t.Fatalf("sign-in requests = %d, want the same session", got)
	}
}

func TestAReadReportsARevokedSession(t *testing.T) {
	s := &script{responses: map[string][]string{"page": {`{"id":"","ok":false,"error":"needs_sign_in"}`}}}
	_, err := session(t, s).Adapter().Page(t.Context(), messenger.SourceID{Platform: "kakaotalk", Account: "account-1"}, "chat-1", "")
	if !errors.Is(err, messenger.ErrNeedsSignIn) {
		t.Fatalf("revoked read: %v", err)
	}
}
