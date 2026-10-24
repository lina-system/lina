// SPDX-License-Identifier: Apache-2.0

package kakaotalk_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/addon"
	"github.com/thisisjun786/lina/internal/core/messenger/kakaotalk"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/sandbox"
)

const person = "person"

// script stands in for the add-on's process: it answers each operation with the
// frame a test queued and records every request the service sent.
type script struct {
	mu        sync.Mutex
	responses map[string][]string
	requests  []map[string]any
}

func (s *script) Run(_ context.Context, _ lease.Lease, command sandbox.Command, _ []string) error {
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
		if _, err := writer.WriteString(strings.ReplaceAll(line, `"id":""`, `"id":"`+id+`"`) + "\n"); err != nil {
			return err
		}
		if err := writer.Flush(); err != nil {
			return err
		}
	}
	return nil
}

func (s *script) ops() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.requests))
	for _, request := range s.requests {
		op, _ := request["op"].(string)
		out = append(out, op)
	}
	return out
}

// harness is one state root, the add-on sessions the service starts and the
// store the source is recorded in.
type harness struct {
	root    string
	script  *script
	service *kakaotalk.Service
	store   *messenger.Store
	manager *addon.Manager
	now     time.Time
}

func newHarness(t *testing.T, responses map[string][]string) *harness {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controls.Close() })
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(context.Background()) })
	store := messenger.Open(db, nil, nil)
	run := &script{responses: responses}
	manager := addon.NewManager(addon.Config{
		Node: "/usr/bin/node", Program: "/addon/main.ts", StateRoot: root, Runner: run,
		RestartDelay: time.Millisecond, RequestTimeout: 2 * time.Second,
		// KakaoTalk keeps one account per main: the sign-in's pending account and
		// the platform's account id share one process and one data directory.
		SingleAccount: []string{kakaotalk.Platform},
	})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	h := &harness{root: root, script: run, store: store, manager: manager, now: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
	h.service = kakaotalk.NewService(kakaotalk.Config{
		Person: person, Store: store, Sessions: manager, Now: func() time.Time { return h.now },
	})
	return h
}

const (
	signedIn       = `{"id":"","ok":true,"result":{"state":"signed_in"}}`
	needsSignIn    = `{"id":"","ok":true,"result":{"state":"needs_sign_in"}}`
	emailPrompt    = `{"id":"","ok":true,"result":{"state":"needs_sign_in","prompt":{"key":"email","kind":"text","text":"KakaoTalk email"}}}`
	passwordPrompt = `{"id":"","ok":true,"result":{"state":"needs_sign_in","prompt":{"key":"password","kind":"secret","text":"KakaoTalk password"}}}`
	passcode       = `{"id":"","ok":true,"result":{"state":"needs_sign_in","show":{"kind":"code","value":"4821"}}}`
	me             = `{"id":"","ok":true,"result":{"author":{"id":"7788","name":"Me"}}}`
	// The driver's descriptor travels with the sign-in state Core already asks for.
	withDescriptor = `{"id":"","ok":true,"result":{"state":"signed_in","capabilities":{"historyDepth":"platform-window","cursorKind":"log-id","edits":false,"deletions":false,"liveEvents":true,"intervalSeconds":900,"readWindowSeconds":172800}}}`
)

// The connect card's acceptance is the person's act: a card opened and accepted
// leaves no source until the platform names the account, and then the record
// carries the accepted term and its date.
func TestConnectRecordsAcceptedTermsOnlyWhenThePlatformNamesTheAccount(t *testing.T) {
	h := newHarness(t, map[string][]string{
		"beginSignIn": {emailPrompt, signedIn},
		"answer":      {passwordPrompt, passcode},
		"whoami":      {me},
	})
	if _, err := h.service.Connect(t.Context(), "connection", false); !errors.Is(err, messenger.ErrInvalid) {
		t.Fatalf("connect without acceptance: %v", err)
	}
	if sources, _ := h.service.Sources(t.Context()); len(sources) != 0 {
		t.Fatalf("a refused connect recorded a source: %+v", sources)
	}
	step, err := h.service.Connect(t.Context(), "connection", true)
	if err != nil || step.State != messenger.NeedsSignIn || step.Prompt == nil || step.Prompt.Key != "email" {
		t.Fatalf("first step %+v, %v", step, err)
	}
	if sources, _ := h.service.Sources(t.Context()); len(sources) != 0 {
		t.Fatalf("a pending connect recorded a source: %+v", sources)
	}
	if _, err = h.service.Answer(t.Context(), "connection", "email", "person@example.test"); err != nil {
		t.Fatal(err)
	}
	step, err = h.service.Answer(t.Context(), "connection", "password", "synthetic-password")
	if err != nil || step.State != messenger.NeedsSignIn || step.Show == nil || step.Show.Value != "4821" {
		t.Fatalf("passcode step %+v, %v", step, err)
	}
	// The phone confirmation finishes; the same connection asks again.
	step, err = h.service.Connect(t.Context(), "connection", true)
	if err != nil || step.State != messenger.SignedIn || step.Account != "7788" {
		t.Fatalf("signed-in step %+v, %v", step, err)
	}
	sources, err := h.service.Sources(t.Context())
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources %+v, %v", sources, err)
	}
	source := sources[0]
	if source.Account != "7788" || source.State != messenger.StateConnected || source.Adapter != kakaotalk.AdapterName || source.Mode != messenger.ModeUnofficial {
		t.Fatalf("recorded source %+v", source)
	}
	if source.AcceptedTerms == nil || source.AcceptedTerms.Text != kakaotalk.Terms || source.AcceptedTerms.At.IsZero() {
		t.Fatalf("accepted terms %+v", source.AcceptedTerms)
	}
	// The passcode is shown, never stored: the record carries only the accepted
	// term, and the step that showed the code carries just that code.
	recorded, _ := json.Marshal(source)
	if strings.Contains(string(recorded), "4821") {
		t.Fatalf("a sign-in step was written to the source record: %s", recorded)
	}
}

// KakaoTalk is one account per main: a second connect is refused before a second
// session starts.
func TestSecondAccountIsRefused(t *testing.T) {
	h := newHarness(t, map[string][]string{"beginSignIn": {signedIn}, "whoami": {me}})
	if _, err := h.service.Connect(t.Context(), "first", true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Connect(t.Context(), "second", true); !errors.Is(err, kakaotalk.ErrConnected) {
		t.Fatalf("second account: %v", err)
	}
}

// A source whose stored session the platform rejected signs in again without
// losing its recorded acceptance.
func TestSignInAgainKeepsAcceptance(t *testing.T) {
	h := newHarness(t, map[string][]string{"beginSignIn": {signedIn, signedIn}, "whoami": {me, me}})
	if _, err := h.service.Connect(t.Context(), "connect", true); err != nil {
		t.Fatal(err)
	}
	before, err := h.service.Sources(t.Context())
	if err != nil || len(before) != 1 {
		t.Fatal(err)
	}
	step, err := h.service.SignIn(t.Context(), "again", "7788")
	if err != nil || step.State != messenger.SignedIn {
		t.Fatalf("sign in again %+v, %v (ops %v)", step, err, h.script.ops())
	}
	after, err := h.service.Sources(t.Context())
	if err != nil || len(after) != 1 {
		t.Fatal(err)
	}
	if after[0].AcceptedTerms == nil || *after[0].AcceptedTerms != *before[0].AcceptedTerms {
		t.Fatalf("acceptance changed: %+v", after[0].AcceptedTerms)
	}
	if !after[0].ConnectedAt.Equal(before[0].ConnectedAt) {
		t.Fatalf("connected date moved: %v", after[0].ConnectedAt)
	}
	if _, err := h.service.SignIn(t.Context(), "again", "other"); !errors.Is(err, messenger.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
}

// An answer without a live connection-bound attempt is refused, and a relay
// stops accepting answers after its lifetime.
func TestAnswerNeedsALiveAttempt(t *testing.T) {
	h := newHarness(t, map[string][]string{"beginSignIn": {emailPrompt}, "answer": {passwordPrompt}})
	if _, err := h.service.Answer(t.Context(), "connection", "email", "person@example.test"); !errors.Is(err, kakaotalk.ErrNoAttempt) {
		t.Fatalf("answer before connect: %v", err)
	}
	if _, err := h.service.Connect(t.Context(), "connection", true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Answer(t.Context(), "other", "email", "person@example.test"); !errors.Is(err, kakaotalk.ErrNoAttempt) {
		t.Fatalf("another connection answered: %v", err)
	}
	if _, err := h.service.Answer(t.Context(), "connection", "email", "person@example.test"); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(11 * time.Minute)
	if _, err := h.service.Answer(t.Context(), "connection", "email", "person@example.test"); !errors.Is(err, kakaotalk.ErrNoAttempt) {
		t.Fatal("a relay past its lifetime kept accepting answers")
	}
}

// Disconnect stops the source and removes the add-on's session directory.
func TestDisconnectRemovesTheAddonSession(t *testing.T) {
	h := newHarness(t, map[string][]string{"beginSignIn": {signedIn}, "whoami": {me}})
	if _, err := h.service.Connect(t.Context(), "connect", true); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Disconnect(t.Context(), "7788"); err != nil {
		t.Fatal(err)
	}
	sources, err := h.service.Sources(t.Context())
	if err != nil || len(sources) != 1 || sources[0].State != messenger.StateDisconnected {
		t.Fatalf("source after disconnect: %+v, %v", sources, err)
	}
	if _, err := os.Stat(addon.DataDir(h.root, kakaotalk.Platform, kakaotalk.Platform)); !os.IsNotExist(err) {
		t.Fatalf("add-on data directory survived the disconnect: %v", err)
	}
	if err := h.service.Disconnect(t.Context(), ""); !errors.Is(err, messenger.ErrInvalid) {
		t.Fatalf("empty account: %v", err)
	}
}

// Every read names the platform it acts for; the one session for this main
// serves only KakaoTalk and refuses a read that names no account. The session
// reports the platform driver's descriptor, not the add-on's general bound.
func TestAdapterBindsTheSourcePlatform(t *testing.T) {
	h := newHarness(t, map[string][]string{"signIn": {withDescriptor}})
	adapter, err := h.manager.Addon(kakaotalk.Platform, "7788")
	if err != nil {
		t.Fatal(err)
	}
	state, err := adapter.SignIn(t.Context(), messenger.SourceID{Platform: kakaotalk.Platform, Account: "7788"})
	if err != nil || state != messenger.SignedIn {
		t.Fatalf("sign-in state %v, %v", state, err)
	}
	for _, id := range []messenger.SourceID{
		{Platform: "discord", Account: "7788"},
		{Platform: kakaotalk.Platform},
	} {
		if _, err := adapter.ListChats(t.Context(), id); !errors.Is(err, messenger.ErrInvalid) {
			t.Fatalf("read through another source %+v: %v", id, err)
		}
	}
	if got := adapter.Capabilities(); got.CursorKind != "log-id" || got.Edits || got.Deletions || len(got.Writes) != 0 {
		t.Fatalf("capabilities %+v", got)
	}
}
