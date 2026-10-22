// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/protocol"
)

const acceptanceBotToken = "123456:synthetic_acceptance_token"

type unavailableTelegram struct {
	entered  chan string
	returned chan struct{}
}

func (u unavailableTelegram) RoundTrip(r *http.Request) (*http.Response, error) {
	select {
	case u.entered <- path.Base(r.URL.Path):
	default:
	}
	// A real request has entered the transport. Hold it until shutdown, making
	// successful startup independent of both platform reachability and timeout.
	<-r.Context().Done()
	u.returned <- struct{}{}
	return nil, r.Context().Err()
}

func TestM1TelegramUnreachable(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("telegram.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Platform, Account string
		CoreState         string             `json:"core_state"`
		ChannelState      string             `json:"channel_state"`
		RequestResult     protocol.ErrorCode `json:"request_result"`
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, "")
	control := filepath.Join(h.root, "system/core")
	if err := os.MkdirAll(control, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(control, "main.json"), []byte(`{"main_id":"main-e2e"}`), 0600); err != nil {
		t.Fatal(err)
	}
	seedTelegramConnection(t, h, channels.Bot{Platform: scenario.Platform, Account: scenario.Account})
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	transport := unavailableTelegram{entered: make(chan string, 1), returned: make(chan struct{}, 1)}
	ready := make(chan boot.Snapshot, 1)
	core := &process{done: make(chan struct{})}
	// Only the platform transport is replaced. Core owns canon, the secret
	// store, channel recovery, the supervisor and shutdown in this scenario.
	cfg := boot.Config{StateRoot: h.root, Socket: h.socket, ChannelTransport: transport}
	cfg.AfterStep = func(step boot.Step, s boot.Snapshot) error {
		if step == boot.Ready {
			ready <- s
		}
		return nil
	}
	go func() { core.err = boot.Run(ctx, cfg); close(core.done) }()
	t.Cleanup(func() { cancel(); <-core.done })
	select {
	case method := <-transport.entered:
		if method != "getMe" {
			t.Fatalf("expected boot identity lookup, got %s", method)
		}
	case <-core.done:
		t.Fatalf("Core exited before starting its saved channel: %v", core.err)
	case <-h.ctx.Done():
		t.Fatal("poller did not enter transport")
	}
	select {
	case state := <-ready:
		if string(state.State) != scenario.CoreState {
			t.Fatalf("state=%s", state.State)
		}
	case <-core.done:
		t.Fatalf("Core exited before READY: %v", core.err)
	case <-h.ctx.Done():
		t.Fatal("unreachable platform blocked READY")
	}
	// The production boot composition must restore the saved channel and expose
	// its unavailable state through the socket while Telegram remains blocked.
	conn := h.connect(core)
	c := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
	hello := clientHello([]string{"1.0"})
	hello.Capabilities = []protocol.Capability{{Name: "channels", Version: "1.0"}}
	handshake := c.call(t, protocol.KindHello, hello)
	welcome, ok := handshake.Payload.(protocol.Welcome)
	if !ok || handshake.Kind != protocol.KindWelcome || handshake.Error != nil || len(welcome.Capabilities) != 1 || welcome.Capabilities[0] != hello.Capabilities[0] {
		t.Fatal("READY Core did not expose the production channel service")
	}
	if welcome.MainID != "main-e2e" || welcome.Epoch != handshake.Epoch || welcome.Epoch == "0" {
		t.Fatal("channel handshake lost Core identity or epoch")
	}
	c.welcome = welcome
	operation := "list"
	listed := c.call(t, protocol.KindChannelRequest, protocol.ChannelRequest{Operation: &operation})
	result, ok := listed.Payload.(protocol.ChannelResult)
	if !ok || listed.Kind != protocol.KindChannelResult || listed.Error != nil || result.Connections == nil || len(*result.Connections) != 1 {
		t.Fatal("saved channel was not restored through production boot")
	}
	connection := (*result.Connections)[0]
	if connection.Platform != scenario.Platform || connection.Account != scenario.Account || connection.Status != scenario.ChannelState {
		t.Fatalf("unexpected channel state: %+v", connection)
	}
	encoded, err := json.Marshal(listed)
	if err != nil || strings.Contains(string(encoded), acceptanceBotToken) {
		t.Fatal("channel response exposed its credential")
	}
	answer := c.call(t, protocol.KindInput, protocol.Input{})
	if answer.Error == nil || answer.Error.Code != scenario.RequestResult || answer.EffectState != protocol.EffectRefused {
		t.Fatalf("READY response: %+v", answer)
	}
	select {
	case <-transport.returned:
		t.Fatal("READY or channel controls waited for the platform request to return")
	default:
	}
	cancel()
	select {
	case <-core.done:
		if core.err != nil {
			t.Fatal(core.err)
		}
	case <-h.ctx.Done():
		t.Fatal("Core did not stop")
	}
	select {
	case <-transport.returned:
	default:
		t.Fatal("Core shutdown did not cancel and join the platform request")
	}
}

func seedTelegramConnection(t *testing.T, h *harness, bot channels.Bot) {
	t.Helper()
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	epoch, err := ctl.MintEpoch(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.root, "canon"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon", "canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	person := "person:main-e2e"
	if err := channels.New(db).Connect(h.ctx, bot, person); err != nil {
		t.Fatal(err)
	}
	vault, err := secrets.OpenFiles(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	if err := vault.Put(secrets.Key{Person: person, Service: bot.Platform, Name: bot.Account}, []byte(acceptanceBotToken)); err != nil {
		t.Fatal(err)
	}
}
