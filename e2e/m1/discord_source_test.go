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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// sourceScenario is e2e/m2/discord-source.json.
type sourceScenario struct {
	Platform, Account, Owner, Guild, Chat, Token string
	First                                        []struct {
		ID, Author, Text string
		Bot              bool
	}
	Edit struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	Delete struct {
		ID string `json:"id"`
	}
	Next struct {
		ID     string `json:"id"`
		Author string `json:"author"`
		Text   string `json:"text"`
	}
	OwnerMessage struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"owner_message"`
}

// TestM2DiscordSourceCore runs the Discord bot source inside an installed Core
// against synthetic REST and Gateway endpoints. It proves the first fill with a
// rate-limited page, incremental paging from the committed cursor after a
// restart, an edit and a deletion from live events, and that a channel
// connection sharing the bot token is served by one Gateway session while the
// source records the same messages.
func TestM2DiscordSourceCore(t *testing.T) {
	t.Parallel()
	scenario := readSourceScenario(t)
	platform := syntheticDiscord(t, scenario)
	deliver := platform
	defer platform.Close()

	h := newHarness(t, "")
	installMessengerCore(t, h)
	seedDiscordSource(t, h, scenario, deliver)

	stop := startSourceCore(t, h, platform)
	waitRecords(t, h, 3)
	if got := deliver.calls("0"); got < 2 {
		t.Fatalf("first fill requests for the oldest page = %d, want the retried 429", got)
	}
	rows := readRecords(t, h)
	if len(rows) != 3 {
		t.Fatalf("records = %v", rows)
	}
	// Speaker kind comes from the platform's own data: the paired owner is the
	// person, a bot author is automation and anyone else is a participant.
	want := map[string]string{scenario.First[0].ID: "person", scenario.First[1].ID: "automation", scenario.First[2].ID: "external_participant"}
	for id, kind := range want {
		if rows[id].speaker != kind {
			t.Fatalf("record %s speaker = %q, want %q", id, rows[id].speaker, kind)
		}
	}
	if rows[scenario.First[0].ID].conversation != "messenger:"+scenario.Platform+":"+scenario.Account+":"+scenario.Chat {
		t.Fatalf("conversation = %q", rows[scenario.First[0].ID].conversation)
	}

	// Live events: an edit supersedes, a deletion hides through the removal
	// list, and a new message admits without waiting for the next schedule.
	send(deliver, sourceEvent("MESSAGE_UPDATE", scenario.Chat, scenario.Guild, map[string]any{
		"id": scenario.Edit.ID, "channel_id": scenario.Chat, "guild_id": scenario.Guild,
		"content": scenario.Edit.Text, "type": 0, "timestamp": "2026-10-24T08:00:00.000000+00:00",
		"edited_timestamp": "2026-10-24T08:05:00.000000+00:00",
		"author":           map[string]any{"id": scenario.Owner, "username": "owner"},
	}))
	send(deliver, sourceEvent("MESSAGE_DELETE", scenario.Chat, scenario.Guild, map[string]any{
		"id": scenario.Delete.ID, "channel_id": scenario.Chat, "guild_id": scenario.Guild,
	}))
	send(deliver, sourceEvent("MESSAGE_CREATE", scenario.Chat, scenario.Guild, map[string]any{
		"id": "900000000000000009", "channel_id": scenario.Chat, "guild_id": scenario.Guild,
		"content": "live now", "type": 0, "timestamp": "2026-10-24T08:06:00.000000+00:00",
		"author": map[string]any{"id": "600", "username": "guest"},
	}))
	// One message from the paired owner in the bound chat reaches both the
	// channel binding and the source over the same session.
	send(deliver, sourceEvent("MESSAGE_CREATE", scenario.Chat, "", map[string]any{
		"id": scenario.OwnerMessage.ID, "channel_id": scenario.Chat,
		"content": scenario.OwnerMessage.Text, "type": 0, "timestamp": "2026-10-24T08:07:00.000000+00:00",
		"author": map[string]any{"id": scenario.Owner, "username": "owner"},
	}))
	waitEdited(t, h, scenario.Edit.ID, scenario.Edit.Text)
	waitDeleted(t, h, scenario.Delete.ID)
	waitRecords(t, h, 4)
	waitReceipt(t, h, scenario.OwnerMessage.ID)
	if got := deliver.maxConcurrent(); got != 1 {
		t.Fatalf("concurrent Gateway sessions = %d, want one per bot token", got)
	}
	if got := deliver.identifies(); got > 2 {
		t.Fatalf("Gateway identifies = %d, want one session with at most one intent change", got)
	}
	stop()

	// A restart resumes from the committed cursor: the platform is asked for
	// what came after the newest record, and nothing is stored twice.
	deliver.phase(2)
	stop = startSourceCore(t, h, platform)
	waitCursor(t, h, scenario.Delete.ID, scenario.Next.ID)
	stop()
	if got := deliver.calls(scenario.First[0].ID); got != 0 {
		t.Fatalf("the restart read the first page again: %d", got)
	}
	if got := deliver.calls(scenario.First[2].ID); got == 0 {
		t.Fatal("the restart did not continue after the committed cursor")
	}
	if n := countRows(t, h, "SELECT count(*) FROM messenger_record WHERE state='live'"); n < 4 {
		t.Fatalf("live records after restart = %d", n)
	}
	if n := countRows(t, h, "SELECT count(*) FROM messenger_record WHERE message=?", scenario.Next.ID); n != 1 {
		t.Fatalf("the incremental page was not admitted: %d", n)
	}
}

// TestM2DiscordSourceWithoutMessageTextCore proves that a bot whose
// message-content intent is off reports that text is unavailable and stores no
// empty records, instead of reading quietly.
func TestM2DiscordSourceWithoutMessageTextCore(t *testing.T) {
	t.Parallel()
	scenario := readSourceScenario(t)
	platform := syntheticDiscord(t, scenario)
	deliver := platform
	defer platform.Close()
	deliver.withoutText()

	h := newHarness(t, "")
	installMessengerCore(t, h)
	seedDiscordSource(t, h, scenario, deliver)
	stop := startSourceCore(t, h, platform)
	waitFailure(t, h, "message text is unavailable")
	stop()
	if n := countRows(t, h, "SELECT count(*) FROM messenger_record"); n != 0 {
		t.Fatalf("records without text = %d, want none", n)
	}
	if n := countRows(t, h, "SELECT count(*) FROM conv_event WHERE origin LIKE '%messenger%'"); n != 0 {
		t.Fatalf("events without text = %d, want none", n)
	}
}

func readSourceScenario(t *testing.T) sourceScenario {
	t.Helper()
	data, err := os.ReadFile("../m2/discord-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario sourceScenario
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	return scenario
}

// installMessengerCore prepares an installed state root, then seeds the source,
// its token and a channel connection for the same bot account.
func installMessengerCore(t *testing.T, h *harness) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bundle := makeInstallBundle(t, t.TempDir(), executable, executable)
	provenance, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"canon", "system/bin", "system/core"} {
		if err = os.MkdirAll(filepath.Join(h.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(h.root, "system/bin/manifest.json"), provenance, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(h.root, "system/core/main.json"), []byte(`{"main_id":"discord-source"}`), 0600); err != nil {
		t.Fatal(err)
	}
}

// seedDiscordSource writes what the person's connect did before Core starts:
// the token, the bot source with the confirmed owner id, its chat, and a
// channel connection that will pair a channel binding on the same bot.
func seedDiscordSource(t *testing.T, h *harness, scenario sourceScenario, _ *discordPlatform) {
	t.Helper()
	person := "person:discord-source"
	vault, err := secrets.OpenFiles(h.root)
	if err != nil {
		t.Fatal(err)
	}
	if err = vault.Put(secrets.Key{Person: person, Service: scenario.Platform, Name: scenario.Account}, []byte(scenario.Token)); err != nil {
		t.Fatal(err)
	}
	vault.Close()
	db, closeStore := installedAcceptanceStore(t, h)
	defer closeStore()
	agentsStore, err := agents.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := agentsStore.Create(h.ctx, agents.Create{Name: "Synthetic", PersonID: person, Layers: agents.Layers{Core: "core", DefaultVoice: "voice"}})
	agentsStore.Close()
	if err != nil {
		t.Fatal(err)
	}
	bot := channels.Bot{Platform: scenario.Platform, Account: scenario.Account}
	channelsStore := channels.New(db)
	if err = channelsStore.Connect(h.ctx, bot, person); err != nil {
		t.Fatal(err)
	}
	code, err := channelsStore.PairingCode(h.ctx, bot, person, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = channelsStore.Pair(h.ctx, bot, code, scenario.Owner, scenario.Chat, time.Now()); err != nil {
		t.Fatal(err)
	}
	address := channels.Address{Bot: bot, Chat: scenario.Chat}
	if err = channelsStore.Bind(h.ctx, channels.Binding{Address: address, Agent: agent.ID, Person: person, Conversation: "main:" + agent.ID, Scope: channels.PrivateScope()}); err != nil {
		t.Fatal(err)
	}
	source := messenger.Source{Person: person, Platform: scenario.Platform, Account: scenario.Account, Owner: scenario.Owner,
		Mode: messenger.ModeOfficial, Adapter: scenario.Platform, State: messenger.StateConnected, ConnectedAt: time.Now()}
	store := messenger.Open(db, nil, nil)
	if err = store.Connect(h.ctx, source); err != nil {
		t.Fatal(err)
	}
	if err = store.UpsertChats(h.ctx, source, []messenger.Chat{{ID: scenario.Chat, Name: "general", Kind: "channel"}}); err != nil {
		t.Fatal(err)
	}
}

// startSourceCore runs production boot in this process with the synthetic
// platform endpoints, and returns a stop function.
func startSourceCore(t *testing.T, h *harness, platform *discordPlatform) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan error, 1)
	go func() {
		done <- boot.Run(ctx, boot.Config{StateRoot: h.root, Socket: h.socket,
			Messenger:        &boot.MessengerConfig{Transport: platform.transport, Interval: 200 * time.Millisecond},
			ChannelTransport: platform.transport,
		})
	}()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("discord source Core stopped with %v", err)
			}
		case <-time.After(3 * operationTimeout):
			t.Fatal("discord source Core shutdown timed out")
		}
	}
}

type recordRow struct {
	speaker, conversation, state, body string
}

// openReader reads canon while the running Core holds the writer lock.
func openReader(t *testing.T, h *harness) *canon.Store {
	t.Helper()
	reader, err := canon.OpenReader(context.Background(), filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close(context.Background()) })
	return reader
}

func readRecords(t *testing.T, h *harness) map[string]recordRow {
	t.Helper()
	db := openReader(t, h)
	rows := map[string]recordRow{}
	if err := db.Read(context.Background(), func(tx *canon.Tx) error {
		cursor, err := tx.QueryContext(context.Background(), `SELECT r.message,r.state,e.speaker_kind,e.conversation_id,e.body FROM messenger_record r
JOIN conv_event e ON e.id=r.event`)
		if err != nil {
			return err
		}
		defer cursor.Close()
		for cursor.Next() {
			var id string
			var row recordRow
			if err := cursor.Scan(&id, &row.state, &row.speaker, &row.conversation, &row.body); err != nil {
				return err
			}
			rows[id] = row
		}
		return cursor.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return rows
}

func countRows(t *testing.T, h *harness, query string, args ...any) int {
	t.Helper()
	db := openReader(t, h)
	var n int
	if err := db.Read(context.Background(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(context.Background(), query, args...).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitRecords(t *testing.T, h *harness, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * operationTimeout)
	for time.Now().Before(deadline) {
		if n := countRows(t, h, "SELECT count(*) FROM messenger_record WHERE state='live'"); n >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("live records = %d, want %d", countRows(t, h, "SELECT count(*) FROM messenger_record WHERE state='live'"), want)
}

func waitEdited(t *testing.T, h *harness, id, text string) {
	t.Helper()
	deadline := time.Now().Add(2 * operationTimeout)
	for time.Now().Before(deadline) {
		if n := countRows(t, h, "SELECT count(*) FROM conv_event WHERE body=? AND speaker_kind='person'", text); n == 1 {
			if countRows(t, h, "SELECT count(*) FROM ev_rel WHERE kind='supersedes'") == 1 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the edit of %s did not supersede its earlier record", id)
}

func waitDeleted(t *testing.T, h *harness, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * operationTimeout)
	for time.Now().Before(deadline) {
		if countRows(t, h, "SELECT count(*) FROM messenger_record WHERE message=? AND state='deleted'", id) == 1 &&
			countRows(t, h, "SELECT count(*) FROM removal_applied") >= 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the platform deletion did not hide the record")
}

func waitReceipt(t *testing.T, h *harness, native string) {
	t.Helper()
	deadline := time.Now().Add(2 * operationTimeout)
	for time.Now().Before(deadline) {
		if n := countRows(t, h, "SELECT count(*) FROM channel_receipts WHERE native_id=?", native); n == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the channel binding did not receive the shared event")
}

func waitCursor(t *testing.T, h *harness, previous, next string) {
	t.Helper()
	deadline := time.Now().Add(2 * operationTimeout)
	for time.Now().Before(deadline) {
		if countRows(t, h, "SELECT count(*) FROM messenger_record WHERE message=?", next) == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the incremental page after %s did not arrive", previous)
}

func waitFailure(t *testing.T, h *harness, cause string) {
	t.Helper()
	deadline := time.Now().Add(2 * operationTimeout)
	for time.Now().Before(deadline) {
		if n := countRows(t, h, "SELECT count(*) FROM messenger_source WHERE state='failing' AND failure LIKE ?", "%"+cause+"%"); n == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the source did not report that message text is unavailable")
}

// sourceEvent builds one Gateway dispatch frame.
func sourceEvent(kind, chat, guild string, data map[string]any) map[string]any {
	return map[string]any{"op": 0, "t": kind, "d": data}
}

func send(d *discordPlatform, frame map[string]any) { d.queue(frame) }

// discordPlatform is the synthetic Discord REST and Gateway. The REST side
// serves the platform's read paths; the Gateway side counts sessions and
// identifies and replays every queued dispatch to each socket.
type discordPlatform struct {
	*httptest.Server
	scenario  sourceScenario
	transport http.RoundTripper

	mu        sync.Mutex
	queued    []map[string]any
	identify  atomic.Int32
	open      atomic.Int32
	max       atomic.Int32
	requested map[string]int
	retried   bool
	phaseNo   atomic.Int32
	textOff   atomic.Bool
}

func syntheticDiscord(t *testing.T, scenario sourceScenario) *discordPlatform {
	t.Helper()
	p := &discordPlatform{scenario: scenario, requested: map[string]int{}, phaseNo: atomic.Int32{}}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	endpoint, err := url.Parse(p.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.transport = pairingTransport(func(req *http.Request) (*http.Response, error) {
		wire := req.Clone(req.Context())
		wire.URL.Scheme = endpoint.Scheme
		wire.URL.Host = endpoint.Host
		if req.URL.Host == "gateway.discord.gg" {
			wire.URL.Path = "/gateway"
		}
		return http.DefaultTransport.RoundTrip(wire)
	})
	return p
}

func (p *discordPlatform) phase(n int) { p.phaseNo.Store(int32(n)) }

func (p *discordPlatform) withoutText() { p.textOff.Store(true) }

func (p *discordPlatform) queue(frame map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queued = append(p.queued, frame)
}

func (p *discordPlatform) calls(after string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requested[after]
}

func (p *discordPlatform) identifies() int { return int(p.identify.Load()) }
func (p *discordPlatform) maxConcurrent() int {
	return int(p.max.Load())
}

func (p *discordPlatform) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/gateway" {
		p.gateway(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v10")
	switch {
	case path == "/users/@me":
		p.json(w, map[string]any{"id": p.scenario.Account, "bot": true})
	case path == "/users/@me/guilds":
		p.json(w, []map[string]any{{"id": p.scenario.Guild, "name": "Guild"}})
	case path == "/guilds/"+p.scenario.Guild+"/channels":
		p.json(w, []map[string]any{{"id": p.scenario.Chat, "name": "general", "type": 0}})
	case path == "/channels/"+p.scenario.Chat:
		p.json(w, map[string]any{"id": p.scenario.Chat, "type": 0})
	case path == "/guilds/"+p.scenario.Guild+"/threads/active":
		p.json(w, map[string]any{"threads": []any{}, "has_more": false})
	case strings.HasPrefix(path, "/channels/") && strings.HasSuffix(path, "/threads/archived/public"):
		p.json(w, map[string]any{"threads": []any{}, "has_more": false})
	case path == "/channels/"+p.scenario.Chat+"/messages":
		p.messages(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (p *discordPlatform) json(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (p *discordPlatform) messages(w http.ResponseWriter, r *http.Request) {
	after := r.URL.Query().Get("after")
	p.mu.Lock()
	p.requested[after]++
	first := p.requested[after]
	p.mu.Unlock()
	if after == "0" && first == 1 {
		// One platform wait, then the same page. The adapter must keep its
		// cursor and continue rather than skip the page.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"You are being rate limited.","retry_after":0.05,"global":false}`))
		return
	}
	message := func(id, author, text string, bot bool) map[string]any {
		if p.textOff.Load() {
			text = ""
		}
		return map[string]any{"id": id, "channel_id": p.scenario.Chat, "guild_id": p.scenario.Guild,
			"content": text, "type": 0, "timestamp": "2026-10-24T08:00:00.000000+00:00",
			"author": map[string]any{"id": author, "username": "member", "bot": bot}}
	}
	switch after {
	case "0":
		var page []map[string]any
		for _, m := range p.scenario.First {
			page = append(page, message(m.ID, m.Author, m.Text, m.Bot))
		}
		p.json(w, page)
	case p.scenario.Delete.ID, p.scenario.First[2].ID, p.scenario.Next.ID:
		if p.phaseNo.Load() == 2 || after == p.scenario.Next.ID {
			p.json(w, []map[string]any{message(p.scenario.Next.ID, p.scenario.Next.Author, p.scenario.Next.Text, false)})
			return
		}
		p.json(w, []any{})
	default:
		p.json(w, []any{})
	}
}

// gateway serves one WebSocket session and replays queued dispatches to it.
func (p *discordPlatform) gateway(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	open := p.open.Add(1)
	for {
		max := p.max.Load()
		if open <= max || p.max.CompareAndSwap(max, open) {
			break
		}
	}
	defer p.open.Add(-1)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if wsjson.Write(ctx, conn, map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 1000}}) != nil {
		return
	}
	var auth struct {
		Op int `json:"op"`
	}
	if wsjson.Read(ctx, conn, &auth) != nil {
		return
	}
	if auth.Op == 2 {
		p.identify.Add(1)
	}
	if wsjson.Write(ctx, conn, map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{"session_id": fmt.Sprintf("session-%d", p.identify.Load()), "user": map[string]any{"id": p.scenario.Account, "bot": true}}}) != nil {
		return
	}
	seq := int64(1)
	sent := 0
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := make(chan struct{})
	go func() {
		defer close(heartbeat)
		for {
			var frame struct {
				Op int `json:"op"`
			}
			if wsjson.Read(ctx, conn, &frame) != nil {
				cancel()
				return
			}
			if frame.Op == 1 {
				if wsjson.Write(ctx, conn, map[string]any{"op": 11, "d": nil}) != nil {
					cancel()
					return
				}
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat:
			return
		case <-ticker.C:
			p.mu.Lock()
			pending := append([]map[string]any(nil), p.queued[sent:]...)
			total := len(p.queued)
			p.mu.Unlock()
			for _, frame := range pending {
				seq++
				body := map[string]any{"op": 0, "t": frame["t"], "s": seq, "d": frame["d"]}
				if wsjson.Write(ctx, conn, body) != nil {
					return
				}
			}
			sent = total
		}
	}
}
