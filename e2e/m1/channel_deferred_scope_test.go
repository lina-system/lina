// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Run the installed Core with its real socket, gateway, lanes, memory and tools.
// Only model HTTP and Telegram transport are scripted. A failed app response
// leaves admitted steering undrained without racing the loop's final snapshot.
func TestM2DeferredChannelScope(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bundle := makeInstallBundle(t, t.TempDir(), executable, executable)
	provenance, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"restricted", "rebind-before-model", "rebind-before-tool"} {
		t.Run(mode, func(t *testing.T) {
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
			db, closeDB := installedAcceptanceStore(t, h)
			as, err := agents.Open(h.root, db)
			if err != nil {
				t.Fatal(err)
			}
			const person = "person:main-e2e"
			first, err := as.FirstRun(h.ctx, person, true)
			if err != nil {
				t.Fatal(err)
			}
			agent := first.Agent.ID
			store := channels.New(db)
			now := time.Now()
			code, err := store.PairingCode(h.ctx, acceptanceBot, person, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Pair(h.ctx, acceptanceBot, code, "100", "100", now); err != nil {
				t.Fatal(err)
			}
			scope := channels.PrivateScope()
			scope.Tools, scope.Memory = mode == "rebind-before-tool", false
			if err := store.Bind(h.ctx, channels.Binding{Address: acceptanceChat, Agent: agent, Person: person, Conversation: "main:" + agent, Scope: scope}); err != nil {
				t.Fatal(err)
			}
			const remembered = "SCOPE-506-REMEMBERED-ORCHARD"
			// A separate conversation keeps this marker out of shared main history.
			event, err := ledger.New(db, nil).Admit(h.ctx, guard.EntryOwner, ledger.Input{ID: "scope-memory-source", ConversationID: "memory-source", AgentID: agent, PersonID: person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: remembered})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := memoryexec.NewMemory(db, nil, nil).Remember(h.ctx, memory.Write{Person: person, Agent: agent, Item: memory.Item{Kind: memory.Fact, Content: remembered, Subject: memory.Subject{Kind: memory.Person, ID: person}, Basis: memory.Stated, Standing: true, Evidence: []memory.Evidence{{Kind: memory.Event, ID: event.ID, Precision: memory.Exact}}}}); err != nil {
				t.Fatal(err)
			}
			as.Close()
			closeDB()

			const appInput = "APP-START-506"
			const lateInput = "PRIVATE-LATE-506"
			const barrierInput = "PRIVATE-BARRIER-506"
			const restartInput = "APP-AFTER-RESTART-506"
			entered, release := make(chan struct{}), make(chan struct{})
			toolEntered, toolRelease := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Input, Tools []json.RawMessage }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				n := calls.Add(1)
				body := string(mustJSON(t, request.Input))
				if n == 1 || strings.Contains(body, restartInput) {
					if len(request.Tools) == 0 || !strings.Contains(body, remembered) {
						t.Error("app turn lost tools or personal memory")
					}
					if n > 1 {
						if !strings.Contains(body, appInput) || !strings.Contains(body, lateInput) {
							t.Error("restart lost shared main history")
						}
						emitResponse(w, "App history restored.", true)
						return
					}
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					http.Error(w, "fixture app failure", http.StatusInternalServerError)
					return
				}
				if mode == "rebind-before-model" {
					t.Error("changed channel binding reached the model")
					emitResponse(w, "Unexpected dispatch.", true)
					return
				}
				if strings.Contains(body, remembered) {
					t.Error("deferred channel turn recalled disabled personal memory")
				}
				if !strings.Contains(body, appInput) || !strings.Contains(body, lateInput) {
					t.Error("deferred channel turn lost shared main history")
				}
				if mode == "restricted" && len(request.Tools) != 0 {
					t.Error("deferred channel turn offered disabled tools")
				}
				if mode == "rebind-before-tool" {
					if n != 2 {
						t.Error("changed binding dispatched another model round")
					}
					if n == 2 {
						if len(request.Tools) == 0 {
							t.Error("channel tool was not offered before revocation")
						}
						close(toolEntered)
						select {
						case <-toolRelease:
						case <-r.Context().Done():
							return
						}
					}
				}
				if n == 2 {
					// An unsolicited call must not bypass the disabled catalog. The
					// rebind case instead tests revocation of a previously offered tool.
					emitPersonaCall(w, "scope-read", "skill_index", map[string]any{})
					return
				}
				emitResponse(w, "Channel scope preserved.", true)
			}))
			t.Cleanup(model.Close)
			updates := make(chan string, 8)
			platformDone := make(chan struct{})
			platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Drain POST bodies so net/http can observe the poller's disconnect.
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					return
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/getMe"):
					fmt.Fprint(w, `{"ok":true,"result":{"id":123456,"is_bot":true,"username":"fixture_bot"}}`)
				case strings.HasSuffix(r.URL.Path, "/getUpdates"):
					select {
					case body := <-updates:
						fmt.Fprint(w, body)
					case <-platformDone:
					case <-r.Context().Done():
					}
				case strings.HasSuffix(r.URL.Path, "/sendMessage"):
					fmt.Fprint(w, `{"ok":true,"result":{"message_id":200}}`)
				default:
					t.Errorf("unexpected Telegram method: %s", r.URL.Path)
				}
			}))
			t.Cleanup(func() { close(platformDone); platform.Close() })
			var core *process
			start := func() func() {
				p := h.command("-test.run=^TestDeferredScopeBootHelper$")
				p.command.Path, p.command.Args[0] = executable, executable
				p.command.Env = append(p.command.Env, "LINA_TEST_SCOPE_ROOT="+h.root, "LINA_TEST_SCOPE_SOCKET="+h.socket, "LINA_TEST_SCOPE_PROVIDER="+model.URL, "LINA_TEST_SCOPE_HTTP="+platform.URL)
				p.start(t)
				core = p
				h.connect(p).Close()
				waitCoreReady(t, h)
				return func() { h.stopCore(p) }
			}
			stop := start()
			t.Cleanup(func() {
				if stop != nil {
					stop()
				}
			})
			connect := func() *client {
				conn := h.connect(core)
				c := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
				hello := clientHello([]string{"1.0"})
				hello.Capabilities = []protocol.Capability{{Name: "channels", Version: "1.0"}, {Name: "conversation", Version: "1.0"}}
				answer := c.call(t, protocol.KindHello, hello)
				if answer.Error != nil {
					t.Fatal(answer.Error)
				}
				c.welcome = answer.Payload.(protocol.Welcome)
				return c
			}
			c := connect()
			reader, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close(context.Background())
			waitCount := func(query string, want int) {
				t.Helper()
				for {
					var count int
					if err := reader.Read(h.ctx, func(tx *canon.Tx) error { return tx.QueryRowContext(h.ctx, query).Scan(&count) }); err != nil {
						t.Fatal(err)
					}
					if count == want {
						return
					}
					select {
					case <-h.ctx.Done():
						t.Fatalf("count=%d want=%d query=%s", count, want, query)
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			send := func(id int, text string, sender int) {
				updates <- string(mustJSON(t, map[string]any{"ok": true, "result": []any{map[string]any{"update_id": id, "message": map[string]any{"message_id": id + 100, "date": 1, "chat": map[string]any{"id": 100, "type": "private"}, "from": map[string]any{"id": sender}, "text": text}}}}))
			}
			str := func(s string) *string { return &s }
			app := func(text string) {
				t.Helper()
				answer := c.call(t, protocol.KindInput, protocol.Input{ConversationID: str("main:" + agent), InputID: &text, Text: &text})
				if answer.Error != nil {
					t.Fatal(answer.Error)
				}
			}
			rebind := func() {
				t.Helper()
				answer := c.call(t, protocol.KindChannelRequest, protocol.ChannelRequest{Operation: str("bind"), Platform: str("telegram"), Account: str(acceptanceBot.Account), AgentID: &agent, Scope: &protocol.ChannelScope{Reads: "dm_only", Writes: "may_start"}})
				if answer.Error != nil {
					t.Fatal(answer.Error)
				}
			}
			app(appInput)
			select {
			case <-entered:
			case <-h.ctx.Done():
				t.Fatal("app model request did not start")
			}
			send(10, lateInput, 100)
			send(11, barrierInput, 100)
			// The channel worker is serial: this later ledger event proves the
			// first input reached the active steering queue before app failure.
			waitCount(`SELECT count(*) FROM conv_event WHERE json_extract(origin,'$.locator')='11'`, 1)
			if mode == "rebind-before-model" {
				rebind()
			}
			close(release)
			if mode == "rebind-before-tool" {
				select {
				case <-toolEntered:
				case <-h.ctx.Done():
					t.Fatal("channel model request did not start")
				}
				rebind()
				close(toolRelease)
			}
			waitCount(`SELECT count(*) FROM turn_runs WHERE state!='active'`, 3)
			waitCount(`SELECT count(*) FROM channel_steering`, 0)
			if mode != "rebind-before-model" {
				var result []byte
				if err := reader.Read(h.ctx, func(tx *canon.Tx) error {
					return tx.QueryRowContext(h.ctx, "SELECT result FROM turn_calls WHERE call_id='scope-read'").Scan(&result)
				}); err != nil {
					t.Fatal(err)
				}
				var outcome struct{ Code string }
				wantCode := "denied"
				if mode == "restricted" {
					wantCode = "tool_unavailable"
				}
				if err := json.Unmarshal(result, &outcome); err != nil || outcome.Code != wantCode {
					t.Errorf("restricted tool code=%s want=%s err=%v", outcome.Code, wantCode, err)
				}
			}
			wantCalls := int32(4)
			if mode == "rebind-before-model" {
				wantCalls = 1
			}
			if mode == "rebind-before-tool" {
				wantCalls = 2
			}
			if calls.Load() != wantCalls {
				t.Errorf("model calls=%d want=%d", calls.Load(), wantCalls)
			}
			beforeRestart := calls.Load()
			c.conn.Close()
			stop()
			stop = nil
			stop = start()
			c = connect()
			// Native receipts and the claimed follow-ups must survive restart.
			send(10, lateInput, 100)
			send(11, barrierInput, 100)
			// A refused later native update proves both duplicate updates passed
			// through the restarted adapter before checking for repeated work.
			send(12, "restart receipt barrier", 999)
			waitCount(`SELECT count(*) FROM channel_receipts WHERE native_id='12'`, 1)
			app(restartInput)
			waitCount(`SELECT count(*) FROM turn_runs WHERE state!='active'`, 4)
			c.conn.Close()
			stop()
			stop = nil
			if calls.Load() != beforeRestart+1 {
				t.Errorf("restart replayed channel work: calls=%d want=%d", calls.Load(), beforeRestart+1)
			}
		})
	}
}

// The parent is the socket's authenticated decider; Core runs in this child.
func TestDeferredScopeBootHelper(t *testing.T) {
	root := os.Getenv("LINA_TEST_SCOPE_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	route, err := modelpath.ResponsesFromEnv(func(k string) string {
		return map[string]string{"LINA_RESPONSES_BASE_URL": os.Getenv("LINA_TEST_SCOPE_PROVIDER") + "/v1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(os.Getenv("LINA_TEST_SCOPE_HTTP"))
	if err != nil {
		t.Fatal(err)
	}
	transport := pairingTransport(func(req *http.Request) (*http.Response, error) {
		wire := req.Clone(req.Context())
		wire.URL.Scheme, wire.URL.Host = endpoint.Scheme, endpoint.Host
		return http.DefaultTransport.RoundTrip(wire)
	})
	ctx, cancel := signal.NotifyContext(t.Context(), syscall.SIGTERM)
	defer cancel()
	if err := boot.Run(ctx, boot.Config{StateRoot: root, Socket: os.Getenv("LINA_TEST_SCOPE_SOCKET"), Conversation: route, ChannelTransport: transport}); err != nil {
		t.Fatal(err)
	}
}
