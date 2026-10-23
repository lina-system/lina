// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

// TestM2ProgressDigestWorker runs the worker journey in
// m2/progress-controls.json, observing its approval, question and real artifact
// through the Core digest. Only external provider and platform wires are
// scripted.
//
// The same Core digest is read while an actual stdio worker waits for approval,
// asks a question and writes its result. Only model/worker service wires are scripted.
func TestM2ProgressDigestWorker(t *testing.T) {
	t.Parallel()
	runCodexWorkTools(t, "../m2/progress-controls.json", "", func(h *harness, phase string, _ []protocol.Question) {
		c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "digest", []protocol.Capability{{Name: "progress", Version: "1.0"}})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if _, err = c.Control(h.ctx, protocol.ProgressDigestGet{}); err != nil {
			t.Fatal(err)
		}
		e, err := c.Receive(h.ctx)
		if err != nil || e.Error != nil {
			t.Fatalf("digest: %v %+v", err, e.Error)
		}
		d, ok := e.Payload.(protocol.ProgressDigest)
		if !ok {
			t.Fatalf("not a digest: %T", e.Payload)
		}
		if len(d.Lines) > 5 || d.Link == "" {
			t.Fatalf("unbounded digest: %+v", d)
		}
		if phase == "approval" || phase == "question" {
			if len(d.Lines) < 2 {
				t.Fatalf("waiting and running missing: %+v", d)
			}
		}
		for _, line := range d.Lines {
			if strings.ContainsAny(line, "\n\r") || strings.Contains(line, "work-tools-thread") || strings.Contains(line, "%") {
				t.Fatalf("internal data in digest: %q", line)
			}
		}
	})
}

// TestM2ProgressControlsSurfaces runs m2/progress-controls.json through the
// built TUI and paired Telegram gateway. They send explicit controls to running
// Core and inspect the durable target runs. Only external provider and platform
// wires are scripted.
//
// Exercise the built TUI and the paired Telegram adapter against the same Core
// handlers and durable run store. Queued runs make scope assertions deterministic.
func TestM2ProgressControlsSurfaces(t *testing.T) {
	for _, surface := range []string{"tui", "channel"} {
		t.Run(surface, func(t *testing.T) { testProgressControlsSurfaces(t, surface) })
	}
}
func testProgressControlsSurfaces(t *testing.T, allSurface string) {
	var fixture struct{ Digest, Order, Note, Refused string }
	raw, err := os.ReadFile("../m2/progress-controls.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	h, start := installedConversationHarness(t, "http://127.0.0.1:1")
	initial := start(true)
	h.stopCore(initial)
	db, closeDB := installedAcceptanceStore(t, h)
	raw, err = os.ReadFile(filepath.Join(h.root, "system/core/main.json"))
	if err != nil {
		t.Fatal(err)
	}
	var main struct {
		ID string `json:"main_id"`
	}
	if err = json.Unmarshal(raw, &main); err != nil {
		t.Fatal(err)
	}
	person := "person:" + main.ID
	as, err := agents.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	first, err := as.FirstRun(h.ctx, person, true)
	if err != nil {
		t.Fatal(err)
	}
	agent := first.Agent.ID
	conversation := "main:" + agent
	store := channels.New(db)
	if err = store.Connect(h.ctx, acceptanceBot, person); err != nil {
		t.Fatal(err)
	}
	vault, err := secrets.OpenFiles(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if err = vault.Put(secrets.Key{Person: person, Service: acceptanceBot.Platform, Name: acceptanceBot.Account}, []byte(acceptanceBotToken)); err != nil {
		t.Fatal(err)
	}
	vault.Close()
	now := time.Now()
	code, err := store.PairingCode(h.ctx, acceptanceBot, person, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Pair(h.ctx, acceptanceBot, code, "100", "100", now); err != nil {
		t.Fatal(err)
	}
	if err = store.Bind(h.ctx, channels.Binding{Address: acceptanceChat, Agent: agent, Person: person, Conversation: conversation, Scope: channels.PrivateScope()}); err != nil {
		t.Fatal(err)
	}
	materials, err := assets.Open(h.ctx, h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := runs.New(runs.Config{DB: db, Assets: materials, WorkDir: filepath.Join(h.root, "work"), Epoch: 1, Authorize: func(context.Context, runs.Origin, runs.Brief) error { return nil }, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
		return nil, fmt.Errorf("queued fixture must not call a model")
	}})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i := range 5 {
		conv := conversation
		runPerson := person
		if i == 4 {
			runPerson = "foreign-owner"
		}
		if i == 3 {
			conv = "another-owner-conversation"
		}
		origin := runs.Origin{Person: runPerson, Agent: agent, Conversation: conv, Turn: fmt.Sprint("fixture-", i)}
		brief := runs.Brief{Goal: "Inspect appendix", Scope: "reads", NotTouch: "writes", Done: "report", Result: "summary", Checks: []runs.Check{{Name: "report", Kind: runs.Claimed}}}
		raw, _ := json.Marshal(brief)
		result := service.StartTool(origin).Execute(h.ctx, provider.ToolCall{ID: fmt.Sprint("fixture-call-", i), Name: runs.StartToolName, Arguments: string(raw)})
		var receipt struct{ ID string }
		if err := json.Unmarshal(result.Output, &receipt); err != nil || receipt.ID == "" {
			t.Fatalf("seed start receipt: %+v %v", result, err)
		}
		id := receipt.ID
		ids = append(ids, id)
	}
	service.Close()
	materials.Close()
	as.Close()
	closeDB()
	updates := make(chan string, 12)
	sent := make(chan string, 24)
	platformDone := make(chan struct{})
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/getUpdates"):
			select {
			case body := <-updates:
				fmt.Fprint(w, body)
			case <-req.Context().Done():
			case <-h.ctx.Done():
			case <-platformDone:
			}
		case strings.HasSuffix(req.URL.Path, "/sendMessage"):
			if err := req.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			if req.MultipartForm != nil {
				defer req.MultipartForm.RemoveAll()
			}
			text := req.FormValue("text")
			select {
			case sent <- text:
			case <-h.ctx.Done():
			case <-platformDone:
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":900}}`)
		default:
			t.Errorf("unexpected method: %s", req.URL.Path)
			http.Error(w, "unexpected", 400)
		}
	}))
	defer func() { close(platformDone); platform.Close() }()
	socket := h.socket
	core := h.command("-test.run=^TestProgressControlBootHelper$")
	core.command.Path = os.Args[0]
	core.command.Args[0] = os.Args[0]
	core.command.Env = append(core.command.Env, "LINA_TEST_PROGRESS_ROOT="+h.root, "LINA_TEST_PROGRESS_SOCKET="+socket, "LINA_TEST_PROGRESS_HTTP="+platform.URL)
	core.start(t)
	h.connect(core).Close()
	waitCoreReady(t, h)
	tap := newAccountTap(t, h, socket)
	h.socket = tap.socket
	terminal := h.startScreenTUI("TERM=xterm-256color")
	waitComposerReady(t, h, terminal)
	tui := func(command string, kind protocol.Kind) protocol.Envelope {
		t.Helper()
		t.Logf("TUI command: %s", command)
		h.keys(terminal, command+"\r")
		for {
			e := tap.next(t, h)
			if tap.requestKind(e) != kind {
				continue
			}
			if e.Error != nil {
				t.Fatalf("%s: %s; core=%s", command, e.Error.Code, &core.stderr)
			}

			expected := ""
			switch p := e.Payload.(type) {
			case protocol.ProgressDigest:
				expected = fmt.Sprintf("%s items", p.Total)
			case protocol.Waiting:
				expected = "nothing to decide now"
			case protocol.FollowUp:
				expected = "Note queued."
			case protocol.StopResult:
				expected = "Stop requested."
			case protocol.MultiTaskControl:
				expected = fmt.Sprintf("Stop requested for %s runs.", *p.Count)
			}
			if expected != "" {
				waitScreen(t, h, terminal, expected)
			}
			return e
		}
	}
	tui(fixture.Digest, protocol.KindProgressDigestGet)
	h.keys(terminal, "q")
	tui(fixture.Order, protocol.KindWaitingGet)
	h.keys(terminal, "q")
	tui("/note "+ids[0]+" "+fixture.Note, protocol.KindFollowUp)
	h.keys(terminal, "q")
	tui("/stop "+ids[0], protocol.KindStop)
	h.keys(terminal, "q")
	// Subscribe before channel commands: history reload must not be required
	// to see their committed inputs and responses on another surface.
	observer, err := clientapi.DialWithCapabilities(h.ctx, socket, "channel-controls", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	topic := "conversation:" + conversation
	if _, err = observer.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	if e, err := observer.Receive(h.ctx); err != nil || e.Kind != protocol.KindSnapshot {
		t.Fatalf("conversation subscription: %s %v", e.Kind, err)
	}
	var command string
	var liveKeys []string
	send := func(id int, text string) {
		t.Helper()
		command = text
		message := map[string]any{"message_id": id, "date": 1, "chat": map[string]any{"id": 100, "type": "private"}, "from": map[string]any{"id": 100}, "text": text}
		updates <- string(mustJSON(t, map[string]any{"ok": true, "result": []any{map[string]any{"update_id": id, "message": message}}}))
	}
	reply := func(want string) {
		t.Helper()
		select {
		case text := <-sent:
			if !strings.Contains(text, want) {
				t.Fatalf("channel reply does not match %q: %q", want, text)
			}
			ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
			defer cancel()
			for i, expected := range []string{command, text} {
				for {
					e, err := observer.Receive(ctx)
					if err != nil {
						t.Fatalf("channel command/reply missing from live conversation: %v", err)
					}
					item, ok := e.Payload.(protocol.ItemDone)
					if !ok {
						continue
					}
					speaker := protocol.ConversationSpeaker("person")
					if i == 1 {
						speaker = "agent"
					}
					if item.Text == nil || *item.Text != expected || item.Speaker == nil || *item.Speaker != speaker || item.AgentID == nil || *item.AgentID != agent || item.Stream == nil || item.Stream.Topic != topic {
						t.Fatalf("live command/reply differs from channel: %+v", item)
					}
					if i == 0 && (item.PersonID == nil || *item.PersonID != person) || i == 1 && item.PersonID != nil {
						t.Fatalf("live command/reply attribution: %+v", item)
					}
					liveKeys = append(liveKeys, item.Stream.Key)
					break
				}
			}
		case <-h.ctx.Done():
			t.Fatal("channel control did not reply")
		}
	}
	send(10, fixture.Digest)
	reply("items")
	send(11, fixture.Order)
	reply("Nothing to decide")
	send(12, "/note "+ids[1]+" "+fixture.Note)
	reply("Note queued")
	send(13, "/stop "+ids[1])
	reply("Stop requested")
	send(15, fixture.Refused)
	reply("could not be applied")
	reader, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	read := func(id string) runs.Record {
		t.Helper()
		var v runs.Record
		err := reader.Read(h.ctx, func(tx *canon.Tx) error {
			var body []byte
			if err := tx.QueryRowContext(h.ctx, "SELECT record FROM background_runs WHERE id=?", id).Scan(&body); err != nil {
				return err
			}
			return json.Unmarshal(body, &v)
		})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for i, id := range ids[:2] {
		v := read(id)
		if len(v.FollowSources) != i {
			t.Fatalf("channel source provenance missing: %d", len(v.FollowSources))
		}
		if v.Outcome != runs.Cancelled || len(v.FollowUps) != 1 || v.FollowUps[0] != fixture.Note {
			t.Fatalf("surface effect differs: %+v", v)
		}
	}
	if allSurface == "channel" {
		send(16, "/stop all")
		reply("Stop requested for 2 runs.")
	} else {
		tui("/stop all", protocol.KindMultiTaskControl)
		h.keys(terminal, "q")
	}
	for _, id := range ids[:4] {
		if v := read(id); v.Outcome != runs.Cancelled {
			t.Fatalf("stop all missed run: %+v", v)
		}
	}
	if v := read(ids[4]); v.State == runs.Landed {
		t.Fatal("channel stop all escaped owner scope")
	}
	tui("/stop all", protocol.KindMultiTaskControl)
	h.keys(terminal, "q")
	if v := read(ids[4]); v.State == runs.Landed {
		t.Fatal("TUI stop all escaped owner scope")
	}
	reader.Close(context.Background())
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.socket = socket
	h.stopCore(core)
	observer.Close()
	// Restart uses the journal's persisted first-appearance order. It must
	// retain exactly the channel rows seen live, with no duplicate controls.
	core = h.command("-test.run=^TestProgressControlBootHelper$")
	core.command.Path = os.Args[0]
	core.command.Args[0] = os.Args[0]
	core.command.Env = append(core.command.Env, "LINA_TEST_PROGRESS_ROOT="+h.root, "LINA_TEST_PROGRESS_SOCKET="+socket, "LINA_TEST_PROGRESS_HTTP="+platform.URL)
	core.start(t)
	h.connect(core).Close()
	waitCoreReady(t, h)
	restored, err := clientapi.DialWithCapabilities(h.ctx, socket, "channel-controls-restored", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err = restored.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	e, err := restored.Receive(h.ctx)
	snapshot, ok := e.Payload.(protocol.Snapshot)
	if err != nil || !ok {
		t.Fatalf("restored conversation: %s %v", e.Kind, err)
	}
	var restoredKeys []string
	for _, item := range snapshot.Items {
		if strings.HasPrefix(item.Key, "channel-input:") || strings.HasPrefix(item.Key, "channel-control:") {
			restoredKeys = append(restoredKeys, item.Key)
		}
	}
	if !reflect.DeepEqual(liveKeys, restoredKeys) {
		t.Fatalf("restart changed live channel order: live=%v restored=%v", liveKeys, restoredKeys)
	}
	h.stopCore(core)
}

func TestProgressControlBootHelper(t *testing.T) {
	root := os.Getenv("LINA_TEST_PROGRESS_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	route, err := modelpath.ResponsesFromEnv(func(k string) string {
		return map[string]string{"LINA_RESPONSES_BASE_URL": "http://127.0.0.1:1/v1", "LINA_RESPONSES_MODEL": "fixture-model", "LINA_RESPONSES_API_KEY": "synthetic-key"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(os.Getenv("LINA_TEST_PROGRESS_HTTP"))
	if err != nil {
		t.Fatal(err)
	}
	transport := pairingTransport(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/getMe") {
			return telegramWire(`{"ok":true,"result":{"id":123456,"is_bot":true,"username":"fixture_bot"}}`), nil
		}
		wire := req.Clone(req.Context())
		wire.URL.Scheme = endpoint.Scheme
		wire.URL.Host = endpoint.Host
		return http.DefaultTransport.RoundTrip(wire)
	})
	ctx, cancel := signal.NotifyContext(t.Context(), syscall.SIGTERM)
	defer cancel()
	if err := boot.Run(ctx, boot.Config{StateRoot: root, Socket: os.Getenv("LINA_TEST_PROGRESS_SOCKET"), Conversation: route, ChannelTransport: transport}); err != nil {
		t.Fatal(err)
	}
}
