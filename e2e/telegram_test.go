// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/gateway/telegram"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/secret"
)

type unavailableTelegram struct{ entered chan struct{} }

func (u unavailableTelegram) RoundTrip(r *http.Request) (*http.Response, error) {
	select {
	case u.entered <- struct{}{}:
	default:
	}
	// A real request has entered the transport. Hold it until shutdown, making
	// successful startup independent of both platform reachability and timeout.
	<-r.Context().Done()
	return nil, r.Context().Err()
}

func TestM1TelegramUnreachable(t *testing.T) {
	data, err := os.ReadFile("m1/telegram.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Platform, Account string
		CoreState         string             `json:"core_state"`
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
	// The fixture supplies the existing boot composition seam with an actual
	// canon store. Its private control root is isolated from the lifecycle root
	// whose lock boot owns; no test introduces a second writer to either store.
	storeRoot := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(storeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	var db *canon.Store
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	entered := make(chan struct{}, 1)
	adapterDone := make(chan error, 1)
	ready := make(chan boot.Snapshot, 1)
	core := &process{done: make(chan struct{})}
	cfg := boot.Config{StateRoot: h.root, Socket: h.socket}
	cfg.Services.PeekEpoch = func(context.Context) (uint64, error) { return 0, nil }
	cfg.Services.OpenCanon = func(ctx context.Context, epoch uint64) (boot.Canon, error) {
		ms := canon.Migrations()
		var err error
		db, err = canon.Open(ctx, canon.Config{Path: filepath.Join(storeRoot, "canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: ms})
		return db, err
	}
	cfg.Services.Reconcile = func(ctx context.Context, recovery boot.Recovery) error {
		bot := channels.Bot{Platform: scenario.Platform, Account: scenario.Account}
		if err := channels.New(db).Connect(ctx, bot, "person"); err != nil {
			return err
		}
		a, err := telegram.New(telegram.Config{Bot: bot, Token: secret.New("123456:synthetic_acceptance_token"), DB: db, Epoch: recovery.Epoch, Transport: unavailableTelegram{entered: entered}})
		if err != nil {
			return err
		}
		g := gateway.New(db, func(context.Context, gateway.Delivery) error {
			return errors.New("unreachable platform produced input")
		})
		go func() { adapterDone <- a.Run(ctx, g) }()
		return nil
	}
	cfg.AfterStep = func(step boot.Step, s boot.Snapshot) error {
		if step == boot.Ready {
			ready <- s
		}
		return nil
	}
	go func() { core.err = boot.Run(ctx, cfg); close(core.done) }()
	t.Cleanup(func() { cancel(); <-core.done })
	select {
	case <-entered:
	case <-h.ctx.Done():
		t.Fatal("poller did not enter transport")
	}
	select {
	case state := <-ready:
		if string(state.State) != scenario.CoreState {
			t.Fatalf("state=%s", state.State)
		}
	case <-h.ctx.Done():
		t.Fatal("unreachable platform blocked READY")
	}
	// Use #34's socket harness and generated protocol envelopes against real
	// boot/client service. This increment has no public channel settings method.
	c := h.client(core)
	answer := c.call(t, protocol.KindInput, protocol.Input{})
	if answer.Error == nil || answer.Error.Code != scenario.RequestResult || answer.EffectState != protocol.EffectRefused {
		t.Fatalf("READY response: %+v", answer)
	}
	cancel()
	select {
	case err := <-adapterDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("poller shutdown: %v", err)
		}
	case <-h.ctx.Done():
		t.Fatal("poller did not stop")
	}
	select {
	case <-core.done:
		if core.err != nil {
			t.Fatal(core.err)
		}
	case <-h.ctx.Done():
		t.Fatal("Core did not stop")
	}
}
