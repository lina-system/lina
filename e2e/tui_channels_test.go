// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIChannelsInstalledCore(t *testing.T) {
	testTUIControlInstalledCore(t, "m1/tui-channels.json", protocol.KindChannelResult)
}
func TestM1TUITalkLogInstalledCore(t *testing.T) {
	testTUIControlInstalledCore(t, "m1/tui-talk-log.json", protocol.KindAgentTalk)
}
func testTUIControlInstalledCore(t *testing.T, path string, kind protocol.Kind) {
	t.Helper()
	var scenario struct{ Command, Reply string }
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		emitResponse(w, scenario.Reply, true)
	}))
	t.Cleanup(provider.Close)
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	if kind == protocol.KindAgentTalk {
		h.stopCore(core)
		seedTalkExchange(t, h)
		core = start(true)
	}
	coreSocket := h.socket
	tap := newAccountTap(t, h, coreSocket)
	h.socket = tap.socket
	terminal := h.startScreenTUI("TERM=xterm-256color")
	waitComposerReady(t, h, terminal)
	// A control view is usable only when the installed composition actually
	// registers its service; do not wait for a reply from an unsupported view.
	capability := "channels"
	if kind == protocol.KindAgentTalk {
		capability = "exchanges"
	}
	for {
		e := tap.next(t, h)
		welcome, ok := e.Payload.(protocol.Welcome)
		if !ok {
			continue
		}
		granted := false
		for _, cap := range welcome.Capabilities {
			if cap.Name == capability {
				granted = true
			}
		}
		if !granted {
			t.Fatalf("installed Core has no %s service", capability)
		}
		break
	}
	h.keys(terminal, scenario.Command+"\r")
	for {
		e := tap.next(t, h)
		if e.Error != nil {
			t.Fatalf("channel request refused: %s", e.Error.Code)
		}
		if e.Kind == protocol.KindItemDone {
			t.Fatalf("%s was admitted as conversation input instead of opening controls", scenario.Command)
		}
		if e.Kind == kind {
			if kind == protocol.KindAgentTalk {
				p := e.Payload.(protocol.AgentTalk)
				if p.Entries == nil || len(*p.Entries) != 1 {
					t.Fatal("durable talk summary missing")
				}
			}
			break
		}
	}
	if kind == protocol.KindAgentTalk {
		waitScreen(t, h, terminal, "Compare synthetic drafts")
		h.keys(terminal, "\r")
		for {
			e := tap.next(t, h)
			if e.Error != nil {
				t.Fatal(e.Error.Code)
			}
			if p, ok := e.Payload.(protocol.AgentTalk); ok {
				if p.ExchangeID == nil || p.Events == nil || len(*p.Events) != 1 || !strings.Contains((*p.Events)[0].Text, "Compare synthetic drafts") {
					t.Fatal("exchange expansion missing")
				}
				break
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("channel command reached the model")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.socket = coreSocket
	h.stopCore(core)
}

// The installed TUI talks to the actual boot composition. Only Telegram's HTTP
// transport is scripted; connection, pairing, binding and secret writes are real.
func TestM1TUIChannelPairing(t *testing.T) {
	var scenario struct{ Command, Token string }
	data, err := os.ReadFile("m1/tui-channels.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	if scenario.Command == "" || scenario.Token == "" {
		t.Fatal("missing channel scenario input")
	}

	h, _ := installedConversationHarness(t, "http://127.0.0.1:1")
	updates := make(chan string, 1)
	polls := make(chan struct{}, 8)
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		select {
		case polls <- struct{}{}:
		default:
		}
		select {
		case body := <-updates:
			_, _ = io.WriteString(w, body)
		case <-req.Context().Done():
		}
	}))
	t.Cleanup(platform.Close)
	socket := h.socket
	core := h.command("-test.run=^TestChannelBootHelper$")
	core.command.Path = os.Args[0]
	core.command.Args[0] = os.Args[0]
	core.command.Env = append(core.command.Env, "LINA_TEST_CHANNEL_ROOT="+h.root, "LINA_TEST_CHANNEL_SOCKET="+socket, "LINA_TEST_CHANNEL_HTTP="+platform.URL)
	core.start(t)
	h.connect(core).Close()
	waitCoreReady(t, h)
	tap := newAccountTap(t, h, socket)
	h.socket = tap.socket
	terminal := h.startScreenTUI("TERM=xterm-256color")
	waitComposerReady(t, h, terminal)
	next := func() protocol.ChannelResult {
		t.Helper()
		for {
			e := tap.next(t, h)
			if e.Error != nil {
				t.Fatalf("control refused: %s", e.Error.Code)
			}
			if e.Kind == protocol.KindItemDone {
				t.Fatal("channel control became conversation input")
			}
			if p, ok := e.Payload.(protocol.ChannelResult); ok {
				return p
			}
		}
	}
	h.keys(terminal, scenario.Command+"\r")
	next()
	waitScreen(t, h, terminal, "c Connect")
	h.keys(terminal, "c")
	waitScreen(t, h, terminal, "bot token")
	h.keys(terminal, scenario.Token+"\r")
	connected := next()
	if connected.Connections == nil || len(*connected.Connections) != 1 {
		t.Fatal("connection missing")
	}
	waitScreen(t, h, terminal, "123456")
	h.keys(terminal, "p")
	pair := next()
	if pair.PairingCode == nil {
		t.Fatal("no pairing code")
	}
	select {
	case <-polls:
	case <-h.ctx.Done():
		t.Fatal("poller not started")
	}
	updates <- `{"ok":true,"result":[{"update_id":1,"message":{"message_id":1,"from":{"id":42,"is_bot":false,"first_name":"Owner"},"chat":{"id":42,"type":"private"},"date":1,"text":"` + *pair.PairingCode + `"}}]}`
	select {
	case <-polls:
	case <-h.ctx.Done():
		t.Fatal("pairing not committed")
	}
	waitScreen(t, h, terminal, *pair.PairingCode)
	h.keys(terminal, "r")
	paired := next()
	if (*paired.Connections)[0].Status != "connected" {
		t.Fatal("not paired")
	}
	waitScreen(t, h, terminal, "Connected")
	h.keys(terminal, "b")
	waitScreen(t, h, terminal, "Chat:")
	h.keys(terminal, "\r")
	waitScreen(t, h, terminal, "Topic:")
	h.keys(terminal, "\r")
	waitScreen(t, h, terminal, "Enter bind")
	h.keys(terminal, "\r")
	bound := next()
	if len((*bound.Connections)[0].Bindings) != 1 || (*bound.Connections)[0].Bindings[0].Chat != "42" {
		t.Fatal("binding missing")
	}
	// Match the chat as a complete binding field: a random pairing code can
	// contain the same digits before the binding response has been rendered.
	if _, err := terminal.screen.Wait(h.ctx, func(snapshot tuiterm.Snapshot) bool {
		for _, line := range strings.Split(screenText(snapshot), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 1 && fields[0] == "42" {
				return true
			}
		}
		return false
	}); err != nil {
		t.Fatal("binding row was not rendered")
	}
	h.keys(terminal, "x")
	disconnected := next()
	if (*disconnected.Connections)[0].Status != "disconnected" || len((*disconnected.Connections)[0].Bindings) != 0 {
		t.Fatal("disconnect retained dispatch")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.socket = socket
	h.stopCore(core)
}

type pairingTransport func(*http.Request) (*http.Response, error)

func (f pairingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func seedTalkExchange(t *testing.T, h *harness) {
	t.Helper()
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	raw, err := ctl.Read("main.json")
	if err != nil {
		t.Fatal(err)
	}
	var main struct {
		ID string `json:"main_id"`
	}
	if err = json.Unmarshal(raw, &main); err != nil {
		t.Fatal(err)
	}
	epoch, err := ctl.MintEpoch(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon", "canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	store, err := agents.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	person := "person:" + main.ID
	first, err := store.FirstRun(h.ctx, person, true)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := store.Create(h.ctx, agents.Create{Name: "Peer", PersonID: person, Layers: agents.Layers{Core: "Compare supplied drafts"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_, err = exchange.New(db).Start(h.ctx, exchange.Request{DeliveryID: "tui-exchange", From: first.Agent.ID, To: peer.ID, HandOff: exchange.HandOff{Subject: "Compare synthetic drafts", Wanted: "List differences", OutOfScope: "No edits", Done: "All sections checked", Check: "sections", Deadline: now.Add(time.Hour)}}, now)
	if err != nil {
		t.Fatal(err)
	}
}

// Separate Core PID preserves the real peer-ancestry decision boundary. The TUI
// and tap are not descendants of Core and no admission check is bypassed.
func TestChannelBootHelper(t *testing.T) {
	root := os.Getenv("LINA_TEST_CHANNEL_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	route, err := modelpath.ResponsesFromEnv(func(key string) string {
		return map[string]string{"LINA_RESPONSES_BASE_URL": "http://127.0.0.1:1/v1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(os.Getenv("LINA_TEST_CHANNEL_HTTP"))
	if err != nil {
		t.Fatal(err)
	}
	transport := pairingTransport(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/getMe") {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":123456,"is_bot":true,"username":"fixture_bot"}}`))}, nil
		}
		if !strings.HasSuffix(req.URL.Path, "/getUpdates") {
			return nil, fmt.Errorf("unexpected method")
		}
		wire := req.Clone(req.Context())
		wire.URL.Scheme = endpoint.Scheme
		wire.URL.Host = endpoint.Host
		wire.URL.Path = "/updates"
		wire.URL.RawPath = ""
		return http.DefaultTransport.RoundTrip(wire)
	})
	ctx, cancel := signal.NotifyContext(t.Context(), syscall.SIGTERM)
	defer cancel()
	if err := boot.Run(ctx, boot.Config{StateRoot: root, Socket: os.Getenv("LINA_TEST_CHANNEL_SOCKET"), Conversation: route, ChannelTransport: transport}); err != nil {
		t.Fatal(err)
	}
}
