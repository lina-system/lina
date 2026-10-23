// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/modelpath"
)

// Real Core admission, provider tool execution, result storage and Telegram
// outbox delivery. Only HTTP peers are scripted; no platform account is used.
func TestTypedResultReachesTelegramAcrossRestart(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bundle := makeInstallBundle(t, t.TempDir(), executable, executable)
	provenance, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, "")
	for name, body := range map[string][]byte{
		"system/bin/manifest.json": provenance,
		"system/core/main.json":    []byte(`{"main_id":"main-e2e"}`),
	} {
		path := filepath.Join(h.root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	seedTelegramConnection(t, h, acceptanceBot)
	const person = "person:main-e2e"
	var agent string
	setMethod := func(comparison string) {
		t.Helper()
		db, closeDB := installedAcceptanceStore(t, h)
		defer closeDB()
		as, err := agents.Open(h.root, db)
		if err != nil {
			t.Fatal(err)
		}
		defer as.Close()
		if _, err := as.FirstRun(h.ctx, person, true); err != nil {
			t.Fatal(err)
		}
		method := fmt.Sprintf(`{"schema":1,"reply":{"comparison":%q}}`, comparison)
		if err := as.UpdatePreset(h.ctx, agents.Preset{ID: "reply-channel-fixture", Core: "Synthetic agent", DefaultVoice: "Plain", WorkMethod: method}, person); err != nil {
			t.Fatal(err)
		}
		if agent != "" {
			return
		}
		a, err := as.Create(h.ctx, agents.Create{Name: "Fixture", PersonID: person, Preset: "reply-channel-fixture"})
		if err != nil {
			t.Fatal(err)
		}
		agent = a.ID
		store := channels.New(db)
		now := time.Now()
		code, err := store.PairingCode(h.ctx, acceptanceBot, person, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Pair(h.ctx, acceptanceBot, code, "100", "100", now); err != nil {
			t.Fatal(err)
		}
		if err := store.Bind(h.ctx, channels.Binding{Address: acceptanceChat, Agent: agent, Person: person, Conversation: "main:" + agent, Scope: channels.PrivateScope()}); err != nil {
			t.Fatal(err)
		}
	}
	setMethod("verdict-then-card")
	var calls, sends atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n%2 == 1 {
			emitPersonaCall(w, fmt.Sprintf("channel-result-%d", n), "present_result", map[string]any{
				"type": "table", "title": "Options", "summary": "Choose A.",
				"body": json.RawMessage(`{"columns":[{"name":"Choice","type":"text","unit":""}],"rows":[["A"],["B"]]}`),
			})
		} else {
			emitResponse(w, "Recorded.", true)
		}
	}))
	defer model.Close()
	route, err := modelpath.ResponsesFromEnv(func(k string) string {
		return map[string]string{"LINA_RESPONSES_BASE_URL": model.URL + "/v1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	updates, delivered := make(chan string, 2), make(chan string, 8)
	transport := pairingTransport(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			return telegramWire(`{"ok":true,"result":{"id":123456,"is_bot":true,"username":"fixture_bot"}}`), nil
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			select {
			case body := <-updates:
				return telegramWire(body), nil
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			if err := r.ParseForm(); err != nil {
				return nil, err
			}
			if r.FormValue("chat_id") != "100" {
				t.Errorf("wrong reply destination: %s", r.FormValue("chat_id"))
			}
			delivered <- r.FormValue("text")
			return telegramWire(fmt.Sprintf(`{"ok":true,"result":{"message_id":%d}}`, 200+sends.Add(1))), nil
		default:
			return nil, fmt.Errorf("unexpected Telegram method: %s", r.URL.Path)
		}
	})
	start := func() func() {
		ctx, cancel := context.WithCancel(h.ctx)
		done := make(chan error, 1)
		go func() {
			done <- boot.Run(ctx, boot.Config{StateRoot: h.root, Socket: h.socket, Conversation: route, ChannelTransport: transport})
		}()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(operationTimeout):
					t.Error("Core shutdown timed out")
				}
			})
		}
		t.Cleanup(stop)
		return stop
	}
	waitOutbox := func(want int) {
		t.Helper()
		db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
		defer cancel()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			var count int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM channel_outbound WHERE state='sent'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count == want {
				return
			}
			if count > want {
				t.Fatalf("duplicate outbox messages: %d", count)
			}
			select {
			case <-ctx.Done():
				t.Fatalf("outbox sent=%d want=%d", count, want)
			case <-tick.C:
			}
		}
	}
	for phase := 0; phase < 2; phase++ {
		if phase == 1 {
			setMethod("verdict-then-table")
		}
		stop := start()
		updates <- fmt.Sprintf(`{"ok":true,"result":[{"update_id":%d,"message":{"message_id":%d,"date":1,"chat":{"id":100,"type":"private"},"from":{"id":100},"text":"Compare the options."}}]}`, 10+phase, 110+phase)
		found := false
		for range 2 {
			select {
			case text := <-delivered:
				if strings.Contains(text, "Choose A.") {
					found = true
					if phase == 0 && (!strings.Contains(text, "Options") || !strings.Contains(text, "lina://conversation/main:"+agent)) {
						t.Errorf("missing card caption/link: %q", text)
					}
					if phase == 1 && (!strings.Contains(text, "Choice") || !strings.Contains(text, "B") || strings.Contains(text, "lina://")) {
						t.Errorf("missing short inline table: %q", text)
					}
				}
			case <-h.ctx.Done():
				t.Fatal("typed result was not delivered", h.ctx.Err())
			}
		}
		if !found {
			t.Fatal("channel received only acknowledgements")
		}
		waitOutbox((phase + 1) * 2)
		stop()
	}
	if calls.Load() != 4 || sends.Load() != 4 {
		t.Fatalf("restart repeated work/delivery: calls=%d sends=%d", calls.Load(), sends.Load())
	}
	select {
	case extra := <-delivered:
		t.Fatalf("duplicate delivery: %q", extra)
	default:
	}
}
